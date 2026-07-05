package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/prompt"
	"github.com/akomyagin/gitl/internal/render"
)

// hardDiffCap is a trivial safety cap so a runaway diff cannot blow up memory
// or the LLM request. The config-driven truncation strategy (max_diff_bytes,
// exclude_globs) is Этап 2 — this is intentionally not wired to config.
const hardDiffCap = 400_000

// newReviewCmd builds the `gitl review <range>` command.
func newReviewCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review <range>",
		Short: "AI review of a commit range (e.g. HEAD~5..HEAD)",
		Long: "review runs `git log` + `git diff` over the given revision range, sends the\n" +
			"result to an LLM, and prints a Markdown review to stdout.\n\n" +
			"Without an API key (GITL_API_KEY or llm.api_key) it falls back to a\n" +
			"deterministic offline review and prints a warning to stderr.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReview(cmd.Context(), cmd, gf, args[0])
		},
	}

	// Flags bound into config (see config.bindChangedFlags). Only override
	// config when explicitly set.
	cmd.Flags().String("provider", "", "LLM provider (openai)")
	cmd.Flags().String("model", "", "model name")
	cmd.Flags().String("base-url", "", "LLM API base URL")

	return cmd
}

// runReview executes the full review pipeline for one revision range.
func runReview(ctx context.Context, cmd *cobra.Command, gf *globalFlags, revRange string) error {
	cfg, err := loadConfig(cmd, gf)
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
	diff, err := runner.Diff(ctx, revRange)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("no commits found in range %q", revRange)
	}

	if len(diff) > hardDiffCap {
		slog.Warn("diff exceeds hard safety cap; truncating", "bytes", len(diff), "cap", hardDiffCap)
		diff = diff[:hardDiffCap] + "\n[... diff truncated at safety cap; config-driven truncation lands in Этап 2 ...]\n"
	}

	provider, err := selectProvider(cmd, cfg, commits, diff)
	if err != nil {
		return err
	}

	system, user := prompt.BuildReview(prompt.Review{
		Range:   revRange,
		Commits: commits,
		Diff:    diff,
	})

	slog.Debug("requesting review", "commits", len(commits), "diff_bytes", len(diff), "offline", cfg.OfflineMode())
	resp, err := provider.Complete(ctx, llm.Request{
		System:      system,
		User:        user,
		Model:       cfg.LLM.Model,
		MaxTokens:   cfg.LLM.MaxTokens,
		Temperature: cfg.LLM.Temperature,
	})
	if err != nil {
		return fmt.Errorf("review failed: %w", err)
	}

	return render.Render(cmd.OutOrStdout(), resp.Content, render.Format(cfg.Output.Format))
}

// selectProvider returns the network client when an API key is configured, or
// the deterministic offline provider otherwise (printing a warning to stderr,
// not failing). A misconfigured provider (anything but "openai") is a real
// error — "not supported until Этап 2" — never silently mishandled.
func selectProvider(cmd *cobra.Command, cfg *config.Config, commits []gitlog.Commit, diff string) (llm.Provider, error) {
	if cfg.OfflineMode() {
		fmt.Fprintln(cmd.ErrOrStderr(), "gitl: no LLM API key configured — using deterministic offline review (set GITL_API_KEY for an AI review).")
		return llm.NewOffline(commits, diff), nil
	}

	return llm.NewClient(llm.ClientConfig{
		Provider: cfg.LLM.Provider,
		BaseURL:  cfg.LLM.BaseURL,
		APIKey:   cfg.LLM.APIKey,
		Timeout:  cfg.LLM.Timeout(),
	})
}
