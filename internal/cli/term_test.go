package cli

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/spf13/cobra"

	"github.com/akomyagin/gitl/internal/config"
)

// newStreamTestCmd builds a review command with the flags wantStream inspects,
// setting stdout to a non-TTY buffer (the test environment has no real TTY).
func newStreamTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := newReviewCmd(&globalFlags{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

func TestIsTerminalOnBuffer(t *testing.T) {
	t.Parallel()
	if isTerminal(&bytes.Buffer{}) {
		t.Error("a bytes.Buffer must not be reported as a terminal")
	}
	// An *os.File that is not a TTY (a regular temp file) is also not a terminal.
	f, err := os.CreateTemp(t.TempDir(), "term")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("a regular file must not be reported as a terminal")
	}
}

func TestWantStreamOfflineMode(t *testing.T) {
	t.Parallel()
	cmd := newStreamTestCmd(t)
	cfg := &config.Config{
		LLM:    config.LLMConfig{APIKey: ""}, // offline
		Output: config.OutputConfig{Format: "md", Stream: true},
	}
	if wantStream(cmd, cfg) {
		t.Error("wantStream must be false in offline mode")
	}
}

func TestWantStreamJSONFormat(t *testing.T) {
	t.Parallel()
	cmd := newStreamTestCmd(t)
	cfg := &config.Config{
		LLM:    config.LLMConfig{APIKey: "k"},
		Output: config.OutputConfig{Format: "json", Stream: true},
	}
	if wantStream(cmd, cfg) {
		t.Error("wantStream must be false for json format")
	}
}

func TestWantStreamNoStreamFlag(t *testing.T) {
	t.Parallel()
	cmd := newStreamTestCmd(t)
	if err := cmd.Flags().Set("no-stream", "true"); err != nil {
		t.Fatalf("set no-stream: %v", err)
	}
	cfg := &config.Config{
		LLM:    config.LLMConfig{APIKey: "k"},
		Output: config.OutputConfig{Format: "md", Stream: true},
	}
	if wantStream(cmd, cfg) {
		t.Error("wantStream must be false when --no-stream is set")
	}
}

// stubTerminal overrides the isTerminalFn seam for the duration of the test so
// the TTY-true branch of wantColor is exercisable without a real PTY. Not
// parallel-safe (package-level seam) — callers must not use t.Parallel().
func stubTerminal(t *testing.T, tty bool) {
	t.Helper()
	old := isTerminalFn
	isTerminalFn = func(io.Writer) bool { return tty }
	t.Cleanup(func() { isTerminalFn = old })
}

// TestWantColorPrecedence: NO_COLOR > output.color:false > non-TTY, evaluated
// top-down, first match wins.
func TestWantColorPrecedence(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	cases := []struct {
		name     string
		noColor  *string // nil → NO_COLOR unset
		cfgColor bool
		tty      bool
		want     bool
	}{
		{"NO_COLOR set beats TTY and color:true", strPtr("1"), true, true, false},
		{"NO_COLOR empty still disables (presence, not value)", strPtr(""), true, true, false},
		{"output.color:false beats TTY", nil, false, true, false},
		{"non-TTY beats output.color:true", nil, true, false, false},
		{"TTY + color:true + NO_COLOR unset → color", nil, true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// t.Setenv registers restore of the original value; Unsetenv after it
			// gives a guaranteed-unset state for the "nil" cases.
			t.Setenv("NO_COLOR", "sentinel")
			if tc.noColor != nil {
				t.Setenv("NO_COLOR", *tc.noColor)
			} else {
				if err := os.Unsetenv("NO_COLOR"); err != nil {
					t.Fatalf("unset NO_COLOR: %v", err)
				}
			}
			stubTerminal(t, tc.tty)
			cfg := &config.Config{Output: config.OutputConfig{Color: tc.cfgColor}}
			if got := wantColor(&bytes.Buffer{}, cfg); got != tc.want {
				t.Errorf("wantColor = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWantColorRealNonTTYWriter: without the seam, a bytes.Buffer is not a
// *os.File and therefore never a TTY — no color even with everything enabled.
func TestWantColorRealNonTTYWriter(t *testing.T) {
	t.Setenv("NO_COLOR", "x")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatalf("unset NO_COLOR: %v", err)
	}
	cfg := &config.Config{Output: config.OutputConfig{Color: true}}
	if wantColor(&bytes.Buffer{}, cfg) {
		t.Error("wantColor must be false for a non-TTY writer")
	}
}

func TestWantStreamNonTerminalWriter(t *testing.T) {
	t.Parallel()
	cmd := newStreamTestCmd(t) // stdout is a bytes.Buffer → not a TTY
	cfg := &config.Config{
		LLM:    config.LLMConfig{APIKey: "k"},
		Output: config.OutputConfig{Format: "md", Stream: true},
	}
	if wantStream(cmd, cfg) {
		t.Error("wantStream must be false when stdout is not a terminal")
	}
}
