package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// respondGeminiOK writes a minimal generateContent success body whose single
// candidate carries content.
func respondGeminiOK(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"parts": []map[string]any{{"text": content}}},
			"finishReason": "STOP",
		}},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// newGeminiTestClient builds a GeminiClient pointed at baseURL with
// test-friendly defaults.
func newGeminiTestClient(t *testing.T, baseURL string, maxRetries int) *GeminiClient {
	t.Helper()
	client, err := NewGeminiClient(ClientConfig{
		Provider:   ProviderGemini,
		BaseURL:    baseURL,
		APIKey:     "AIza-test-key",
		Timeout:    5 * time.Second,
		MaxRetries: maxRetries,
	})
	if err != nil {
		t.Fatalf("NewGeminiClient: %v", err)
	}
	return client
}

func TestNewGeminiClientRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := NewGeminiClient(ClientConfig{Provider: ProviderGemini}); err == nil {
		t.Error("expected error for missing api_key, got nil")
	}
}

func TestNewGeminiClientDefaultsBaseURL(t *testing.T) {
	t.Parallel()
	client, err := NewGeminiClient(ClientConfig{Provider: ProviderGemini, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewGeminiClient: %v", err)
	}
	if client.baseURL != defaultGeminiBaseURL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, defaultGeminiBaseURL)
	}
}

// TestGeminiRequestBuilding checks the generateContent wire shape: path with
// the model embedded, ?key= query auth (and NO auth header), systemInstruction,
// contents[].parts[], and generationConfig.
func TestGeminiRequestBuilding(t *testing.T) {
	t.Parallel()

	var gotPath, gotKey, gotAuthz, gotXAPIKey string
	var gotBody geminiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.URL.Query().Get("key")
		gotAuthz = r.Header.Get("Authorization")
		gotXAPIKey = r.Header.Get("x-api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		respondGeminiOK(w, "## Summary\n\nok\n\n```risk\n{\"level\":\"medium\",\"summary\":\"meh\"}\n```")
	}))
	defer srv.Close()

	client := newGeminiTestClient(t, srv.URL, 0)
	resp, err := client.Complete(context.Background(), Request{
		System: "sys", User: "usr", Model: "gemini-2.5-flash", MaxTokens: 42, Temperature: 0.3,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotPath != "/models/gemini-2.5-flash:generateContent" {
		t.Errorf("path = %q, want /models/gemini-2.5-flash:generateContent", gotPath)
	}
	if gotKey != "AIza-test-key" {
		t.Errorf("?key= = %q, want AIza-test-key", gotKey)
	}
	if gotAuthz != "" || gotXAPIKey != "" {
		t.Errorf("auth headers must be absent, got Authorization=%q x-api-key=%q", gotAuthz, gotXAPIKey)
	}
	if gotBody.SystemInstruction == nil || len(gotBody.SystemInstruction.Parts) != 1 ||
		gotBody.SystemInstruction.Parts[0].Text != "sys" {
		t.Errorf("systemInstruction = %+v, want single part %q", gotBody.SystemInstruction, "sys")
	}
	if len(gotBody.Contents) != 1 || gotBody.Contents[0].Role != "user" ||
		len(gotBody.Contents[0].Parts) != 1 || gotBody.Contents[0].Parts[0].Text != "usr" {
		t.Errorf("contents = %+v, want one user content with one part", gotBody.Contents)
	}
	if gotBody.GenerationConfig == nil || gotBody.GenerationConfig.MaxOutputTokens != 42 ||
		gotBody.GenerationConfig.Temperature != 0.3 {
		t.Errorf("generationConfig = %+v", gotBody.GenerationConfig)
	}
	// Risk parsed from the model block, not the heuristic.
	if resp.Risk.Level != "medium" || resp.Risk.Summary != "meh" || resp.Risk.Heuristic {
		t.Errorf("risk = %+v", resp.Risk)
	}
	if strings.Contains(resp.Content, "```risk") {
		t.Errorf("content still has risk block:\n%s", resp.Content)
	}
}

// An empty system prompt must omit systemInstruction entirely.
func TestGeminiOmitsEmptySystemInstruction(t *testing.T) {
	t.Parallel()

	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		respondGeminiOK(w, "ok")
	}))
	defer srv.Close()

	client := newGeminiTestClient(t, srv.URL, 0)
	if _, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"}); err != nil {
		t.Fatalf("CompleteRaw: %v", err)
	}
	if _, present := raw["systemInstruction"]; present {
		t.Error("systemInstruction must be omitted when the system prompt is empty")
	}
}

