package llm

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newStreamClient builds a Client pointed at srv for streaming tests.
func newStreamClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{Provider: ProviderOpenAI, BaseURL: url, APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// sseServer serves the given raw SSE payload with a 200 status.
func sseServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(payload))
	}))
}

func TestStreamHappyPath(t *testing.T) {
	t.Parallel()

	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"\\n```risk\\n{\\\"level\\\":\\\"low\\\",\\\"summary\\\":\\\"safe\\\"}\\n```\"}}]}\n\n" +
		"data: [DONE]\n\n"

	srv := sseServer(t, payload)
	defer srv.Close()

	c := newStreamClient(t, srv.URL)
	var w bytes.Buffer
	resp, err := c.Stream(context.Background(), Request{User: "hi", Model: "gpt-4o-mini"}, &w)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Streamed text (including the raw risk block) is written to w verbatim.
	if !strings.Contains(w.String(), "Hello world") {
		t.Errorf("streamed output missing text; got %q", w.String())
	}
	if !strings.Contains(w.String(), "```risk") {
		t.Errorf("streamed output should contain raw risk block; got %q", w.String())
	}

	// resp.Content has the risk block stripped.
	if strings.Contains(resp.Content, "```risk") {
		t.Errorf("resp.Content should have risk block stripped; got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Hello world") {
		t.Errorf("resp.Content missing body text; got %q", resp.Content)
	}
	if resp.Risk.Level != RiskLow {
		t.Errorf("resp.Risk.Level = %q, want %q", resp.Risk.Level, RiskLow)
	}
	if resp.Risk.Summary != "safe" {
		t.Errorf("resp.Risk.Summary = %q, want %q", resp.Risk.Summary, "safe")
	}
	if resp.Risk.Heuristic {
		t.Error("resp.Risk.Heuristic = true, want false (risk parsed from model)")
	}
}

func TestStreamMissingRiskBlockFallsBackToHeuristic(t *testing.T) {
	t.Parallel()

	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"a review with no risk block\"}}]}\n\n" +
		"data: [DONE]\n\n"

	srv := sseServer(t, payload)
	defer srv.Close()

	c := newStreamClient(t, srv.URL)
	var w bytes.Buffer
	resp, err := c.Stream(context.Background(), Request{User: "hi", Model: "gpt-4o-mini"}, &w)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !resp.Risk.Heuristic {
		t.Error("resp.Risk.Heuristic = false, want true (heuristic fallback)")
	}
	if !ValidRiskLevel(resp.Risk.Level) {
		t.Errorf("heuristic risk level %q is not valid", resp.Risk.Level)
	}
}

func TestStreamNon200BeforeFirstByte(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()

	c := newStreamClient(t, srv.URL)
	var w bytes.Buffer
	_, err := c.Stream(context.Background(), Request{User: "hi", Model: "gpt-4o-mini"}, &w)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if se.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", se.StatusCode)
	}
	if !se.Retryable {
		t.Error("500 should be Retryable")
	}
	if w.Len() != 0 {
		t.Errorf("nothing should be written to w on early failure; got %q", w.String())
	}
}

