package cli

// End-to-end tests for the buffered-wait indicator hook (U2, review.go /
// waiting.go). The core invariant: stdout stays byte-pure — no spinner
// frames, no `\r`, no ANSI escapes — with the indicator wired in, for every
// buffered trigger (--no-stream, --format=json, non-streaming providers) and
// in offline mode. On non-TTY stderr (bytes.Buffer, as in CI) the indicator
// must be fully silent on stderr too.
//
// The PTY test at the bottom proves the positive case: with a real TTY on
// stderr the spinner actually animates during the buffered wait and erases
// itself before the review is rendered.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// noSpinnerBytes asserts that s carries no trace of the wait indicator:
// no carriage returns, no ESC (ANSI), no waiting message.
func noSpinnerBytes(t *testing.T, streamName, s string) {
	t.Helper()
	if strings.Contains(s, "\r") {
		t.Errorf("%s must not contain carriage returns, got %q", streamName, s)
	}
	if strings.Contains(s, "\033") {
		t.Errorf("%s must not contain ANSI escapes, got %q", streamName, s)
	}
	if strings.Contains(s, waitingForModelMsg) {
		t.Errorf("%s must not contain the waiting message, got %q", streamName, s)
	}
}

// delayHandler wraps h with a fixed pre-response sleep, long enough that a
// (wrongly) active spinner would have drawn frames during the wait.
func delayHandler(d time.Duration, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(d)
		h.ServeHTTP(w, r)
	})
}

// runReviewCaptureBoth is runReviewInDir plus separate stderr capture: both
// cobra streams go to bytes.Buffers (non-TTY — exactly the CI/pipe setup the
// indicator must stay silent in).
func runReviewCaptureBoth(t *testing.T, dir string, env map[string]string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	for k, v := range env {
		t.Setenv(k, v)
	}
	empty := filepath.Join(t.TempDir(), "none.yaml")

	root := newRootCmd()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"review", "--config", empty}, args...))
	runErr := root.ExecuteContext(context.Background())
	return outBuf.String(), errBuf.String(), runErr
}

