package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/llmcache"
	"github.com/akomyagin/gitl/internal/prompt"
	"github.com/akomyagin/gitl/internal/render"
)

// failError signals that the risk score met the --fail-on threshold. It is
// returned AFTER the review is printed, so the tool always shows its reasoning
// before failing (a deliberate project principle — see TECHNICAL_PLAN §9).
type failError struct {
	level     string
	threshold string
}

func (e *failError) Error() string {
	return fmt.Sprintf("review risk %q meets --fail-on=%s threshold", e.level, e.threshold)
}

// byteCountWriter wraps an io.Writer and tracks how many bytes have been
// written. The streaming branch uses it to detect pre-first-token failures
// so it can safely fall back to the non-streaming Complete path.
type byteCountWriter struct {
	w       io.Writer
	written int64
}

func (c *byteCountWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.written += int64(n)
	return n, err
}

// newReviewCmd builds the `gitl review <range>` command.
func newReviewCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review [<range> | pr/<N>]",
		Short: "AI review of a commit range (e.g. HEAD~5..HEAD), a GitHub PR (pr/N), or staged changes",
		Long: "review runs `git log` + `git diff` over the given revision range, sends the\n" +
			"result to an LLM, and prints a review (md/text/json) to stdout with a\n" +
			"structured risk score.\n\n" +
			"With pr/N (e.g. `gitl review pr/42`) it reviews a GitHub pull request by\n" +
			"number: the PR's base/head SHAs are resolved via the GitHub CLI (`gh`,\n" +
			"which must be installed and authenticated), the head is fetched locally\n" +
			"via `pull/N/head` if needed (forks included), and the diff is computed\n" +
			"from the merge-base (`base...head`) — the same diff GitHub shows.\n\n" +
			"With --staged it reviews the staged (indexed, not yet committed) changes\n" +
			"via `git diff --cached` instead — no revision range is needed, a natural\n" +
			"pre-commit check. --staged, <range>, and pr/N are mutually exclusive.\n\n" +
			"Without an API key (GITL_API_KEY or llm.api_key) it falls back to a\n" +
			"deterministic offline review and prints a warning to stderr.\n\n" +
			"--dry-run prints a cost estimate and exits without calling the API.\n" +
			"--fail-on gates CI: exit non-zero when the risk level meets the threshold.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			staged, err := cmd.Flags().GetBool("staged")
			if err != nil {
				return err
			}
			switch {
			case staged && len(args) > 0:
				return fmt.Errorf("cannot combine --staged with a revision range or pr/N: --staged reviews the index — pick one")
			case !staged && len(args) == 0:
				return fmt.Errorf("provide a revision range (e.g. HEAD~5..HEAD), a PR number (pr/N), or --staged to review staged changes")
			}

			ctx := cmd.Context()
			runner, err := gitlog.NewRunner("")
			if err != nil {
				return err
			}

			var src diffSource
			switch {
			case staged:
				src, err = stagedSource(ctx, runner)
			default:
				isPR, prNum, argErr := classifyReviewArg(args[0])
				if argErr != nil {
					return argErr
				}
				if isPR {
					resolver, rErr := newPRResolver("")
					if rErr != nil {
						return rErr
					}
					src, err = prSource(ctx, runner, resolver, prNum)
				} else {
					src, err = rangeSource(ctx, runner, args[0])
				}
			}
			if err != nil {
				return err
			}
			return runReview(ctx, cmd, gf, src)
		},
	}

	// Flags bound into config (see config.bindChangedFlags). Only override
	// config when explicitly set.
	cmd.Flags().String("provider", "", "LLM provider (openai | ollama | azure_openai)")
	cmd.Flags().String("model", "", "model name")
	cmd.Flags().String("base-url", "", "LLM API base URL")
	cmd.Flags().String("format", "", "output format (md | text | json)")
	cmd.Flags().String("fail-on", "", "exit non-zero when risk meets threshold (never | low | medium | high)")
	cmd.Flags().Float64("max-cost-usd", 0, "block the request if the estimated cost exceeds this (<=0 disables the guard)")
	cmd.Flags().Bool("dry-run", false, "print a cost estimate and exit without calling the API")
	cmd.Flags().Bool("no-cache", false, "skip LLM response cache (always call the API)")
	cmd.Flags().Bool("no-stream", false, "disable token-by-token streaming (wait for full response)")
	cmd.Flags().Bool("staged", false, "review staged (indexed, not yet committed) changes instead of a revision range")

	return cmd
}

