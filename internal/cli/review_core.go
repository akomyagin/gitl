package cli

// The cmd-free core of `gitl review`: everything a non-CLI caller (e.g. a
// future MCP server) needs to produce a review artifact, with zero dependency
// on cobra/pflag. The cobra wrapper (runReview in review.go) composes the SAME
// building blocks — prepareReview, lookupCache, costGuard, selectProvider,
// complete — adding only the CLI-specific concerns on top (flag parsing,
// --dry-run, terminal streaming, rendering to stdout), so the two paths cannot
// diverge.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/llmcache"
	"github.com/akomyagin/gitl/internal/prompt"
	"github.com/akomyagin/gitl/internal/render"
)

// ReviewOptions carries the per-run review parameters that are not part of the
// merged config. Deliberately plain Go types only — no *cobra.Command, no
// *pflag.FlagSet — so non-CLI callers can fill it directly (config.Load with
// Options{Flags: nil} still applies defaults→file→env).
type ReviewOptions struct {
	// NoCache skips the on-disk LLM response cache entirely (the --no-cache
	// flag): no lookup, no store.
	NoCache bool
	// ErrOut receives user-facing warnings (the offline-mode notice, cost-guard
	// warnings). The CLI passes its stderr; nil discards them.
	ErrOut io.Writer
}

// errWriter returns the warning sink, defaulting to io.Discard.
func (o ReviewOptions) errWriter() io.Writer {
	if o.ErrOut != nil {
		return o.ErrOut
	}
	return io.Discard
}

// reviewPlan is the prepared state of one review: the shaped diff, the built
// prompts, and the cache handle. It is produced once by prepareReview and
// consumed by both RunReviewCore (buffered) and the CLI streaming branch.
type reviewPlan struct {
	cfg      *config.Config
	src      diffSource
	diff     string // diff after exclude_globs filtering and truncation
	system   string
	user     string
	cache    *llmcache.Cache // nil when the cache is disabled or unavailable
	cacheKey string
}

// prepareReview shapes the diff, builds the prompts, and initializes the LLM
// response cache for one resolved diffSource. It performs no network I/O and
// writes nothing to stdout/stderr.
func prepareReview(cfg *config.Config, src diffSource, opts ReviewOptions) (*reviewPlan, error) {
	// Config-driven diff shaping: drop excluded files, then truncate to
	// max_diff_bytes with an explicit marker (§6).
	diff := filterDiffByGlobs(src.Diff, mergedExcludeGlobs(cfg))
	diff = truncateDiff(diff, cfg.Diff.MaxDiffBytes)
	// Post-shaping emptiness check for the non-range modes (staged, pr/N):
	// their whole point is the diff, so an all-excluded diff is a clear user
	// error, not a silent empty review. Dispatch on the explicit Mode, never
	// on the label — a range over a branch literally named pr/5 must keep
	// range semantics.
	if strings.TrimSpace(diff) == "" {
		switch src.Mode {
		case modeStaged:
			return nil, fmt.Errorf("no staged changes to review: all staged files are excluded by exclude_globs")
		case modePR:
			return nil, fmt.Errorf("no reviewable changes in %s: the PR diff is empty or all files are excluded by exclude_globs", src.Label)
		case modeRange:
			// Historical behavior: commit metadata alone can be worth reviewing.
		}
	}

	// Custom prompt templates apply only when calling a real provider; the
	// offline provider ignores the system prompt, so pass an empty path in
	// offline mode to keep behavior deterministic.
	systemTemplateFile := cfg.Prompt.SystemTemplateFile
	if cfg.OfflineMode() {
		systemTemplateFile = ""
	}
	system, user, err := prompt.BuildReviewWithTemplate(prompt.Review{
		Range:   src.Label,
		Commits: src.Commits,
		Diff:    diff,
		Staged:  src.Staged,
	}, systemTemplateFile)
	if err != nil {
		return nil, fmt.Errorf("prompt template: %w", err)
	}

	plan := &reviewPlan{cfg: cfg, src: src, diff: diff, system: system, user: user}

	// LLM response cache (Item 5): only active for network reviews — offline
	// mode is already deterministic and free, so it is never cached.
	// The key hashes provider+model+system+user, and the user message embeds
	// the full diff — so staged mode is content-addressed by the staged diff
	// itself (no range exists to key on): re-running with the same index hits
	// the cache, any `git add` changes the diff and therefore the key.
	useCache := cfg.Cache.Enabled && !opts.NoCache && !cfg.OfflineMode() && cfg.Cache.TTLHours > 0
	if useCache {
		if c, cerr := llmcache.New(time.Duration(cfg.Cache.TTLHours) * time.Hour); cerr == nil {
			plan.cache = c
			plan.cacheKey = llmcache.Key(cfg.LLM.Provider, cfg.LLM.Model, system, user)
		} else {
			slog.Debug("llm cache unavailable", "err", cerr)
		}
	}
	return plan, nil
}

