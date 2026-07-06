package cli

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/render"
)

// newChangelogCmd builds the `gitl changelog [<range>]` command (§9.1).
func newChangelogCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "changelog [<range>]",
		Short: "Keep a Changelog-style summary of a commit range, grouped by conventional-commit type",
		Long: "changelog runs `git log` over the given revision range and groups commits into\n" +
			"Keep a Changelog categories (Added/Changed/Deprecated/Removed/Fixed/Security/Other)\n" +
			"based on their conventional-commit prefix (feat:, fix:, ...).\n\n" +
			"<range> is optional: without it, changelog uses <latest-tag>..HEAD, or the full\n" +
			"history if the repository has no tags yet.\n\n" +
			"changelog never calls an LLM — the categorization is fully deterministic from\n" +
			"commit metadata, online or offline, with or without an API key.\n\n" +
			"policy.required_changelog_categories (if set) is checked after categorization:\n" +
			"an empty required category prints a warning to stderr but never fails the command.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChangelog(cmd.Context(), cmd, gf, args)
		},
	}

	cmd.Flags().String("format", "", "output format (md | text | json)")

	return cmd
}

// runChangelog executes the changelog pipeline for one revision range.
func runChangelog(ctx context.Context, cmd *cobra.Command, gf *globalFlags, args []string) error {
	cfg, err := loadConfig(cmd, gf)
	if err != nil {
		return err
	}

	runner, err := gitlog.NewRunner("")
	if err != nil {
		return err
	}

	revRange, err := resolveChangelogRange(ctx, runner, args)
	if err != nil {
		return err
	}

	slog.Debug("collecting git history for changelog", "range", revRange)
	commits, err := runner.Log(ctx, revRange)
	if err != nil {
		return err
	}

	cl := gitlog.CategorizeCommits(commits)
	missing := gitlog.MissingRequiredCategories(cl, cfg.Policy.RequiredChangelogCategories)
	for _, name := range missing {
		slog.Warn(fmt.Sprintf("required changelog category %q has no entries in range %q", name, revRange))
	}

	art := render.NewChangelogArtifact(time.Now().UTC(), revRange, cl, missing)
	return render.RenderChangelog(cmd.OutOrStdout(), art, render.Format(cfg.Output.Format))
}

// resolveChangelogRange returns the explicit range argument if given, or
// falls back to <latest-tag>..HEAD, or plain "HEAD" if the repo has no tags
// at all (§9.1). A failure to describe a tag is not fatal.
func resolveChangelogRange(ctx context.Context, runner *gitlog.Runner, args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}

	tag, err := runner.LatestTag(ctx)
	if err != nil {
		return "", err
	}
	if tag == "" {
		slog.Debug("no tags found; defaulting changelog range to full history")
		return "HEAD", nil
	}
	return tag + "..HEAD", nil
}
