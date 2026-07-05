// Package render turns a computed review artifact into output.
//
// Этап 1 only renders Markdown (the LLM already returns Markdown, so this is
// essentially a passthrough that normalizes trailing whitespace). Multi-format
// output (text/json) and the versioned JSON contract (schema_version) are
// explicitly Этап 2 scope (see docs/TECHNICAL_PLAN.md §6); until then the "text"
// and "json" formats return a clear "not yet implemented" error rather than
// silently producing wrong output.
package render

import (
	"fmt"
	"io"
	"strings"
)

// Format is an output format. Only FormatMarkdown is implemented in Этап 1.
type Format string

const (
	FormatMarkdown Format = "md"
	FormatText     Format = "text"
	FormatJSON     Format = "json"
)

// Render writes the review content in the requested format to w. In Этап 1 only
// "md" is supported; "text"/"json" return a not-implemented error.
func Render(w io.Writer, content string, format Format) error {
	switch format {
	case FormatMarkdown, "":
		body := strings.TrimRight(content, "\n") + "\n"
		if _, err := io.WriteString(w, body); err != nil {
			return fmt.Errorf("render markdown: %w", err)
		}
		return nil
	case FormatText, FormatJSON:
		return fmt.Errorf("render: output format %q is not yet implemented (lands in Этап 2)", format)
	default:
		return fmt.Errorf("render: unknown output format %q (supported: md)", format)
	}
}
