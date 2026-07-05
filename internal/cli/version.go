package cli

import (
	"github.com/spf13/cobra"
)

// Build-provenance values, injected via -ldflags at release time (Этап 5).
// Defaults are used for local/dev builds.
var (
	version = "0.0.0-dev"
	commit  = "none"
	date    = "unknown"
)

// newVersionCmd builds the `gitl version` command.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version and build provenance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Printf("gitl %s (commit %s, built %s)\n", version, commit, date)
			return nil
		},
	}
}