// prPattern matches the pr/N positional argument of `gitl review`. Negative
// or non-numeric suffixes (pr/-1, pr/abc) deliberately do NOT match — they
// fall through to range mode and fail with the natural git error.
var prPattern = regexp.MustCompile(`^pr/(\d+)$`)

// classifyReviewArg decides whether the positional argument selects PR mode
// (pr/N) or a plain revision range. It only errors on a matching-but-invalid
// PR number (pr/0 and numbers too large for int); anything that does not
// match the pattern is a range, never an error.
func classifyReviewArg(arg string) (isPR bool, prNum int, err error) {
	m := prPattern.FindStringSubmatch(arg)
	if m == nil {
		return false, 0, nil
	}
	n, convErr := strconv.Atoi(m[1])
	if convErr != nil || n < 1 {
		return false, 0, fmt.Errorf("invalid PR number in %q (must be a positive integer)", arg)
	}
	return true, n, nil
}

// reviewMode discriminates how the diffSource was selected. An explicit field
// rather than sniffing src.Label: a range like "pr/5..HEAD" (a real branch
// named pr/5) must NOT be mistaken for PR mode by a string-prefix check.
type reviewMode int

const (
	modeRange reviewMode = iota
	modeStaged
	modePR
)

// diffSource is the resolved input for one review, regardless of whether it
// was selected by a git range, the index (--staged), or a PR number (pr/N).
// runReview operates only on this — no per-mode branching downstream.
type diffSource struct {
	Commits []gitlog.Commit // empty for staged
	Diff    string          // raw diff BEFORE shaping (exclude/truncate)
	Label   string          // display label: "staged", "HEAD~5..HEAD", "pr/42"
	Staged  bool            // switches prompt.Review to the staged user message
	Mode    reviewMode      // which constructor produced this source
}

// stagedSource collects the staged (indexed) diff. An empty index is a clear
// user error, not a silent empty review.
func stagedSource(ctx context.Context, runner *gitlog.Runner) (diffSource, error) {
	slog.Debug("collecting staged diff")
	rawDiff, err := runner.DiffStaged(ctx)
	if err != nil {
		return diffSource{}, err
	}
	if strings.TrimSpace(rawDiff) == "" {
		return diffSource{}, fmt.Errorf("no staged changes to review (stage files with `git add` first)")
	}
	return diffSource{Diff: rawDiff, Label: "staged", Staged: true, Mode: modeStaged}, nil
}

// rangeSource collects the historical log+diff pair for a revision range.
func rangeSource(ctx context.Context, runner *gitlog.Runner, revRange string) (diffSource, error) {
	slog.Debug("collecting git history", "range", revRange)
	commits, err := runner.Log(ctx, revRange)
	if err != nil {
		return diffSource{}, err
	}
	if len(commits) == 0 {
		return diffSource{}, fmt.Errorf("no commits found in range %q", revRange)
	}
	rawDiff, err := runner.Diff(ctx, revRange)
	if err != nil {
		return diffSource{}, err
	}
	return diffSource{Commits: commits, Diff: rawDiff, Label: revRange, Mode: modeRange}, nil
}

