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

// respondAnthropicOK writes a minimal Anthropic Messages API success body
// whose single text block carries content.
func respondAnthropicOK(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{
		"content":     []map[string]any{{"type": "text", "text": content}},
		"stop_reason": "end_turn",
	}
	_ = json.NewEncoder(w).Encode(body)
}

// newAnthropicTestClient builds an AnthropicClient pointed at baseURL with
// test-friendly defaults.
func newAnthropicTestClient(t *testing.T, baseURL string, maxRetries int) *AnthropicClient {
	t.Helper()
	client, err := NewAnthropicClient(ClientConfig{
		Provider:   ProviderAnthropic,
		BaseURL:    baseURL,
		APIKey:     "sk-ant-test",
		Timeout:    5 * time.Second,
		MaxRetries: maxRetries,
	})
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	return client
}

func TestNewAnthropicClientRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := NewAnthropicClient(ClientConfig{Provider: ProviderAnthropic}); err == nil {
		t.Error("expected error for missing api_key, got nil")
	}
}

func TestNewAnthropicClientDefaultsBaseURL(t *testing.T) {
	t.Parallel()
	client, err := NewAnthropicClient(ClientConfig{Provider: ProviderAnthropic, APIKey: "k"})
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	if client.baseURL != defaultAnthropicBaseURL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, defaultAnthropicBaseURL)
	}
}

// TestAnthropicRequestBuilding checks the Messages API wire shape: path,
// auth headers (x-api-key + anthropic-version, NO Authorization: Bearer),
// top-level system field, a single user message, and a mandatory max_tokens.
func TestAnthropicRequestBuilding(t *testing.T) {
	t.Parallel()

	var gotPath, gotAPIKey, gotVersion, gotAuthz string
	var gotBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotAuthz = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		respondAnthropicOK(w, "## Summary\n\nok\n\n```risk\n{\"level\":\"low\",\"summary\":\"tiny\"}\n```")
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 0)
	resp, err := client.Complete(context.Background(), Request{
		System: "sys", User: "usr", Model: "claude-sonnet-4-6", MaxTokens: 10, Temperature: 0.2,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", gotPath)
	}
	if gotAPIKey != "sk-ant-test" {
		t.Errorf("x-api-key = %q, want sk-ant-test", gotAPIKey)
	}
	if gotVersion != anthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, anthropicVersion)
	}
	if gotAuthz != "" {
		t.Errorf("Authorization header must be absent, got %q", gotAuthz)
	}
	// system is a top-level field, NOT the first message.
	if gotBody.System != "sys" {
		t.Errorf("top-level system = %q, want %q", gotBody.System, "sys")
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Role != "user" || gotBody.Messages[0].Content != "usr" {
		t.Errorf("messages = %+v, want exactly one user message", gotBody.Messages)
	}
	if gotBody.MaxTokens != 10 {
		t.Errorf("max_tokens = %d, want 10", gotBody.MaxTokens)
	}
	if gotBody.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q", gotBody.Model)
	}
	// Risk parsed from the model block, not the heuristic.
	if resp.Risk.Level != "low" || resp.Risk.Summary != "tiny" || resp.Risk.Heuristic {
		t.Errorf("risk = %+v", resp.Risk)
	}
	if strings.Contains(resp.Content, "```risk") {
		t.Errorf("content still has risk block:\n%s", resp.Content)
	}
}

// An explicit Temperature: 0 ("fully deterministic output") must be
// serialized as "temperature":0, never dropped by omitempty — otherwise the
// provider silently substitutes its own default (~1.0).
func TestAnthropicTemperatureZeroIsSerialized(t *testing.T) {
	t.Parallel()

	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		respondAnthropicOK(w, "ok")
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 0)
	if _, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m", MaxTokens: 10, Temperature: 0}); err != nil {
		t.Fatalf("CompleteRaw: %v", err)
	}
	got, present := raw["temperature"]
	if !present {
		t.Fatal(`"temperature" field missing from request body; explicit 0 was dropped`)
	}
	if string(got) != "0" {
		t.Errorf("temperature = %s, want 0", got)
	}
}

