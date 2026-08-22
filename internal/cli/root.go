// Package cli wires up the gitl command tree (cobra) and shared scaffolding:
// persistent flags, viper-backed config loading, and slog setup.
//
// One file per command: root.go (this scaffold), version.go, init.go,
// review.go, changelog.go, digest.go (see docs/TECHNICAL_PLAN.md §6, §9, §10).
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
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
	root.AddCommand(newInitCmd(gf))
	root.AddCommand(newReviewCmd(gf))
	root.AddCommand(newChangelogCmd(gf))
	root.AddCommand(newDigestCmd(gf))
	root.AddCommand(newMCPCmd(gf))

	return root
}

// Exit codes for the gitl process (ROADMAP F3):
//
//	0 ok, 1 tool/runtime error, 2 --fail-on gate triggered.
const (
	ExitOK      = 0
	ExitToolErr = 1
	ExitGate    = 2
)

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

// ExitCode maps a top-level command error to the process exit code. A
// *failError (the --fail-on risk gate, review.go) means the tool ran correctly
// but the review's own risk verdict tripped the gate — a distinct signal from a
// genuine tool/runtime failure, so CI can tell "risky change" from "gitl broke".
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var fe *failError
	if errors.As(err, &fe) {
		return ExitGate
	}
	return ExitToolErr
}

// ExecuteWithExitCode runs the command tree and returns the process exit code
// (0/1/2) instead of a bare error, so main stays a one-liner. stderr printing
// still happens inside Execute.
func ExecuteWithExitCode(ctx context.Context, args []string) int {
	return ExitCode(Execute(ctx, args))
}

// setupLogging configures the default slog logger. --verbose raises the level
// to debug with full structured output; otherwise warnings and above go to
// stderr as human-voiced `gitl: ...` lines (ROADMAP U6).
func setupLogging(verbose bool) {
	var handler slog.Handler
	if verbose {
		// Explicit opt-in to detail: full structured logs (time/level/attrs),
		// also the right shape for CI grep. Debug and above.
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	} else {
		// Default: one human `gitl: ...` voice, Warn and above only
		// (humanHandler.Enabled gates the level).
		handler = newHumanHandler(os.Stderr)
	}
	slog.SetDefault(slog.New(handler))
}

// loadConfig loads the merged config for a command, honoring the --config
// override and binding the command's flags for flag-level priority. The git
// repository root is discovered best-effort so a committed root-level
// .gitl.yaml (team policy, exclude_globs) applies even when gitl runs from a
// subdirectory; the cwd .gitl.yaml still wins key-by-key.
func loadConfig(cmd *cobra.Command, gf *globalFlags) (*config.Config, error) {
	return config.Load(config.Options{
		PersonalPath: gf.configPath,
		RepoRootDir:  discoverRepoRoot(cmd.Context()),
		Flags:        cmd.Flags(),
	})
}

// discoverRepoRoot best-effort resolves the git working-tree root for config
// discovery. Any failure (git not in PATH, cwd not inside a git repo, ...)
// returns "" so config loading falls back to the cwd-only behavior — root
// discovery must never fail a command.
func discoverRepoRoot(ctx context.Context) string {
	runner, err := gitlog.NewRunner("")
	if err != nil {
		return ""
	}
	root, err := runner.TopLevel(ctx)
	if err != nil {
		return ""
	}
	return root
}
