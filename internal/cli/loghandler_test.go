package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// assertNoStructuredNoise fails if the human-voice output leaks the
// TextHandler's `time=`/`level=` structured prefixes (ROADMAP U6).
func assertNoStructuredNoise(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "time=") {
		t.Errorf("output contains structured %q noise: %q", "time=", out)
	}
	if strings.Contains(out, "level=") {
		t.Errorf("output contains structured %q noise: %q", "level=", out)
	}
}

func TestHumanHandlerNoAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newHumanHandler(&buf))

	logger.Warn("overwriting existing config")

	got := buf.String()
	want := "gitl: overwriting existing config\n"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	assertNoStructuredNoise(t, got)
}

func TestHumanHandlerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newHumanHandler(&buf))

	logger.Warn("diff exceeds max_diff_bytes; truncating", "bytes", 200000, "limit", 131072)

	got := buf.String()
	want := "gitl: diff exceeds max_diff_bytes; truncating (bytes=200000, limit=131072)\n"
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	assertNoStructuredNoise(t, got)
}

func TestHumanHandlerSuppressesDebugInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newHumanHandler(&buf))

	logger.Info("x")
	logger.Debug("y")

	if got := buf.String(); got != "" {
		t.Errorf("Info/Debug should be suppressed at default level, got %q", got)
	}
}

func TestHumanHandlerWithAttrsAccumulation(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newHumanHandler(&buf))

	logger.With("provider", "anthropic").Warn("provider does not support streaming; buffering full response")

	got := buf.String()
	if !strings.Contains(got, "(provider=anthropic)") {
		t.Errorf("output = %q, want it to contain %q", got, "(provider=anthropic)")
	}
}

// TestSetupLoggingDefaultHumanVoice locks in that default verbosity produces
// the single human `gitl: ...` voice on stderr. Not parallel: it mutates the
// process-global default logger and os.Stderr.
func TestSetupLoggingDefaultHumanVoice(t *testing.T) {
	origLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	out := captureStderr(t, func() {
		// setupLogging must run after os.Stderr is redirected so the handler
		// binds to the pipe.
		setupLogging(false)
		slog.Warn("test warn", "k", "v")
	})

	want := "gitl: test warn (k=v)\n"
	if out != want {
		t.Errorf("stderr = %q, want %q", out, want)
	}
	assertNoStructuredNoise(t, out)
}

// TestSetupLoggingVerboseStructured locks in that --verbose keeps the full
// machine-parseable TextHandler output. Not parallel: mutates global state.
func TestSetupLoggingVerboseStructured(t *testing.T) {
	origLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	out := captureStderr(t, func() {
		setupLogging(true)
		slog.Warn("test warn", "k", "v")
	})

	for _, want := range []string{"level=WARN", `msg="test warn"`, "k=v"} {
		if !strings.Contains(out, want) {
			t.Errorf("verbose stderr = %q, want it to contain %q", out, want)
		}
	}
}
