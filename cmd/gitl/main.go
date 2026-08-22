// Command gitl is the CLI entrypoint for git-log-lens: an AI reviewer of git
// history (risk-scored review, changelog, multi-repo digest).
//
// main is intentionally thin: it builds a cancellable context wired to SIGINT/
// SIGTERM (so Ctrl-C propagates through git exec and the HTTP call, per
// docs/TECHNICAL_PLAN.md §3.1) and hands off to internal/cli. The process exit
// code follows the 0/1/2 contract: 0 ok, 1 tool/runtime error, 2 --fail-on
// risk gate triggered (see cli.ExitCode).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/akomyagin/gitl/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.ExecuteWithExitCode(ctx, os.Args[1:]))
}
