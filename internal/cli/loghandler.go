package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// humanHandler renders slog records as single human-facing `gitl: <msg>` lines
// for the default (non-verbose) CLI experience (ROADMAP U6). It emits at
// LevelWarn and above only; Debug/Info are dropped (they are only meaningful
// under --verbose, which uses slog's standard TextHandler instead). Attributes
// are appended as a parenthesized `key=value` aside so actionable specifics
// (provider, path, limits) survive without the `time=`/`level=` structured
// noise.
type humanHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	attrs []slog.Attr // accumulated via WithAttrs
	// group prefix intentionally unsupported: gitl never uses slog groups.
}

func newHumanHandler(w io.Writer) *humanHandler {
	return &humanHandler{mu: &sync.Mutex{}, w: w}
}

func (h *humanHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *humanHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString("gitl: ")
	b.WriteString(r.Message)

	// Collect WithAttrs-accumulated attrs plus record-level ones.
	parts := make([]string, 0, r.NumAttrs()+len(h.attrs))
	for _, a := range h.attrs {
		parts = append(parts, fmt.Sprintf("%s=%v", a.Key, a.Value.Any()))
	}
	r.Attrs(func(a slog.Attr) bool {
		parts = append(parts, fmt.Sprintf("%s=%v", a.Key, a.Value.Any()))
		return true
	})
	if len(parts) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString(")")
	}
	b.WriteString("\n")

	// The shared *sync.Mutex (copied by pointer in WithAttrs) serializes
	// writes so concurrent slog.Warn calls (digest worker-pool, mcp server
	// goroutines) don't interleave a line — matching slog.TextHandler's own
	// locking.
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *humanHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nc := *h
	nc.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &nc
}

// WithGroup: gitl never groups; return self so grouping is a no-op.
func (h *humanHandler) WithGroup(_ string) slog.Handler { return h }