// promptText is the full prompt as fed to the cost estimator (§8.3/§8.4).
func (p *reviewPlan) promptText() string {
	return p.system + "\n" + p.user
}

// request builds the provider request shared by the streaming and buffered paths.
func (p *reviewPlan) request() llm.Request {
	return llm.Request{
		System:      p.system,
		User:        p.user,
		Model:       p.cfg.LLM.Model,
		MaxTokens:   p.cfg.LLM.MaxTokens,
		Temperature: p.cfg.LLM.Temperature,
		Commits:     p.src.Commits,
		Diff:        p.diff,
	}
}

// lookupCache serves an equivalent prior review without a network call. ok is
// false when the cache is disabled, unavailable, or has no fresh entry.
func (p *reviewPlan) lookupCache() (render.Artifact, bool) {
	if p.cache == nil || p.cacheKey == "" {
		return render.Artifact{}, false
	}
	resp, ok, _ := p.cache.Get(p.cacheKey)
	if !ok {
		return render.Artifact{}, false
	}
	slog.Debug("llm cache hit", "key", p.cacheKey[:12])
	return buildArtifact(p.cfg, p.src.Label, p.src.Commits, p.diff, resp), true
}

// storeCache persists a provider response for later lookupCache hits. A put
// failure is debug-logged, never fatal.
func (p *reviewPlan) storeCache(resp llm.Response) {
	if p.cache == nil || p.cacheKey == "" {
		return
	}
	if err := p.cache.Put(p.cacheKey, resp); err != nil {
		slog.Debug("llm cache put failed", "err", err)
	}
}

// complete runs the buffered (non-streaming) provider call and assembles the
// final artifact, storing the response in the cache on the way.
func (p *reviewPlan) complete(ctx context.Context, provider llm.Provider) (render.Artifact, error) {
	slog.Debug("requesting review", "commits", len(p.src.Commits), "diff_bytes", len(p.diff), "offline", p.cfg.OfflineMode())
	resp, err := provider.Complete(ctx, p.request())
	if err != nil {
		return render.Artifact{}, fmt.Errorf("review failed: %w", err)
	}
	p.storeCache(resp)
	return buildArtifact(p.cfg, p.src.Label, p.src.Commits, p.diff, resp), nil
}

// RunReviewCore executes the full review pipeline for one resolved diffSource
// without any cobra dependency and without writing the result anywhere: diff
// shaping, prompt build, cache lookup, cost guard, provider selection, the
// buffered provider call, and cache store. It returns the final artifact ready
// for the render package (md|text|json).
//
// CLI-only concerns intentionally live in runReview instead: --dry-run (prints
// an estimate, produces no artifact), terminal token streaming, rendering, and
// the --fail-on exit-code gate.
func RunReviewCore(ctx context.Context, cfg *config.Config, src diffSource, opts ReviewOptions) (render.Artifact, error) {
	plan, err := prepareReview(cfg, src, opts)
	if err != nil {
		return render.Artifact{}, err
	}
	if art, ok := plan.lookupCache(); ok {
		return art, nil
	}
	// Cost guard runs automatically before calling the provider (§8.4), skipped
	// in offline mode (no call, no cost).
	if !cfg.OfflineMode() {
		if err := costGuard(opts.errWriter(), cfg, plan.promptText()); err != nil {
			return render.Artifact{}, err
		}
	}
	provider, err := selectProvider(opts.errWriter(), cfg, src.Commits, plan.diff)
	if err != nil {
		return render.Artifact{}, err
	}
	return plan.complete(ctx, provider)
}

// gateFailOn implements the --fail-on CI gate (§9): a non-nil failError when
// the risk level meets the configured threshold. Callers invoke it LAST, after
// the review has been printed, so the tool always shows its reasoning before
// failing.
func gateFailOn(cfg *config.Config, riskLevel string) error {
	threshold := cfg.Policy.FailOn
	if threshold != "" && threshold != "never" && llm.RiskAtLeast(riskLevel, threshold) {
		return &failError{level: riskLevel, threshold: threshold}
	}
	return nil
}