// prSource resolves a GitHub PR number into a local diff: SHAs come from the
// resolver (gh), the head/base objects are fetched best-effort ONLY when not
// already available locally, and the diff is the merge-base triple-dot
// `base...head` — the exact diff GitHub shows for a PR. Commits are the PR's
// own commits (`base..head`).
func prSource(ctx context.Context, runner *gitlog.Runner, resolver PRResolver, prNum int) (diffSource, error) {
	ref, err := resolver.ResolvePR(ctx, prNum)
	if err != nil {
		return diffSource{}, err
	}
	label := fmt.Sprintf("pr/%d", prNum)
	slog.Debug("resolved pull request", "pr", label, "base", ref.BaseSHA, "head", ref.HeadSHA, "head_ref", ref.HeadRef, "cross_repo", ref.CrossRepo)

	// gh resolves the PR against its own notion of the repository, which is
	// NOT necessarily the remote named "origin" (fork workflows: origin = the
	// fork, upstream = where the PR and its pull/N/head ref actually live).
	// Match the PR's owner/repo against the local remotes; "origin" remains
	// the fallback when nothing matches (single-remote case, GHE URLs, ...).
	remote := "origin"
	if owner, repo, ok := parseGitHubOwnerRepo(ref.URL); ok {
		remote = resolveRemoteName(ctx, runner, owner, repo)
	}

	// Best-effort fetch: skip fetching objects already present locally. The
	// pull/N/head refspec works for cross-repository (fork) PRs too. Each SHA
	// is probed once up front and re-probed only after an actual fetch (a
	// successful fetch does not guarantee the SHA: the PR may have been
	// force-pushed between `gh pr view` and the fetch).
	headOK := runner.ObjectExists(ctx, ref.HeadSHA)
	if !headOK {
		if err := runner.FetchRef(ctx, remote, fmt.Sprintf("pull/%d/head", prNum)); err != nil {
			return diffSource{}, fmt.Errorf("fetching %s head: %w", label, err)
		}
		headOK = runner.ObjectExists(ctx, ref.HeadSHA)
	}
	baseOK := runner.ObjectExists(ctx, ref.BaseSHA)
	if !baseOK {
		// Fetch the base by branch name when gh reported one — bare-SHA
		// fetches only work on servers with allowReachableSHA1InWant (github.com
		// default, but not universal). Fall back to the SHA if the name is absent.
		baseRef := ref.BaseRefName
		if baseRef == "" {
			baseRef = ref.BaseSHA
		}
		if err := runner.FetchRef(ctx, remote, baseRef); err != nil {
			return diffSource{}, fmt.Errorf("fetching %s base: %w", label, err)
		}
		baseOK = runner.ObjectExists(ctx, ref.BaseSHA)
	}
	if !headOK || !baseOK {
		return diffSource{}, fmt.Errorf("could not resolve PR #%d commits locally after fetching — your clone may be shallow (run `git fetch --unshallow`), or the PR may have been updated concurrently (retry)", prNum)
	}

	commits, err := runner.Log(ctx, ref.BaseSHA+".."+ref.HeadSHA)
	if err != nil {
		return diffSource{}, err
	}
	diff, err := runner.Diff(ctx, ref.BaseSHA+"..."+ref.HeadSHA)
	if err != nil {
		return diffSource{}, err
	}
	return diffSource{Commits: commits, Diff: diff, Label: label, Mode: modePR}, nil
}