// An explicit Temperature: 0 ("fully deterministic output") must be
// serialized as "temperature":0 inside generationConfig, never dropped by
// omitempty — otherwise the provider silently substitutes its own default.
// generationConfig itself must be present even when MaxTokens is unset too
// (the old conditional skipped the whole object in that corner).
func TestGeminiTemperatureZeroIsSerialized(t *testing.T) {
	t.Parallel()

	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		respondGeminiOK(w, "ok")
	}))
	defer srv.Close()

	client := newGeminiTestClient(t, srv.URL, 0)
	// MaxTokens deliberately unset: generationConfig must still be built.
	if _, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m", Temperature: 0}); err != nil {
		t.Fatalf("CompleteRaw: %v", err)
	}
	genRaw, present := raw["generationConfig"]
	if !present {
		t.Fatal(`"generationConfig" missing from request body; must be built unconditionally`)
	}
	var gen map[string]json.RawMessage
	if err := json.Unmarshal(genRaw, &gen); err != nil {
		t.Fatalf("unmarshal generationConfig: %v", err)
	}
	got, present := gen["temperature"]
	if !present {
		t.Fatal(`"temperature" field missing from generationConfig; explicit 0 was dropped`)
	}
	if string(got) != "0" {
		t.Errorf("temperature = %s, want 0", got)
	}
	// MaxTokens 0 means "not set" — maxOutputTokens keeps omitempty.
	if _, present := gen["maxOutputTokens"]; present {
		t.Error(`"maxOutputTokens" must be omitted when Request.MaxTokens is unset`)
	}
}

func TestGeminiHeuristicFallbackWhenNoRiskBlock(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondGeminiOK(w, "## Summary\n\nNo risk block here.\n")
	}))
	defer srv.Close()

	client := newGeminiTestClient(t, srv.URL, 0)
	resp, err := client.Complete(context.Background(), Request{
		User: "hi", Model: "m",
		Commits: sampleCommits(),
		Diff:    sampleDiff,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !ValidRiskLevel(resp.Risk.Level) || !resp.Risk.Heuristic {
		t.Errorf("expected heuristic fallback risk, got %+v", resp.Risk)
	}
}

func TestGeminiCompleteRawReturnsContentVerbatim(t *testing.T) {
	t.Parallel()

	const content = "prose before\n```changelog\n{\"categories\": {}}\n```\n" +
		"```risk\n{\"level\":\"low\",\"summary\":\"must NOT be stripped\"}\n```"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondGeminiOK(w, content)
	}))
	defer srv.Close()

	client := newGeminiTestClient(t, srv.URL, 0)
	got, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"})
	if err != nil {
		t.Fatalf("CompleteRaw: %v", err)
	}
	if got != content {
		t.Errorf("CompleteRaw altered the content:\ngot:  %q\nwant: %q", got, content)
	}
}

