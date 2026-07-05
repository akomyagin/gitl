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

// respondOK writes a minimal OpenAI-shaped success body with the given content.
func respondOK(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(chatResponse{
		Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: content}}},
	})
	_, _ = w.Write(body)
}

func TestNewClientRejectsUnknownProvider(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(ClientConfig{Provider: "gemini", BaseURL: "http://x"}); err == nil {
		t.Error("expected error for unknown provider, got nil")
	}
	if _, err := NewClient(ClientConfig{Provider: "", BaseURL: "http://x"}); err == nil {
		t.Error("expected error for empty provider, got nil")
	}
}

func TestNewClientAzureRequiresSubBlock(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(ClientConfig{Provider: ProviderAzure}); err == nil {
		t.Error("expected error when azure sub-block is missing, got nil")
	}
}

// TestRequestBuildingPerProvider checks URL, headers and body for each provider.
func TestRequestBuildingPerProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         ClientConfig
		wantPath    string
		wantQuery   string
		authHeader  string // header name to check
		wantAuth    string // expected value; "" = header must be absent
		noAuthCheck bool
	}{
		{
			name:       "openai",
			cfg:        ClientConfig{Provider: ProviderOpenAI, APIKey: "sk-key"},
			wantPath:   "/chat/completions",
			authHeader: "Authorization",
			wantAuth:   "Bearer sk-key",
		},
		{
			name:       "ollama sends no auth even with key",
			cfg:        ClientConfig{Provider: ProviderOllama, APIKey: "should-be-ignored"},
			wantPath:   "/v1/chat/completions",
			authHeader: "Authorization",
			wantAuth:   "", // must be absent
		},
		{
			name: "azure_openai",
			cfg: ClientConfig{Provider: ProviderAzure, APIKey: "az-key", Azure: AzureConfig{
				Deployment: "my-deploy", APIVersion: "2024-02-01",
			}},
			wantPath:   "/openai/deployments/my-deploy/chat/completions",
			wantQuery:  "api-version=2024-02-01",
			authHeader: "api-key",
			wantAuth:   "az-key",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotPath, gotQuery, gotAuth string
			var authPresent bool
			var gotBody chatRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotQuery = r.URL.RawQuery
				gotAuth = r.Header.Get(tc.authHeader)
				_, authPresent = r.Header[tc.authHeader]
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				respondOK(w, "## Summary\n\nok\n\n```risk\n{\"level\":\"low\",\"summary\":\"tiny\"}\n```")
			}))
			defer srv.Close()

			cfg := tc.cfg
			// Point the provider at the test server.
			switch cfg.Provider {
			case ProviderAzure:
				cfg.Azure.Endpoint = srv.URL
			default:
				cfg.BaseURL = srv.URL
				if cfg.Provider == ProviderOllama {
					cfg.BaseURL = srv.URL + "/v1"
				}
			}
			cfg.Timeout = 5 * time.Second

			client, err := NewClient(cfg)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			resp, err := client.Complete(context.Background(), Request{
				System: "sys", User: "usr", Model: "m", MaxTokens: 10, Temperature: 0.2,
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}

			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if tc.wantQuery != "" && gotQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", gotQuery, tc.wantQuery)
			}
			if tc.wantAuth == "" {
				if authPresent {
					t.Errorf("%s header should be absent, got %q", tc.authHeader, gotAuth)
				}
			} else if gotAuth != tc.wantAuth {
				t.Errorf("%s = %q, want %q", tc.authHeader, gotAuth, tc.wantAuth)
			}
			if len(gotBody.Messages) != 2 || gotBody.Model != "m" {
				t.Errorf("body = %+v", gotBody)
			}
			// Risk parsed from the model block, not the heuristic.
			if resp.Risk.Level != "low" || resp.Risk.Summary != "tiny" {
				t.Errorf("risk = %+v", resp.Risk)
			}
			// The risk block is stripped from the displayed content.
			if strings.Contains(resp.Content, "```risk") {
				t.Errorf("content still has risk block:\n%s", resp.Content)
			}
		})
	}
}

func TestClientRetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		respondOK(w, "ok")
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{Provider: ProviderOpenAI, BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second, MaxRetries: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Complete(context.Background(), Request{User: "hi", Model: "m"}); err != nil {
		t.Fatalf("Complete after 429→200: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls (429 then 200), got %d", got)
	}
}

func TestClientDoesNotRetryFatal(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided"}}`))
	}))
	defer srv.Close()

	const secret = "sk-super-secret-key"
	client, err := NewClient(ClientConfig{Provider: ProviderOpenAI, BaseURL: srv.URL, APIKey: secret, Timeout: 5 * time.Second, MaxRetries: 5})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Complete(context.Background(), Request{User: "hi", Model: "m"})
	if err == nil {
		t.Fatal("expected 401 error, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("401 must not be retried; got %d calls", got)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the API key: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status 401: %v", err)
	}
}

func TestClientStopsAtMaxRetries(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{Provider: ProviderOpenAI, BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second, MaxRetries: 2})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Complete(context.Background(), Request{User: "hi", Model: "m"}); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// 1 initial + 2 retries = 3 attempts.
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 attempts (1 + 2 retries), got %d", got)
	}
}

func TestClientRetrySleepRespectsContext(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	// Many retries would take seconds of backoff; a short context deadline must
	// interrupt the pending retry sleep quickly.
	client, err := NewClient(ClientConfig{Provider: ProviderOpenAI, BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second, MaxRetries: 20})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := client.Complete(ctx, Request{User: "hi", Model: "m"}); err == nil {
		t.Fatal("expected context-cancelled error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("retry sleep did not honor ctx cancellation; took %v", elapsed)
	}
}

func TestClientHeuristicFallbackWhenNoRiskBlock(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondOK(w, "## Summary\n\nNo risk block here.\n")
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{Provider: ProviderOpenAI, BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := client.Complete(context.Background(), Request{
		User: "hi", Model: "m",
		Commits: sampleCommits(),
		Diff:    sampleDiff,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !ValidRiskLevel(resp.Risk.Level) {
		t.Errorf("expected heuristic fallback risk, got %q", resp.Risk.Level)
	}
}

func TestClientCompleteContextCancellation(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	client, err := NewClient(ClientConfig{Provider: ProviderOpenAI, BaseURL: srv.URL, APIKey: "k", Timeout: time.Minute})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Complete(ctx, Request{User: "hi", Model: "m"}); err == nil {
		t.Error("expected context deadline error, got nil")
	}
}
