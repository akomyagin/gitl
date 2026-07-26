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

// oneShotJSONHandler serves a single fixed chat/completions-style JSON
// response regardless of whether the request asked for streaming — used
// below to distinguish "did wantStream even attempt to stream" from "was the
// template applied", independent of the streaming-fallback machinery above.
type oneShotJSONHandler struct {
	content string
}

func (h *oneShotJSONHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": h.content}},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// TestStreamingDisabledWhenOutputTemplateFileSet proves wantStream's
// output.template_file gate actually changes behavior on a real TTY: with a
// custom output.template_file configured, `gitl review` on a PTY must render
// through that template (buffered path) rather than silently falling back to
// the raw, un-templated streaming body. Before this gate existed, the marker
// asserted below would never appear on a real terminal with streaming on.
func TestStreamingDisabledWhenOutputTemplateFileSet(t *testing.T) {
	handler := &oneShotJSONHandler{
		content: "some review body\n```risk\n{\"level\":\"low\",\"summary\":\"ok\"}\n```",
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	dir := setupRepo(t, false)

	tmplPath := filepath.Join(dir, "custom.md.tmpl")
	if err := os.WriteFile(tmplPath, []byte("CUSTOM-TEMPLATE-MARKER Risk={{.RiskLevel}}"), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	cfgYAML := "output:\n" +
		"  stream: true\n" +
		"  template_file: " + tmplPath + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitl.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("write .gitl.yaml: %v", err)
	}

	env := map[string]string{"GITL_API_KEY": "sk-fake-e2e"}
	out, err := runReviewOnPTY(t, dir, env, "HEAD~1..HEAD", "--base-url", srv.URL, "--no-cache")
	if err != nil {
		t.Fatalf("review must succeed, got: %v", err)
	}

	if !strings.Contains(out, "CUSTOM-TEMPLATE-MARKER") {
		t.Errorf("output must go through the custom template (buffered path) when output.template_file is set on a TTY:\n%s", out)
	}
	if !strings.Contains(out, "Risk=low") {
		t.Errorf("custom template's {{.RiskLevel}} substitution missing:\n%s", out)
	}
	// The raw model body (not the template) must not leak through — proves
	// this genuinely went through RenderWithTemplate, not a mix of both paths.
	if strings.Contains(out, "some review body") {
		t.Errorf("raw model body leaked into templated output — streaming path was used instead of buffered:\n%s", out)
	}
}

// nonStreamerWarning is the stderr marker runReview logs (at Warn, visible at
// the default log level) when streaming was wanted but the provider does not
// implement llm.Streamer.
const nonStreamerWarning = "provider does not support streaming"

// anthropicReviewBody is the review text the mock Anthropic server returns.
const anthropicReviewBody = "This is the anthropic buffered review body."

// anthropicOneShotHandler emulates the Anthropic Messages API with a single
// buffered JSON response carrying a valid ```risk``` block (wire shape copied
// from internal/llm/anthropic_test.go respondAnthropicOK). It counts requests
// so tests can assert no streaming attempt ever hit the wire.
type anthropicOneShotHandler struct {
	calls atomic.Int32
}

func (h *anthropicOneShotHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.calls.Add(1)
	content := anthropicReviewBody +
		"\n```risk\n{\"level\":\"low\",\"summary\":\"anthropic ok\"}\n```"
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{
		"content":     []map[string]any{{"type": "text", "text": content}},
		"stop_reason": "end_turn",
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(fmt.Sprintf("encode mock anthropic response: %v", err))
	}
}

// TestNonStreamingProviderWarnsWhenStreamingWanted: the anthropic provider
// does not implement llm.Streamer, so on a real TTY with output.stream on
// (the default) runReview must log a Warn-level heads-up to stderr and then
// produce the full review via the buffered Complete path — with exactly one
// HTTP request (no streaming attempt).
func TestNonStreamingProviderWarnsWhenStreamingWanted(t *testing.T) {
	handler := &anthropicOneShotHandler{}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	dir := setupRepo(t, false)
	env := map[string]string{"GITL_API_KEY": "sk-ant-fake-e2e"}

	// The warning goes through slog, whose handler (built in setupLogging)
	// writes to the real os.Stderr — capture it at the process level, same
	// trick as TestChangelogRequiredCategoryWarns.
	var out string
	var err error
	osStderr := captureStderr(t, func() {
		out, err = runReviewOnPTY(t, dir, env,
			"HEAD~1..HEAD", "--provider", "anthropic", "--base-url", srv.URL, "--no-cache")
	})

	if err != nil {
		t.Fatalf("review must succeed via the buffered path, got: %v", err)
	}

	// 1. The Warn-level fallback notice reached stderr.
	if !strings.Contains(osStderr, nonStreamerWarning) {
		t.Errorf("stderr missing %q warning:\n%s", nonStreamerWarning, osStderr)
	}
	if !strings.Contains(osStderr, "anthropic") {
		t.Errorf("warning should name the provider (anthropic):\n%s", osStderr)
	}

	// 2. The terminal received the full buffered review.
	if !strings.Contains(out, anthropicReviewBody) {
		t.Errorf("terminal output missing the review body:\n%s", out)
	}
	if !strings.Contains(out, "**Risk:**") || !strings.Contains(out, "LOW") {
		t.Errorf("terminal output missing the rendered LOW risk header:\n%s", out)
	}

	// 3. Exactly one request — the buffered Complete; no streaming attempt.
	if got := handler.calls.Load(); got != 1 {
		t.Errorf("server saw %d request(s), want exactly 1 (buffered Complete only)", got)
	}
}

// TestNonStreamingProviderNoWarningWhenStreamOff protects the invariant that
// the warning only fires when streaming was actually expected: same anthropic
// provider on the same TTY, but output.stream: false — no warning on stderr,
// review still rendered.
func TestNonStreamingProviderNoWarningWhenStreamOff(t *testing.T) {
	handler := &anthropicOneShotHandler{}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	dir := setupRepo(t, false)
	if err := os.WriteFile(filepath.Join(dir, ".gitl.yaml"), []byte("output:\n  stream: false\n"), 0o600); err != nil {
		t.Fatalf("write .gitl.yaml: %v", err)
	}
	env := map[string]string{"GITL_API_KEY": "sk-ant-fake-e2e"}

	var out string
	var err error
	osStderr := captureStderr(t, func() {
		out, err = runReviewOnPTY(t, dir, env,
			"HEAD~1..HEAD", "--provider", "anthropic", "--base-url", srv.URL, "--no-cache")
	})

	if err != nil {
		t.Fatalf("review must succeed, got: %v", err)
	}
	if strings.Contains(osStderr, nonStreamerWarning) {
		t.Errorf("no warning expected when output.stream is false, stderr:\n%s", osStderr)
	}
	if !strings.Contains(out, anthropicReviewBody) {
		t.Errorf("terminal output missing the review body:\n%s", out)
	}
}

// streamedReviewBody is the review text the SSE mock server streams token by
// token before the trailing risk block.
const streamedReviewBody = "This is the streamed review body."

// sseReviewHandler emulates an OpenAI-compatible streaming chat/completions
// endpoint: it answers every request with a text/event-stream body whose
// deltas carry the review text and then a trailing ```risk block — with the
// marker deliberately split across two deltas, the same shape the unit tests
// in internal/llm exercise (TestStreamSuppressesRiskBlockFromTerminal).
type sseReviewHandler struct {
	calls atomic.Int32
}

func (h *sseReviewHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.calls.Add(1)
	w.Header().Set("Content-Type", "text/event-stream")
	chunks := []string{
		streamedReviewBody,
		"\n``",
		"`risk\n{\"level\":\"low\",\"summary\":\"streamed ok\"}\n```",
	}
	for _, c := range chunks {
		delta, err := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": c}}},
		})
		if err != nil {
			panic(fmt.Sprintf("marshal mock SSE delta: %v", err))
		}
		fmt.Fprintf(w, "data: %s\n\n", delta)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// TestStreamingSuppressesRiskBlockEndToEnd: on a real TTY the streaming branch
// is active and the stream carries a trailing ```risk block. The terminal must
// receive the review body and the RENDERED risk header ("**Risk:** ..."), but
// never the raw fenced risk block or its JSON payload — the same contract the
// buffered path upholds via ParseRisk.
func TestStreamingSuppressesRiskBlockEndToEnd(t *testing.T) {
	handler := &sseReviewHandler{}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	dir := setupRepo(t, false)
	env := map[string]string{"GITL_API_KEY": "sk-fake-e2e"}

	out, err := runReviewOnPTY(t, dir, env,
		"HEAD~1..HEAD", "--base-url", srv.URL, "--no-cache")
	if err != nil {
		t.Fatalf("streaming review must succeed, got: %v", err)
	}

	// 1. The raw risk block must not reach the terminal in any form.
	if strings.Contains(out, "```risk") {
		t.Errorf("raw risk block leaked to the terminal:\n%s", out)
	}
	if strings.Contains(out, `{"level":`) {
		t.Errorf("risk JSON payload leaked to the terminal:\n%s", out)
	}

	// 2. The review body and the rendered risk header did.
	if !strings.Contains(out, streamedReviewBody) {
		t.Errorf("terminal output missing the streamed review body:\n%s", out)
	}
	if !strings.Contains(out, "**Risk:**") {
		t.Errorf("terminal output missing the rendered risk header:\n%s", out)
	}

	// 3. Streaming itself succeeded: exactly one request, no Complete fallback.
	if got := handler.calls.Load(); got != 1 {
		t.Errorf("server saw %d request(s), want exactly 1 (streaming only, no fallback)", got)
	}
}