// Multiple parts of the first candidate are concatenated.
func TestGeminiConcatenatesParts(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"part one, "},{"text":"part two"}]},"finishReason":"STOP"}]}`))
	}))
	defer srv.Close()

	client := newGeminiTestClient(t, srv.URL, 0)
	got, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"})
	if err != nil {
		t.Fatalf("CompleteRaw: %v", err)
	}
	if got != "part one, part two" {
		t.Errorf("content = %q, want concatenated parts", got)
	}
}

// The shared retry() helper drives the new client too: 503 then 200 succeeds.
func TestGeminiRetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"try later","status":"UNAVAILABLE"}}`))
			return
		}
		respondGeminiOK(w, "ok")
	}))
	defer srv.Close()

	client := newGeminiTestClient(t, srv.URL, 3)
	if _, err := client.Complete(context.Background(), Request{User: "hi", Model: "m", Commits: sampleCommits(), Diff: sampleDiff}); err != nil {
		t.Fatalf("Complete after 503→200: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls (503 then 200), got %d", got)
	}
}

// Fatal 4xx must not be retried; the Google error envelope's message and
// status must surface, and the API key must never leak into the error text.
func TestGeminiDoesNotRetryFatalAndParsesErrorEnvelope(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"API key not valid","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	const secret = "AIza-super-secret-key"
	client, err := NewGeminiClient(ClientConfig{
		Provider: ProviderGemini, BaseURL: srv.URL, APIKey: secret,
		Timeout: 5 * time.Second, MaxRetries: 5,
	})
	if err != nil {
		t.Fatalf("NewGeminiClient: %v", err)
	}
	_, err = client.Complete(context.Background(), Request{User: "hi", Model: "m"})
	if err == nil {
		t.Fatal("expected 400 error, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("400 must not be retried; got %d calls", got)
	}
	if !strings.Contains(err.Error(), "API key not valid") || !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
		t.Errorf("error should carry the envelope message and status: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the API key: %v", err)
	}
}

// A 200 with empty candidates and a promptFeedback.blockReason (safety block)
// is an explicit non-retryable error — no panic, no silent empty review.
func TestGeminiSafetyBlockedResponseIsExplicitError(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[],"promptFeedback":{"blockReason":"SAFETY"}}`))
	}))
	defer srv.Close()

	client := newGeminiTestClient(t, srv.URL, 3)
	_, err := client.Complete(context.Background(), Request{User: "hi", Model: "m"})
	if err == nil {
		t.Fatal("expected safety-block error, got nil")
	}
	if !strings.Contains(err.Error(), `blockReason="SAFETY"`) {
		t.Errorf("error should mention the block reason: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("a safety block is not retryable; got %d calls", got)
	}
}

// Transport-level failures wrap *url.Error, whose message contains the full
// request URL — including ?key=. The client must redact it before surfacing.
func TestGeminiTransportErrorRedactsAPIKey(t *testing.T) {
	t.Parallel()

	// A server that is already closed produces a connection-refused url.Error.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	base := srv.URL
	srv.Close()

	const secret = "AIza-must-not-leak"
	client, err := NewGeminiClient(ClientConfig{
		Provider: ProviderGemini, BaseURL: base, APIKey: secret,
		Timeout: 2 * time.Second, MaxRetries: 0,
	})
	if err != nil {
		t.Fatalf("NewGeminiClient: %v", err)
	}
	_, err = client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"})
	if err == nil {
		t.Fatal("expected transport error against a closed server, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("transport error leaks the API key: %v", err)
	}
	if !strings.Contains(err.Error(), "key=REDACTED") {
		t.Errorf("expected redacted key marker in transport error: %v", err)
	}
}

func TestGeminiCompleteContextCancellation(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	client, err := NewGeminiClient(ClientConfig{
		Provider: ProviderGemini, BaseURL: srv.URL, APIKey: "k", Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewGeminiClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Complete(ctx, Request{User: "hi", Model: "m"}); err == nil {
		t.Error("expected context deadline error, got nil")
	}
}

// TestGeminiCapabilityAssertions documents the compile-time assertions in
// gemini.go: the client is both a Provider and a RawCompleter (the capability
// changelog --ai probes for).
func TestGeminiCapabilityAssertions(t *testing.T) {
	t.Parallel()
	var _ Provider = (*GeminiClient)(nil)
	var _ RawCompleter = (*GeminiClient)(nil)
}
