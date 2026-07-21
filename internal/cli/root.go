// Package cli wires up the gitl command tree (cobra) and shared scaffolding:
// persistent flags, viper-backed config loading, and slog setup.
//
// One file per command: root.go (this scaffold), version.go, review.go,
// changelog.go, digest.go (see docs/TECHNICAL_PLAN.md §6, §9, §10).
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/config"
)

// globalFlags holds values for root-level persistent flags shared by all
// subcommands. It is populated by cobra flag parsing before RunE fires.
type globalFlags struct {
	verbose    bool
	configPath string // override for the personal config file path
}

// newRootCmd builds the root command and attaches subcommands. gf is created
// here and captured by the subcommand closures, so callers only need the
// resulting *cobra.Command.
func newRootCmd() *cobra.Command {
	gf := &globalFlags{}

	root := &cobra.Command{
		Use:           "gitl",
		Short:         "AI reviewer of git history (git-log-lens)",
		Long:          "gitl AI-reviews a git commit range (`gitl review <range>`) with an LLM, producing a structured risk score (low|medium|high) and md/text/json output.\n\nWithout an API key it falls back to a deterministic offline review.\n\ngitl changelog groups a range into Keep a Changelog categories — fully deterministic\nby default; --ai optionally rewrites it with the model. gitl digest aggregates\nactivity over a day window (optionally across multiple repos) and never calls an LLM.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			setupLogging(gf.verbose)
		},
	}

	root.PersistentFlags().BoolVarP(&gf.verbose, "verbose", "v", false, "enable debug logging")
	root.PersistentFlags().StringVar(&gf.configPath, "config", "", "path to personal config file (overrides ~/.config/gitl/config.yaml)")

	root.AddCommand(newVersionCmd())
	root.AddCommand(newReviewCmd(gf))
	root.AddCommand(newChangelogCmd(gf))
	root.AddCommand(newDigestCmd(gf))
	root.AddCommand(newMCPCmd(gf))

	return root
}

// Execute runs the gitl command tree with the given context and args. It
// returns a non-nil error to signal a non-zero exit code; the error is printed
// to stderr here so main stays thin.
func Execute(ctx context.Context, args []string) error {
	root := newRootCmd()
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "gitl:", err)
		return err
	}
	return nil
}

// setupLogging configures the default slog logger. --verbose raises the level
// to debug; otherwise warnings and above go to stderr.
func setupLogging(verbose bool) {
	level := slog.LevelWarn
	if verbose {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// loadConfig loads the merged config for a command, honoring the --config
// override and binding the command's flags for flag-level priority.
func loadConfig(cmd *cobra.Command, gf *globalFlags) (*config.Config, error) {
	return config.Load(config.Options{
		PersonalPath: gf.configPath,
		Flags:        cmd.Flags(),
	})
}
