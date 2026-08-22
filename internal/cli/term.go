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

// isTerminalFn is the TTY probe consulted by wantColor. A package var so tests
// can stub the TTY-true branch deterministically without allocating a real PTY
// (the same isolation idea as gitlog.Runner wrapping os/exec).
var isTerminalFn = isTerminal

// wantColor reports whether ANSI color should be emitted for human-readable
// review output written to w. Precedence (highest first, first match wins):
//
//  1. NO_COLOR env var present (any value, even empty) → false (https://no-color.org)
//  2. cfg.Output.Color == false                        → false
//  3. w is not a TTY                                   → false
//  4. otherwise                                        → true
//
// Only consulted on the md/text/streaming header paths; JSON output never
// carries color (renderJSON takes no color parameter by construction).
func wantColor(w io.Writer, cfg *config.Config) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if !cfg.Output.Color {
		return false
	}
	return isTerminalFn(w)
}

// wantQuiet reports whether informational (non-error) stderr banners should be
// suppressed (U9): the offline-review notice and the changelog --ai fallback
// notice. Precedence mirrors wantColor — each layer can independently turn
// suppression on, first match wins:
//
//  1. GITL_QUIET env var present (any value, even empty) → quiet
//  2. --quiet flag set                                   → quiet
//  3. output.quiet: true in config                       → quiet
//  4. otherwise                                          → not quiet
//
// Never suppresses errors, the rendered review/risk output, or the --fail-on
// gate message — those are not informational banners. The MCP path has no
// *cobra.Command and derives quiet from cfg.Output.Quiet alone (RunReviewCore).
func wantQuiet(cmd *cobra.Command, cfg *config.Config) bool {
	if _, ok := os.LookupEnv("GITL_QUIET"); ok {
		return true
	}
	// Commands lacking the flag just yield (false, err) — ignore the error,
	// matching the existing `noCache, _ := cmd.Flags().GetBool(...)` idiom.
	if q, _ := cmd.Flags().GetBool("quiet"); q {
		return true
	}
	return cfg.Output.Quiet
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
