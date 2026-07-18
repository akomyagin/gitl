package cli

// End-to-end test for the streaming fallback scenario (review.go, runReview):
// the streaming attempt fails BEFORE the first token (429), zero bytes reach
// the terminal, and runReview silently falls back to the buffered Complete
// path, which produces the full review.
//
// Why this file exists separately from command_test.go: wantStream requires a
// real TTY (*os.File passing term.IsTerminal), so the usual bytes.Buffer via
// cmd.SetOut never exercises the streaming branch. Here a pseudo-terminal from
// creack/pty is attached as the command's stdout — the slave end (tty) is a
// genuine *os.File recognized as a terminal, and everything written to it is
// captured by reading the master end (ptmx).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
)

// fallbackReviewBody is the review text the mock server returns on the second
// (non-streaming Complete) request. The trailing risk block is valid, so the
// rendered artifact carries a model-scored LOW risk, not the heuristic.
const fallbackReviewBody = "This is the fallback review body produced by Complete."

// streamFallbackHandler emulates an OpenAI-compatible chat/completions
// endpoint for the fallback scenario:
//
//	request #1 (the streaming attempt) → 429 immediately, empty body, before
//	  any SSE chunk — so byteCountWriter.written stays 0 and the fallback fires;
//	request #2 (the automatic Complete fallback) → 200 with a normal JSON
//	  response containing a valid ```risk``` block.
//
// The atomic counter is the source of truth for which request this is; the
// request bodies' "stream" flag is additionally recorded for stricter asserts.
type streamFallbackHandler struct {
	calls atomic.Int32

	mu     sync.Mutex
	bodies []string
}

func (h *streamFallbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, string(body))
	h.mu.Unlock()

	switch h.calls.Add(1) {
	case 1:
		// Streaming attempt: fail before the first SSE chunk. 429 is
		// classified retryable, but Stream never retries — it surfaces the
		// StatusError so runReview can fall back.
		w.WriteHeader(http.StatusTooManyRequests)
	default:
		content := fallbackReviewBody +
			"\n```risk\n{\"level\":\"low\",\"summary\":\"fallback ok\"}\n```"
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			panic(fmt.Sprintf("encode mock response: %v", err))
		}
	}
}

// requestStreamFlag reports whether the i-th recorded request body (0-based)
// asked for streaming ("stream":true).
func (h *streamFallbackHandler) requestStreamFlag(t *testing.T, i int) bool {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.bodies) {
		t.Fatalf("request #%d not recorded (got %d requests)", i+1, len(h.bodies))
	}
	var parsed struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal([]byte(h.bodies[i]), &parsed); err != nil {
		t.Fatalf("request #%d body is not JSON: %v", i+1, err)
	}
	return parsed.Stream
}

// runReviewOnPTY runs `gitl review <args>` with the command's stdout attached
// to the slave end of a freshly-allocated pseudo-terminal, and returns
// everything the command wrote to that "terminal" plus the run error. It
// skips the test when a pty cannot be allocated on this platform.
func runReviewOnPTY(t *testing.T, dir string, env map[string]string, args ...string) (string, error) {
	t.Helper()

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty not supported on this platform: %v", err)
	}
	// ptmx is closed explicitly below; this is a safety net for early exits.
	t.Cleanup(func() { _ = ptmx.Close() })

	// Drain the master end concurrently so the command can never block on a
	// full pty buffer. The goroutine ends when the slave end is fully closed
	// (read returns EOF/EIO — expected, not an error).
	outCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, ptmx)
		outCh <- buf.String()
	}()

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
	// Same isolation as runReviewInDir: point the personal config at a
	// non-existent path so no host config leaks in.
	empty := filepath.Join(t.TempDir(), "none.yaml")

	root := newRootCmd()
	var stderr bytes.Buffer
	root.SetOut(tty) // the real *os.File TTY — this is what enables wantStream
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"review", "--config", empty}, args...))
	runErr := root.ExecuteContext(context.Background())

	// Close the slave end so the ptmx reader sees EOF/EIO and finishes.
	_ = tty.Close()

	select {
	case out := <-outCh:
		_ = ptmx.Close()
		return out, runErr
	case <-time.After(15 * time.Second):
		_ = ptmx.Close()
		t.Fatal("timed out waiting for pty output after the command finished")
		return "", runErr // unreachable
	}
}

// TestStreamingFallbackEndToEnd is the full scenario: a real TTY on stdout
// activates the streaming branch, the mock server 429s the streaming attempt
// before the first token, and runReview automatically falls back to the
// buffered Complete path, which succeeds — exactly two HTTP requests total.
func TestStreamingFallbackEndToEnd(t *testing.T) {
	handler := &streamFallbackHandler{}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	dir := setupRepo(t, false)
	env := map[string]string{"GITL_API_KEY": "sk-fake-e2e"}

	// --no-cache: never touch the on-disk LLM cache from a test.
	// No --no-stream, no --dry-run, default md format, output.stream defaults
	// to true — all wantStream conditions hold once stdout is a TTY.
	out, err := runReviewOnPTY(t, dir, env,
		"HEAD~1..HEAD", "--base-url", srv.URL, "--no-cache")

	// 1. The command must succeed: the fallback produced a review, it did not
	// merely propagate the streaming failure.
	if err != nil {
		t.Fatalf("review must succeed via the Complete fallback, got: %v", err)
	}

	// 2. The terminal received the full buffered review from Complete: the
	// body text and the rendered risk header (model-scored LOW, not heuristic).
	if !strings.Contains(out, fallbackReviewBody) {
		t.Errorf("terminal output missing the fallback review body:\n%s", out)
	}
	if !strings.Contains(out, "**Risk:**") {
		t.Errorf("terminal output missing the rendered risk header:\n%s", out)
	}
	if !strings.Contains(out, "LOW") {
		t.Errorf("terminal output should carry the model's LOW risk level:\n%s", out)
	}
	// The raw risk block is stripped by ParseRisk before rendering.
	if strings.Contains(out, "```risk") {
		t.Errorf("raw risk block must not leak into the rendered output:\n%s", out)
	}

	// 3. Exactly two requests reached the server: one streaming attempt
	// (429) and one fallback Complete (200). No retries, no extras.
	if got := handler.calls.Load(); got != 2 {
		t.Errorf("server saw %d request(s), want exactly 2 (stream attempt + Complete fallback)", got)
	}

	// 4. Wire-format sanity: the first request asked for streaming, the
	// second (fallback) did not.
	if !handler.requestStreamFlag(t, 0) {
		t.Error(`first request should be the streaming attempt ("stream":true)`)
	}
	if handler.requestStreamFlag(t, 1) {
		t.Error(`second request should be the non-streaming Complete fallback (no "stream":true)`)
	}
}
