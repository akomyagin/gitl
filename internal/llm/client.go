package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal net/http client for an OpenAI-compatible
// chat/completions endpoint. It performs a single request with no retry —
// retry/backoff+jitter and typed retryable errors land in Этап 2.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	provider   string
}

var _ Provider = (*Client)(nil)

// ClientConfig configures a Client. It mirrors the config.LLMConfig fields the
// network path needs, without importing internal/config (avoids a cycle and
// keeps llm reusable).
type ClientConfig struct {
	Provider string
	BaseURL  string
	APIKey   string
	Timeout  time.Duration
}

// NewClient constructs a Client. Only provider "openai" is supported in Этап 1;
// any other provider returns a clear error rather than being mishandled
// silently (Ollama/Azure OpenAI branching lands in Этап 2).
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Provider != "openai" {
		return nil, fmt.Errorf("llm: provider %q is not supported until Этап 2 (only \"openai\" is implemented now)", cfg.Provider)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		httpClient: &http.Client{Timeout: timeout},
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		provider:   cfg.Provider,
	}, nil
}

// chatRequest / chatResponse mirror the OpenAI chat/completions wire format.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Complete sends one chat/completions request and returns the assistant text.
// The context (carrying the configured timeout and Ctrl-C cancellation) is
// threaded through the HTTP call.
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	messages := make([]chatMessage, 0, 2)
	if req.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: req.User})

	body, err := json.Marshal(chatRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	})
	if err != nil {
		return Response{}, fmt.Errorf("llm: marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Note: the error must never echo the API key; only status + provider
		// message are surfaced. Typed retryable-vs-fatal handling is Этап 2.
		msg := extractErrorMessage(respBody)
		return Response{}, fmt.Errorf("llm: %s API returned status %d: %s", c.provider, resp.StatusCode, msg)
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if parsed.Error != nil {
		return Response{}, fmt.Errorf("llm: %s API error: %s", c.provider, parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("llm: %s API returned no choices", c.provider)
	}
	return Response{Content: parsed.Choices[0].Message.Content}, nil
}

// extractErrorMessage best-effort parses an OpenAI-style error body, falling
// back to a truncated raw body.
func extractErrorMessage(body []byte) string {
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != nil {
		return parsed.Error.Message
	}
	s := strings.TrimSpace(string(body))
	const max = 200
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