// runReview executes the full review pipeline for one resolved diffSource
// (range, staged index, or PR — the source constructors have already done the
// per-mode collection and validation).
func runReview(ctx context.Context, cmd *cobra.Command, gf *globalFlags, src diffSource) error {
	cfg, err := loadConfig(cmd, gf)
	if err != nil {
		return err
	}
	dryRun, err := cmd.Flags().GetBool("dry-run")
	if err != nil {
		return err
	}

	commits := src.Commits

	// Config-driven diff shaping: drop excluded files, then truncate to
	// max_diff_bytes with an explicit marker (§6).
	excludeGlobs := mergedExcludeGlobs(cfg)
	diff := filterDiffByGlobs(src.Diff, excludeGlobs)
	diff = truncateDiff(diff, cfg.Diff.MaxDiffBytes)
	// Post-shaping emptiness check for the non-range modes (staged, pr/N):
	// their whole point is the diff, so an all-excluded diff is a clear user
	// error, not a silent empty review. Dispatch on the explicit Mode, never
	// on the label — a range over a branch literally named pr/5 must keep
	// range semantics.
	if strings.TrimSpace(diff) == "" {
		switch src.Mode {
		case modeStaged:
			return fmt.Errorf("no staged changes to review: all staged files are excluded by exclude_globs")
		case modePR:
			return fmt.Errorf("no reviewable changes in %s: the PR diff is empty or all files are excluded by exclude_globs", src.Label)
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
		Commits: commits,
		Diff:    diff,
		Staged:  src.Staged,
	}, systemTemplateFile)
	if err != nil {
		return fmt.Errorf("prompt template: %w", err)
	}

	// LLM response cache (Item 5): serve an equivalent prior review without a
	// network call. Only active for network reviews — offline mode is already
	// deterministic and free, so it is never cached.
	// The key hashes provider+model+system+user, and the user message embeds
	// the full diff — so staged mode is content-addressed by the staged diff
	// itself (no range exists to key on): re-running with the same index hits
	// the cache, any `git add` changes the diff and therefore the key.
	noCache, _ := cmd.Flags().GetBool("no-cache")
	useCache := cfg.Cache.Enabled && !noCache && !cfg.OfflineMode() && cfg.Cache.TTLHours > 0
	var (
		cache    *llmcache.Cache
		cacheKey string
	)
	if useCache {
		if c, err := llmcache.New(time.Duration(cfg.Cache.TTLHours) * time.Hour); err == nil {
			cache = c
			cacheKey = llmcache.Key(cfg.LLM.Provider, cfg.LLM.Model, system, user)
			if resp, ok, _ := cache.Get(cacheKey); ok {
				slog.Debug("llm cache hit", "key", cacheKey[:12])
				art := buildArtifact(cfg, src.Label, commits, diff, resp)
				if err := render.RenderWithTemplate(cmd.OutOrStdout(), art, render.Format(cfg.Output.Format), cfg.Output.TemplateFile); err != nil {
					return err
				}
				threshold := cfg.Policy.FailOn
				if threshold != "" && threshold != "never" && llm.RiskAtLeast(resp.Risk.Level, threshold) {
					return &failError{level: resp.Risk.Level, threshold: threshold}
				}
				return nil
			}
		} else {
			slog.Debug("llm cache unavailable", "err", err)
		}
	}

	// --dry-run: print the estimate, no network call, exit 0.
	if dryRun {
		return printDryRun(cmd, cfg, system+"\n"+user)
	}

	// Cost guard runs automatically before calling the provider (§8.4), skipped
	// in offline mode (no call, no cost).
	if !cfg.OfflineMode() {
		if err := costGuard(cmd, cfg, system+"\n"+user); err != nil {
			return err
		}
	}

	provider, err := selectProvider(cmd, cfg, commits, diff)
	if err != nil {
		return err
	}

	// Streaming branch (Item 1): stream tokens to a terminal for md/text output.
	// The body is written directly by Stream; only the risk header is appended
	// afterward, so the full Artifact renderer is NOT invoked here.
	// If Stream fails before writing any bytes (e.g. 429/503 before the first
	// token), we fall through to the buffered Complete path below (§7.2/§8).
	if s, ok := provider.(llm.Streamer); ok && wantStream(cmd, cfg) {
		out := cmd.OutOrStdout()
		cw := &byteCountWriter{w: out}
		resp, streamErr := s.Stream(ctx, llm.Request{
			System:      system,
			User:        user,
			Model:       cfg.LLM.Model,
			MaxTokens:   cfg.LLM.MaxTokens,
			Temperature: cfg.LLM.Temperature,
			Commits:     commits,
			Diff:        diff,
		}, cw)
		if streamErr == nil {
			// Risk header printed after [DONE] — body already written by Stream.
			fmt.Fprintf(out, "\n---\n%s\n", render.RiskHeaderLine(resp.Risk.Level, resp.Risk.Summary, resp.Risk.Heuristic))
			if cache != nil && cacheKey != "" {
				if err := cache.Put(cacheKey, resp); err != nil {
					slog.Debug("llm cache put failed", "err", err)
				}
			}
			threshold := cfg.Policy.FailOn
			if threshold != "" && threshold != "never" && llm.RiskAtLeast(resp.Risk.Level, threshold) {
				return &failError{level: resp.Risk.Level, threshold: threshold}
			}
			return nil
		}
		if cw.written > 0 {
			// Tokens already reached the terminal — partial output can't be undone.
			return fmt.Errorf("review stream failed: %w", streamErr)
		}
		// Zero bytes written: safe to fall back to the buffered path.
		slog.Info("streaming failed before first token, falling back to non-streaming", "err", streamErr)
	}

	slog.Debug("requesting review", "commits", len(commits), "diff_bytes", len(diff), "offline", cfg.OfflineMode())
	resp, err := provider.Complete(ctx, llm.Request{
		System:      system,
		User:        user,
		Model:       cfg.LLM.Model,
		MaxTokens:   cfg.LLM.MaxTokens,
		Temperature: cfg.LLM.Temperature,
		Commits:     commits,
		Diff:        diff,
	})
	if err != nil {
		return fmt.Errorf("review failed: %w", err)
	}

	if cache != nil && cacheKey != "" {
		if err := cache.Put(cacheKey, resp); err != nil {
			slog.Debug("llm cache put failed", "err", err)
		}
	}

	art := buildArtifact(cfg, src.Label, commits, diff, resp)
	if err := render.RenderWithTemplate(cmd.OutOrStdout(), art, render.Format(cfg.Output.Format), cfg.Output.TemplateFile); err != nil {
		return err
	}

	// Gate LAST, only after the review has been printed (§9).
	threshold := cfg.Policy.FailOn
	if threshold != "" && threshold != "never" && llm.RiskAtLeast(resp.Risk.Level, threshold) {
		return &failError{level: resp.Risk.Level, threshold: threshold}
	}
	return nil
}

