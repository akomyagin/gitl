package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
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
		Use:   "review <range>",
		Short: "AI review of a commit range (e.g. HEAD~5..HEAD)",
		Long: "review runs `git log` + `git diff` over the given revision range, sends the\n" +
			"result to an LLM, and prints a review (md/text/json) to stdout with a\n" +
			"structured risk score.\n\n" +
			"Without an API key (GITL_API_KEY or llm.api_key) it falls back to a\n" +
			"deterministic offline review and prints a warning to stderr.\n\n" +
			"--dry-run prints a cost estimate and exits without calling the API.\n" +
			"--fail-on gates CI: exit non-zero when the risk level meets the threshold.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReview(cmd.Context(), cmd, gf, args[0])
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

	return cmd
}

// runReview executes the full review pipeline for one revision range.
func runReview(ctx context.Context, cmd *cobra.Command, gf *globalFlags, revRange string) error {
	cfg, err := loadConfig(cmd, gf)
	if err != nil {
		return err
	}
	dryRun, err := cmd.Flags().GetBool("dry-run")
	if err != nil {
		return err
	}

	runner, err := gitlog.NewRunner("")
	if err != nil {
		return err
	}

	slog.Debug("collecting git history", "range", revRange)
	commits, err := runner.Log(ctx, revRange)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("no commits found in range %q", revRange)
	}
	rawDiff, err := runner.Diff(ctx, revRange)
	if err != nil {
		return err
	}

	// Config-driven diff shaping: drop excluded files, then truncate to
	// max_diff_bytes with an explicit marker (§6).
	excludeGlobs := mergedExcludeGlobs(cfg)
	diff := filterDiffByGlobs(rawDiff, excludeGlobs)
	diff = truncateDiff(diff, cfg.Diff.MaxDiffBytes)

	// Custom prompt templates apply only when calling a real provider; the
	// offline provider ignores the system prompt, so pass an empty path in
	// offline mode to keep behavior deterministic.
	systemTemplateFile := cfg.Prompt.SystemTemplateFile
	if cfg.OfflineMode() {
		systemTemplateFile = ""
	}
	system, user, err := prompt.BuildReviewWithTemplate(prompt.Review{
		Range:   revRange,
		Commits: commits,
		Diff:    diff,
	}, systemTemplateFile)
	if err != nil {
		return fmt.Errorf("prompt template: %w", err)
	}

	// LLM response cache (Item 5): serve an equivalent prior review without a
	// network call. Only active for network reviews — offline mode is already
	// deterministic and free, so it is never cached.
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
				art := buildArtifact(cfg, revRange, commits, diff, resp)
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

	art := buildArtifact(cfg, revRange, commits, diff, resp)
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
// provider response.
func buildArtifact(cfg *config.Config, revRange string, commits []gitlog.Commit, diff string, resp llm.Response) render.Artifact {
	added, removed := gitlog.DiffLineStats(diff)
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
			FilesChanged: gitlog.ChangedFileCount(commits),
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
