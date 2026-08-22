package cli

// Unit tests for waitIndicator (waiting.go). A *bytes.Buffer is never a TTY,
// so the forced-TTY cases flip isTTY by hand after construction — the tests
// live in package cli precisely to reach that field. All writes from the
// spinner goroutine happen-before stop() returns (channel close + receive),
// so reading the buffer after stop is race-free under -race.

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestWaitIndicatorSilentOnNonTTY: with a non-TTY writer (bytes.Buffer),
// start/stop must produce zero bytes — the CI/pipe silence guarantee.
func TestWaitIndicatorSilentOnNonTTY(t *testing.T) {
	var buf bytes.Buffer
	ind := newWaitIndicator(&buf)
	if ind.isTTY {
		t.Fatal("bytes.Buffer must not be detected as a TTY")
	}
	ind.start(waitingForModelMsg)
	time.Sleep(150 * time.Millisecond)
	ind.stop()
	if got := buf.String(); got != "" {
		t.Errorf("non-TTY indicator must write nothing, got %q", got)
	}
}

// TestWaitIndicatorForcedTTYFramesAndClear: on a (forced) TTY the indicator
// writes spinner frames with the message to the provided writer only, and
// stop() leaves the erase sequence as the very last bytes so subsequent
// output starts on a clean line.
func TestWaitIndicatorForcedTTYFramesAndClear(t *testing.T) {
	var buf bytes.Buffer
	ind := newWaitIndicator(&buf)
	ind.isTTY = true // force: buffers are never real TTYs

	ind.start(waitingForModelMsg)
	// Long enough for the first (synchronous-in-goroutine) frame plus at
	// least one ticker frame.
	time.Sleep(250 * time.Millisecond)
	ind.stop()

	out := buf.String()
	if !strings.Contains(out, waitingForModelMsg) {
		t.Errorf("output missing the waiting message, got %q", out)
	}
	if !strings.Contains(out, "\r") {
		t.Errorf("frames must be drawn with carriage returns, got %q", out)
	}
	if !strings.ContainsAny(out, `|/-\`) {
		t.Errorf("output missing any spinner frame char, got %q", out)
	}
	const clear = "\r\033[K"
	if !strings.HasSuffix(out, clear) {
		t.Errorf("output must END with the erase sequence %q (nothing may leak after stop), got tail %q", clear, out[max(0, len(out)-16):])
	}
}

// TestWaitIndicatorStopIdempotent: a second stop() (e.g. a deferred safety
// stop after the explicit one) must be a no-op — no panic, no extra bytes.
func TestWaitIndicatorStopIdempotent(t *testing.T) {
	var buf bytes.Buffer
	ind := newWaitIndicator(&buf)
	ind.isTTY = true

	ind.start(waitingForModelMsg)
	ind.stop()
	after := buf.Len()
	ind.stop() // must not panic (double channel close) nor write again
	if buf.Len() != after {
		t.Errorf("second stop wrote %d extra byte(s)", buf.Len()-after)
	}
}

// TestWaitIndicatorStopWithoutStart: stop on a never-started indicator (the
// offline-mode path in runReview constructs but never starts it) is a no-op.
func TestWaitIndicatorStopWithoutStart(t *testing.T) {
	var buf bytes.Buffer
	ind := newWaitIndicator(&buf)
	ind.stop()
	ind.stop()
	if got := buf.String(); got != "" {
		t.Errorf("stop without start must write nothing, got %q", got)
	}
}
