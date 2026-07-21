package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/akomyagin/gitl/internal/config"
)

// isTerminal reports whether w is a real TTY.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// wantStream reports whether the streaming path should be used for this review:
// terminal stdout, md/text format, not --no-stream, not --dry-run, not offline,
// no custom output.template_file.
func wantStream(cmd *cobra.Command, cfg *config.Config) bool {
	if !isTerminal(cmd.OutOrStdout()) {
		return false
	}
	if cfg.OfflineMode() {
		return false
	}
	format := cfg.Output.Format
	if format != "" && format != "md" && format != "text" {
		return false
	}
	noStream, _ := cmd.Flags().GetBool("no-stream")
	if noStream {
		return false
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		return false
	}
	if cfg.Output.TemplateFile != "" {
		// The streaming path writes the model's raw body directly and never
		// calls RenderWithTemplate (only the buffered path in runReview does)
		// — streaming a custom template would either require re-implementing
		// template application against a partial/live response or silently
		// ignoring the user's template, which is what happened before this
		// check existed. Buffering is the correct fallback, same as the other
		// streaming-disabling conditions above.
		return false
	}
	return cfg.Output.Stream
}
