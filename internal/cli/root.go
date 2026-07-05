// Package cli wires up the gitl command tree.
//
// Этап 0 (bootstrap): only a placeholder Execute that prints usage/version so
// that `go build ./...` and a basic `go run ./cmd/gitl` work. The real cobra
// command tree (root + review/changelog/digest/version, viper config wiring,
// global flags, slog) is implemented in Этап 1+ — see docs/TECHNICAL_PLAN.md §6.
package cli

import (
	"fmt"
	"os"
)

// Build-provenance values, injected via -ldflags at release time (Этап 5).
// Defaults are used for local/dev builds.
var (
	version = "0.0.0-dev"
	commit  = "none"
	date    = "unknown"
)

const usage = `gitl — AI reviewer of git history (git-log-lens)

Usage:
  gitl <command> [flags]

Commands (implemented from Этап 1+):
  review <range>     AI review of a commit range with machine-readable risk score
  changelog          Keep a Changelog-style changelog from history
  digest             activity digest (multi-repo capable)
  version            print version and build provenance

This is the Этап 0 bootstrap skeleton. See docs/PLAN.md for the roadmap.
`

// Execute is the entrypoint used by cmd/gitl. In Этап 0 it only handles
// `version`, `-h`/`--help`, and prints usage otherwise. It returns a non-nil
// error to signal a non-zero exit code.
func Execute(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "version":
			fmt.Printf("gitl %s (commit %s, built %s)\n", version, commit, date)
			return nil
		case "-h", "--help", "help":
			fmt.Print(usage)
			return nil
		}
	}
	fmt.Fprint(os.Stdout, usage)
	return nil
}
