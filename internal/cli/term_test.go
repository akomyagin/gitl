package cli

import (
	"bytes"
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