// TestBufferedReviewStdoutPureNonTTY covers plan §5 tests 1 and 6: for every
// buffered trigger (--no-stream, --format=json, a non-streaming anthropic
// provider) the review succeeds through the buffered branch — where the
// indicator hook now lives — and neither stdout nor the command's stderr
// carries a single spinner byte, because both are non-TTY buffers.
func TestBufferedReviewStdoutPureNonTTY(t *testing.T) {
	openaiHandler := &oneShotJSONHandler{
		content: "buffered review body for the indicator test\n```risk\n{\"level\":\"low\",\"summary\":\"ok\"}\n```",
	}

	tests := []struct {
		name      string
		handler   http.Handler
		env       map[string]string
		extraArgs []string
		checkOut  func(t *testing.T, stdout string)
	}{
		{
			name:      "no-stream flag",
			handler:   openaiHandler,
			env:       map[string]string{"GITL_API_KEY": "sk-fake-e2e"},
			extraArgs: []string{"--no-stream"},
			checkOut: func(t *testing.T, stdout string) {
				if !strings.Contains(stdout, "buffered review body") {
					t.Errorf("stdout missing the review body:\n%s", stdout)
				}
			},
		},
		{
			name:      "format json",
			handler:   openaiHandler,
			env:       map[string]string{"GITL_API_KEY": "sk-fake-e2e"},
			extraArgs: []string{"--format=json"},
			checkOut: func(t *testing.T, stdout string) {
				if !json.Valid([]byte(stdout)) {
					t.Errorf("--format=json stdout is not valid JSON:\n%s", stdout)
				}
			},
		},
		{
			name:      "anthropic provider (no Streamer)",
			handler:   &anthropicOneShotHandler{},
			env:       map[string]string{"GITL_API_KEY": "sk-ant-fake-e2e"},
			extraArgs: []string{"--provider", "anthropic"},
			checkOut: func(t *testing.T, stdout string) {
				if !strings.Contains(stdout, anthropicReviewBody) {
					t.Errorf("stdout missing the anthropic review body:\n%s", stdout)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 50ms of server-side wait: enough for a wrongly-active spinner
			// to draw frames, short enough to keep the suite fast.
			srv := httptest.NewServer(delayHandler(50*time.Millisecond, tt.handler))
			defer srv.Close()

			dir := setupRepo(t, false)
			args := append([]string{"HEAD~1..HEAD", "--base-url", srv.URL, "--no-cache"}, tt.extraArgs...)
			stdout, stderr, err := runReviewCaptureBoth(t, dir, tt.env, args...)
			if err != nil {
				t.Fatalf("buffered review must succeed, got: %v", err)
			}
			tt.checkOut(t, stdout)
			noSpinnerBytes(t, "stdout", stdout)
			noSpinnerBytes(t, "stderr", stderr)
		})
	}
}

// TestOfflineReviewNoSpinner is plan §5 test 5: offline mode still prints the
// offline banner to stderr, but the indicator is never started (guarded by
// !cfg.OfflineMode()), so no spinner bytes appear on either stream.
func TestOfflineReviewNoSpinner(t *testing.T) {
	dir := setupRepo(t, false)
	stdout, stderr, err := runReviewCaptureBoth(t, dir, map[string]string{"GITL_API_KEY": ""}, "HEAD~1..HEAD")
	if err != nil {
		t.Fatalf("offline review must succeed, got: %v", err)
	}
	if !strings.Contains(stderr, "offline review") {
		t.Errorf("stderr missing the offline banner:\n%s", stderr)
	}
	noSpinnerBytes(t, "stdout", stdout)
	noSpinnerBytes(t, "stderr", stderr)
}

// TestWaitIndicatorSpinsOnTTYStderr is the positive end-to-end case: stderr
// is a real pseudo-terminal, stdout a plain buffer (so wantStream is false →
// buffered branch), and the server holds the response long enough for the
// spinner to animate. The pty must have received the waiting message and the
// final erase sequence; stdout must carry the review, byte-pure.
func TestWaitIndicatorSpinsOnTTYStderr(t *testing.T) {
	handler := &oneShotJSONHandler{
		content: "tty spinner review body\n```risk\n{\"level\":\"low\",\"summary\":\"ok\"}\n```",
	}
	srv := httptest.NewServer(delayHandler(300*time.Millisecond, handler))
	defer srv.Close()

	dir := setupRepo(t, false)

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty not supported on this platform: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })

	// Drain the master end concurrently (same pattern as runReviewOnPTY).
	errCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, ptmx)
		errCh <- buf.String()
	}()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()
	t.Setenv("GITL_API_KEY", "sk-fake-e2e")
	empty := filepath.Join(t.TempDir(), "none.yaml")

	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout) // non-TTY stdout → wantStream false → buffered branch
	root.SetErr(tty)     // real TTY stderr → the indicator animates
	root.SetArgs([]string{"review", "--config", empty, "HEAD~1..HEAD", "--base-url", srv.URL, "--no-cache"})
	runErr := root.ExecuteContext(context.Background())

	_ = tty.Close()
	var stderrOut string
	select {
	case stderrOut = <-errCh:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for pty output after the command finished")
	}

	if runErr != nil {
		t.Fatalf("review must succeed, got: %v", runErr)
	}

	// 1. The spinner animated on the TTY stderr…
	if !strings.Contains(stderrOut, waitingForModelMsg) {
		t.Errorf("TTY stderr missing the waiting message:\n%q", stderrOut)
	}
	// …and erased itself (the clear sequence is present; a raw pty echoes \r
	// as-is, so look for the ANSI erase-to-EOL).
	if !strings.Contains(stderrOut, "\033[K") {
		t.Errorf("TTY stderr missing the erase sequence after stop:\n%q", stderrOut)
	}

	// 2. stdout carries the rendered review and stays byte-pure.
	if !strings.Contains(stdout.String(), "tty spinner review body") {
		t.Errorf("stdout missing the review body:\n%s", stdout.String())
	}
	noSpinnerBytes(t, "stdout", stdout.String())
}
