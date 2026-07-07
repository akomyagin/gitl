package cli

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Build-provenance values, injected via -ldflags at release time (Этап 5).
// Defaults are used for local/dev builds; init() fills them from build info
// when the binary is installed via `go install` without explicit ldflags.
var (
	version = "0.0.0-dev"
	commit  = "none"
	date    = "unknown"
)

func init() {
	if version != "0.0.0-dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return
	}
	version = info.Main.Version
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 12 {
				commit = s.Value[:12]
			} else {
				commit = s.Value
			}
		case "vcs.time":
			date = s.Value
		}
	}
}

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