// TestStreamSanitizesTerminalOutput verifies that ANSI escape sequences in
// model output never reach the terminal-facing writer, even when the escape
// sequence is deliberately split across two SSE data chunks (chunk A ends
// with ESC+"[", chunk B starts with "31mHIDDEN"), while the internal raw
// buffer used for ParseRisk still sees the unfiltered text.
func TestStreamSanitizesTerminalOutput(t *testing.T) {
	t.Parallel()

	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"safe \\u001b[\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"31mHIDDEN\\u001b[0m done\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"\\n```risk\\n{\\\"level\\\":\\\"low\\\",\\\"summary\\\":\\\"safe\\\"}\\n```\"}}]}\n\n" +
		"data: [DONE]\n\n"

	srv := sseServer(t, payload)
	defer srv.Close()

	c := newStreamClient(t, srv.URL)
	var w bytes.Buffer
	resp, err := c.Stream(context.Background(), Request{User: "hi", Model: "gpt-4o-mini"}, &w)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if bytes.ContainsRune(w.Bytes(), 0x1b) {
		t.Errorf("terminal-facing writer received raw ESC byte: %q", w.String())
	}
	if !strings.Contains(w.String(), "safe [31mHIDDEN[0m done") {
		t.Errorf("streamed output lost visible content: %q", w.String())
	}

	// The internal buffer (what ParseRisk consumed) must keep the RAW,
	// unfiltered text: resp.Content is that buffer with the risk block
	// stripped, so the ESC bytes are still present there.
	if !strings.Contains(resp.Content, "safe \x1b[31mHIDDEN\x1b[0m done") {
		t.Errorf("resp.Content should carry the raw unfiltered text: %q", resp.Content)
	}
	if resp.Risk.Level != RiskLow {
		t.Errorf("resp.Risk.Level = %q, want %q (risk block should still parse)", resp.Risk.Level, RiskLow)
	}
}

// TestStreamSanitizerPreservesMultibyteUTF8 verifies that valid multi-byte
// UTF-8 (Cyrillic, emoji) passes through the sanitizing writer unchanged when
// not split by a chunk boundary.
func TestStreamSanitizerPreservesMultibyteUTF8(t *testing.T) {
	t.Parallel()

	payload := "data: {\"choices\":[{\"delta\":{\"content\":\"фикс: добавил ✅ проверку\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"\\n```risk\\n{\\\"level\\\":\\\"low\\\",\\\"summary\\\":\\\"safe\\\"}\\n```\"}}]}\n\n" +
		"data: [DONE]\n\n"

	srv := sseServer(t, payload)
	defer srv.Close()

	c := newStreamClient(t, srv.URL)
	var w bytes.Buffer
	if _, err := c.Stream(context.Background(), Request{User: "hi", Model: "gpt-4o-mini"}, &w); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(w.String(), "фикс: добавил ✅ проверку") {
		t.Errorf("multibyte UTF-8 content corrupted by sanitizer: %q", w.String())
	}
}

// TestStreamCapsTotalBytesRead is a regression test for unbounded memory
// growth in Stream: the per-line scanner buffer caps a single SSE line at
// 1 MiB, but nothing used to cap the TOTAL bytes accumulated across lines, so
// a misbehaving endpoint (e.g. a self-hosted base_url) streaming data: chunks
// forever would grow the internal buffer without bound. The body must be read
// through io.LimitReader(resp.Body, maxResponseBytes) so the total is capped
// at the same 8 MiB threshold the non-streaming path already enforces.
func TestStreamCapsTotalBytesRead(t *testing.T) {
	t.Parallel()

	// ~10 MiB of delta content split into 512 KiB chunks (each SSE line stays
	// well under the 1 MiB per-line scanner limit) — deliberately past
	// maxResponseBytes (8 MiB).
	const chunkContent = 512 * 1024
	const chunks = 20 // 20 × 512 KiB = 10 MiB of content
	piece := strings.Repeat("a", chunkContent)
	var payload strings.Builder
	payload.Grow(chunks * (chunkContent + 64))
	for range chunks {
		payload.WriteString(`data: {"choices":[{"delta":{"content":"`)
		payload.WriteString(piece)
		payload.WriteString("\"}}]}\n\n")
	}
	payload.WriteString("data: [DONE]\n\n")

	srv := sseServer(t, payload.String())
	defer srv.Close()

	// Guard against the regression mode where the stream is consumed forever:
	// the call must return well within this window.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := newStreamClient(t, srv.URL)
	var w bytes.Buffer
	resp, err := c.Stream(ctx, Request{User: "hi", Model: "gpt-4o-mini"}, &w)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Small slack on top of maxResponseBytes for line-level buffering; the
	// key property is that the output is nowhere near the full payload size.
	const limit = maxResponseBytes + 1<<20
	if w.Len() > limit {
		t.Errorf("terminal writer received %d bytes, want <= %d (total stream read must be capped)", w.Len(), limit)
	}
	if len(resp.Content) > limit {
		t.Errorf("resp.Content is %d bytes, want <= %d (total stream read must be capped)", len(resp.Content), limit)
	}
	if total := chunks * chunkContent; w.Len() >= total {
		t.Errorf("terminal writer received the full %d-byte payload; stream reading is unbounded", total)
	}

	// Truncation cuts the stream before any risk block; the existing
	// heuristic fallback must still produce a valid response.
	if !resp.Risk.Heuristic {
		t.Error("resp.Risk.Heuristic = false, want true (truncated stream has no risk block)")
	}
}

// TestStreamerInterfaceAssertion documents that the compile-time assertion in
// stream.go (var _ Streamer = (*Client)(nil)) holds — Client implements Streamer.
func TestStreamerInterfaceAssertion(t *testing.T) {
	t.Parallel()
	var _ Streamer = (*Client)(nil)
}
