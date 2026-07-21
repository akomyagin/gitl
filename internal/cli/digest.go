package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/render"
	"github.com/akomyagin/gitl/internal/tui"
)

// defaultDigestDays is the default --days window (§10.1).
const defaultDigestDays = 7

// newDigestCmd builds the `gitl digest` command (§10.1/§10.4).
func newDigestCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "digest",
		Short: "Deterministic activity summary over the last N days, optionally across multiple repos",
		Long: "digest aggregates git history from the last --days days by author, by\n" +
			"conventional-commit topic, and by most-changed files. It never calls an LLM —\n" +
			"the aggregation is fully deterministic from commit metadata.\n\n" +
			"--repos=a,b,c runs the digest concurrently over multiple repositories (a bounded\n" +
			"worker pool) and adds a combined overall summary. A repository that is missing,\n" +
			"not a git repository, or otherwise fails is reported as a per-repo error and does\n" +
			"not abort the others. --repos replaces digest.repos from .gitl.yaml entirely (it\n" +
			"does not merge with it). Without --repos or digest.repos, digest runs against the\n" +
			"current directory as a single repo.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDigest(cmd.Context(), cmd, gf)
		},
	}

	cmd.Flags().Int("days", defaultDigestDays, "size of the activity window in days (must be > 0)")
	cmd.Flags().String("repos", "", "comma-separated list of repository paths (overrides digest.repos entirely)")
	cmd.Flags().String("format", "", "output format (md | text | json)")
	cmd.Flags().Bool("tui", false, "interactive TUI viewer (requires a terminal)")

	return cmd
}

// runDigest is the cobra adapter around the cmd-free digest core: parse the
// flags into DigestOptions, call RunDigestCore, and render the resulting
// artifact (plain writer or the --tui viewer) — see digest_core.go.
func runDigest(ctx context.Context, cmd *cobra.Command, gf *globalFlags) error {
	cfg, err := loadConfig(cmd, gf)
	if err != nil {
		return err
	}

	days, err := cmd.Flags().GetInt("days")
	if err != nil {
		return err
	}

	// The raw comma-split values go into DigestOptions.Repos as-is; the core
	// trims/normalizes them. An explicitly set --repos always yields a
	// non-empty slice here (even `--repos=` → [""]), which the core rejects
	// with the "--repos was set but contained no repository paths" error.
	var repos []string
	if cmd.Flags().Changed("repos") {
		reposFlag, err := cmd.Flags().GetString("repos")
		if err != nil {
			return err
		}
		repos = strings.Split(reposFlag, ",")
	}

	art, err := RunDigestCore(ctx, cfg, DigestOptions{Days: days, Repos: repos})
	if err != nil {
		return err
	}

	tuiFlag, _ := cmd.Flags().GetBool("tui")
	if tuiFlag {
		if !isTerminal(cmd.OutOrStdout()) {
			fmt.Fprintln(cmd.ErrOrStderr(), "gitl: --tui requires a terminal — falling back to plain output")
		} else {
			if cmd.Flags().Changed("format") {
				fmt.Fprintln(cmd.ErrOrStderr(), "gitl: --tui ignores --format (interactive view)")
			}
			return tui.Run(ctx, art)
		}
	}
	return render.RenderDigest(cmd.OutOrStdout(), art, render.Format(cfg.Output.Format))
}
