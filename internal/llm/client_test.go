package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClientRejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"ollama", "azure_openai", ""} {
		if _, err := NewClient(ClientConfig{Provider: provider}); err == nil {
			t.Errorf("NewClient(provider=%q): expected \"not supported until Этап 2\" error, got nil", provider)
		}
	}
}

func TestClientCompleteSuccess(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var gotBody chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"## Review\n\nLooks fine."}}]}`))
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{
		Provider: "openai",
		BaseURL:  srv.URL,
		APIKey:   "sk-test-key",
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := client.Complete(context.Background(), Request{
		System:      "system prompt",
		User:        "user prompt",
		Model:       "gpt-4o-mini",
		MaxTokens:   100,
		Temperature: 0.2,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "## Review\n\nLooks fine." {
		t.Errorf("Content = %q", resp.Content)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Errorf("Authorization header = %q", gotAuth)
	}
	if gotBody.Model != "gpt-4o-mini" || len(gotBody.Messages) != 2 {
		t.Errorf("request body = %+v", gotBody)
	}
	if gotBody.Messages[0].Role != "system" || gotBody.Messages[1].Role != "user" {
		t.Errorf("message roles = %+v", gotBody.Messages)
	}
}

func TestClientCompleteErrorDoesNotLeakAPIKey(t *testing.T) {
	t.Parallel()

	const secret = "sk-super-secret-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{Provider: "openai", BaseURL: srv.URL, APIKey: secret, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.Complete(context.Background(), Request{User: "hi", Model: "m"})
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error message leaks the API key: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention the HTTP status, got: %v", err)
	}
}

func TestClientCompleteNoChoices(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	client, err := NewClient(ClientConfig{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Complete(context.Background(), Request{User: "hi", Model: "m"}); err == nil {
		t.Error("expected error for empty choices, got nil")
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

	client, err := NewClient(ClientConfig{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Timeout: time.Minute})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Complete(ctx, Request{User: "hi", Model: "m"}); err == nil {
		t.Error("expected context deadline error, got nil")
	}
}