// buildArtifact assembles the render artifact from the review inputs and the
// provider response. With no commit metadata (a staged review has none), the
// changed-file count comes from the diff headers instead.
func buildArtifact(cfg *config.Config, revRange string, commits []gitlog.Commit, diff string, resp llm.Response) render.Artifact {
	added, removed := gitlog.DiffLineStats(diff)
	filesChanged := gitlog.ChangedFileCount(commits)
	if len(commits) == 0 {
		filesChanged = gitlog.DiffFileCount(diff)
	}
	rc := make([]render.Commit, 0, len(commits))
	for _, c := range commits {
		rc = append(rc, render.Commit{
			Hash:    c.Hash,
			Author:  c.Author,
			Date:    c.Date,
			Subject: c.Subject,
		})
	}
	return render.Artifact{
		GeneratedAt:   time.Now().UTC(),
		Range:         revRange,
		Offline:       cfg.OfflineMode(),
		Provider:      cfg.LLM.Provider,
		Model:         cfg.LLM.Model,
		RiskLevel:     resp.Risk.Level,
		RiskSummary:   resp.Risk.Summary,
		RiskHeuristic: resp.Risk.Heuristic,
		Stats: render.Stats{
			Commits:      len(commits),
			FilesChanged: filesChanged,
			LinesAdded:   added,
			LinesRemoved: removed,
		},
		Commits:        rc,
		ReviewMarkdown: resp.Content,
	}
}

// selectProvider returns the network client when an API key is configured, or
// the deterministic offline provider otherwise (printing a warning to stderr,
// not failing).
func selectProvider(cmd *cobra.Command, cfg *config.Config, commits []gitlog.Commit, diff string) (llm.Provider, error) {
	if cfg.OfflineMode() {
		fmt.Fprintln(cmd.ErrOrStderr(), "gitl: no LLM API key configured — using deterministic offline review (set GITL_API_KEY for an AI review).")
		return llm.NewOffline(commits, diff), nil
	}
	return newNetworkClient(cfg)
}