// max_tokens is mandatory on the Anthropic wire; an unset Request.MaxTokens
// must be replaced with a positive default, never sent as 0/omitted.
func TestAnthropicMaxTokensDefaulted(t *testing.T) {
	t.Parallel()

	var gotBody anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		respondAnthropicOK(w, "ok")
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 0)
	if _, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"}); err != nil {
		t.Fatalf("CompleteRaw: %v", err)
	}
	if gotBody.MaxTokens != anthropicDefaultMaxTokens {
		t.Errorf("max_tokens = %d, want defaulted %d", gotBody.MaxTokens, anthropicDefaultMaxTokens)
	}
}

func TestAnthropicHeuristicFallbackWhenNoRiskBlock(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondAnthropicOK(w, "## Summary\n\nNo risk block here.\n")
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 0)
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

func TestAnthropicCompleteRawReturnsContentVerbatim(t *testing.T) {
	t.Parallel()

	const content = "prose before\n```changelog\n{\"categories\": {}}\n```\n" +
		"```risk\n{\"level\":\"low\",\"summary\":\"must NOT be stripped\"}\n```"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondAnthropicOK(w, content)
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 0)
	got, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"})
	if err != nil {
		t.Fatalf("CompleteRaw: %v", err)
	}
	if got != content {
		t.Errorf("CompleteRaw altered the content:\ngot:  %q\nwant: %q", got, content)
	}
}

// Multiple text blocks in the content list are concatenated, not truncated to
// the first.
func TestAnthropicConcatenatesTextBlocks(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"part one, "},{"type":"text","text":"part two"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 0)
	got, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"})
	if err != nil {
		t.Fatalf("CompleteRaw: %v", err)
	}
	if got != "part one, part two" {
		t.Errorf("content = %q, want concatenated blocks", got)
	}
}

// A 200 with an empty content list is an explicit error, not a panic or an
// empty review.
func TestAnthropicEmptyContentIsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 0)
	if _, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"}); err == nil {
		t.Fatal("expected error for empty content, got nil")
	}
}

// The shared retry() helper drives the new client too: 429 then 200 succeeds.
func TestAnthropicRetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`))
			return
		}
		respondAnthropicOK(w, "ok")
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 3)
	if _, err := client.Complete(context.Background(), Request{User: "hi", Model: "m", Commits: sampleCommits(), Diff: sampleDiff}); err != nil {
		t.Fatalf("Complete after 429→200: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls (429 then 200), got %d", got)
	}
}

// Anthropic's 529 "overloaded" falls in the retryable 5xx range.
func TestAnthropicRetriesOverloaded529(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(529)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		}
		respondAnthropicOK(w, "ok")
	}))
	defer srv.Close()

	client := newAnthropicTestClient(t, srv.URL, 3)
	if _, err := client.CompleteRaw(context.Background(), Request{User: "hi", Model: "m"}); err != nil {
		t.Fatalf("CompleteRaw after 529→200: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls (529 then 200), got %d", got)
	}
}

// Fatal 4xx must not be retried; the Anthropic error envelope's message must
// surface, and the API key must never leak into the error text.
func TestAnthropicDoesNotRetryFatalAndParsesErrorEnvelope(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens is too large"}}`))
	}))
	defer srv.Close()

	const secret = "sk-ant-super-secret"
	client, err := NewAnthropicClient(ClientConfig{
		Provider: ProviderAnthropic, BaseURL: srv.URL, APIKey: secret,
		Timeout: 5 * time.Second, MaxRetries: 5,
	})
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	_, err = client.Complete(context.Background(), Request{User: "hi", Model: "m"})
	if err == nil {
		t.Fatal("expected 400 error, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("400 must not be retried; got %d calls", got)
	}
	if !strings.Contains(err.Error(), "max_tokens is too large") {
		t.Errorf("error should carry the envelope message: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status 400: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the API key: %v", err)
	}
}

func TestAnthropicCompleteContextCancellation(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	client, err := NewAnthropicClient(ClientConfig{
		Provider: ProviderAnthropic, BaseURL: srv.URL, APIKey: "k", Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Complete(ctx, Request{User: "hi", Model: "m"}); err == nil {
		t.Error("expected context deadline error, got nil")
	}
}

// TestAnthropicCapabilityAssertions documents the compile-time assertions in
// anthropic.go: the client is both a Provider and a RawCompleter (the
// capability changelog --ai probes for).
func TestAnthropicCapabilityAssertions(t *testing.T) {
	t.Parallel()
	var _ Provider = (*AnthropicClient)(nil)
	var _ RawCompleter = (*AnthropicClient)(nil)
}
