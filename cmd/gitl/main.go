// Command gitl is the CLI entrypoint for git-log-lens: an AI reviewer of git
// history (risk-scored review, changelog, multi-repo digest).
//
// This is intentionally a thin main for the Этап 0 bootstrap: it wires up the
// (currently minimal) CLI and delegates to internal/cli. The real command tree
// (cobra + subcommands review/changelog/digest) lands in Этап 1+.
package main

import (
	"os"

	"github.com/akomyagin/gitl/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