// newNetworkClient builds the concrete network *llm.Client from config. Shared
// by selectProvider (review) and the changelog --ai path, which needs the
// concrete type for CompleteRaw and handles offline mode itself, before any
// provider selection.
func newNetworkClient(cfg *config.Config) (*llm.Client, error) {
	return llm.NewClient(llm.ClientConfig{
		Provider:   cfg.LLM.Provider,
		BaseURL:    cfg.LLM.BaseURL,
		APIKey:     cfg.LLM.APIKey,
		Timeout:    cfg.LLM.Timeout(),
		MaxRetries: cfg.LLM.MaxRetries,
		Azure: llm.AzureConfig{
			Endpoint:   cfg.LLM.AzureOpenAI.Endpoint,
			Deployment: cfg.LLM.AzureOpenAI.Deployment,
			APIVersion: cfg.LLM.AzureOpenAI.APIVersion,
		},
	})
}

// mergedExcludeGlobs combines the personal diff.exclude_globs with the repo
// policy.exclude_globs (the policy list ADDS, it does not replace — §5).
func mergedExcludeGlobs(cfg *config.Config) []string {
	globs := make([]string, 0, len(cfg.Diff.ExcludeGlobs)+len(cfg.Policy.ExcludeGlobs))
	globs = append(globs, cfg.Diff.ExcludeGlobs...)
	globs = append(globs, cfg.Policy.ExcludeGlobs...)
	return globs
}

// matchesAnyGlob reports whether p matches any of the globs. It tries the full
// path and, for "**"-style patterns, a basename match, so patterns like
// "vendor/**" and "*.lock" both work against changed-file paths.
func matchesAnyGlob(p string, globs []string) bool {
	base := path.Base(p)
	for _, g := range globs {
		if g == "" {
			continue
		}
		if ok, _ := path.Match(g, p); ok {
			return true
		}
		// "*.lock" should match "dir/foo.lock" via the basename.
		if ok, _ := path.Match(g, base); ok {
			return true
		}
		// "vendor/**" → treat as a prefix match on the directory.
		if strings.HasSuffix(g, "/**") {
			prefix := strings.TrimSuffix(g, "**")
			if strings.HasPrefix(p, prefix) {
				return true
			}
		}
	}
	return false
}

// filterDiffByGlobs drops whole per-file sections of a unified diff whose path
// matches an exclude glob. It splits on "diff --git " headers; anything before
// the first header (rare) is preserved.
func filterDiffByGlobs(diff string, globs []string) string {
	if len(globs) == 0 || strings.TrimSpace(diff) == "" {
		return diff
	}
	const sep = "diff --git "
	// Keep any preamble before the first section.
	idx := strings.Index(diff, sep)
	if idx == -1 {
		return diff
	}

	var b strings.Builder
	b.WriteString(diff[:idx])

	rest := diff[idx:]
	sections := strings.Split(rest, sep)
	for _, sec := range sections {
		if sec == "" {
			continue
		}
		p := parseDiffGitPath(sec)
		if p != "" && matchesAnyGlob(p, globs) {
			slog.Debug("excluding file from diff", "path", p)
			continue
		}
		b.WriteString(sep)
		b.WriteString(sec)
	}
	return b.String()
}

// parseDiffGitPath extracts the b-side path from a "diff --git a/x b/y" header
// section (the leading "diff --git " prefix already stripped).
func parseDiffGitPath(section string) string {
	nl := strings.IndexByte(section, '\n')
	header := section
	if nl != -1 {
		header = section[:nl]
	}
	// header is "a/OLDPATH b/NEWPATH"; both sides can contain spaces.
	// Find the last " b/" to correctly split the b-side even for paths with spaces.
	idx := strings.LastIndex(header, " b/")
	if idx < 0 {
		return ""
	}
	return header[idx+3:]
}

// truncateDiff caps the diff at maxBytes with an explicit marker (§6). A
// non-positive maxBytes disables truncation.
func truncateDiff(diff string, maxBytes int) string {
	if maxBytes <= 0 || len(diff) <= maxBytes {
		return diff
	}
	slog.Warn("diff exceeds max_diff_bytes; truncating", "bytes", len(diff), "limit", maxBytes)
	// Align the cut to a valid UTF-8 rune boundary so the result is never a
	// malformed string (multi-byte runes must not be split mid-sequence).
	for maxBytes > 0 && !utf8.RuneStart(diff[maxBytes]) {
		maxBytes--
	}
	return diff[:maxBytes] + "\n[... diff truncated ...]\n"
}
