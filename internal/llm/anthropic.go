package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ProviderAnthropic selects the native Anthropic Messages API client
// (AnthropicClient), a separate Provider implementation from the
// OpenAI-compatible Client: the wire format differs fundamentally (top-level
// `system` field, mandatory `max_tokens`, `content` blocks instead of
// `choices`, x-api-key auth instead of Bearer).
const ProviderAnthropic = "anthropic"

const (
	// defaultAnthropicBaseURL is used when llm.base_url is empty; tests
	// override it with an httptest server URL.
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	// anthropicVersion is the required anthropic-version header. 2023-06-01
	// is the current stable Messages API version (per the public Anthropic
	// API docs as of 2026-07 — the version string is a wire-format contract,
	// not a release date, and has been stable since launch).
	anthropicVersion = "2023-06-01"
	// anthropicDefaultMaxTokens guards the mandatory max_tokens field when a
	// caller leaves Request.MaxTokens unset (config.validate() normally
	// prevents that, but the wire format requires the field, unlike OpenAI).
	anthropicDefaultMaxTokens = 1024
)

// AnthropicClient is a hand-written net/http client for the Anthropic
// Messages API (POST {base}/v1/messages). It reuses the package-level
// retry/backoff loop and error classification shared with the
// OpenAI-compatible Client: 429 and 5xx (incl. Anthropic's 529 "overloaded")
// are retryable, other 4xx are fatal.
type AnthropicClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	version    string
	maxRetries int
}

var (
	_ Provider     = (*AnthropicClient)(nil)
	_ RawCompleter = (*AnthropicClient)(nil)
)

// NewAnthropicClient constructs an AnthropicClient. The API key is mandatory
// (Anthropic has no keyless mode); base_url is optional and defaults to the
// public endpoint.
func NewAnthropicClient(cfg ClientConfig) (*AnthropicClient, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm: provider %q requires llm.api_key (or env GITL_API_KEY)", ProviderAnthropic)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	retries := cfg.MaxRetries
	if retries < 0 {
		retries = 0
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultAnthropicBaseURL
	}
	return &AnthropicClient{
		httpClient: &http.Client{Timeout: timeout},
		baseURL:    baseURL,
		apiKey:     cfg.APIKey,
		version:    anthropicVersion,
		maxRetries: retries,
	}, nil
}

// anthropicMessage / anthropicRequest / anthropicResponse mirror the
// Anthropic Messages API wire format: system prompt is a TOP-LEVEL field (not
// a message role), max_tokens is mandatory, and the assistant reply arrives
// as a list of typed content blocks.
type anthropicMessage struct {
	Role    string `json:"role"` // always "user" here — gitl sends single-turn prompts
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"` // mandatory, unlike OpenAI
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	// Temperature has NO omitempty: 0 is a meaningful, explicitly configurable
	// value ("fully deterministic output") and must reach the wire instead of
	// silently falling back to the provider default.
	Temperature float64 `json:"temperature"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"` // "text" blocks carry the reply
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason,omitempty"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// marshalAnthropicRequest builds the wire body shared by Complete and
// CompleteRaw.
func marshalAnthropicRequest(req Request) ([]byte, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	body, err := json.Marshal(anthropicRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		System:      req.System,
		Messages:    []anthropicMessage{{Role: "user", Content: req.User}},
		Temperature: req.Temperature,
	})
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}
	return body, nil
}

// Complete sends a Messages API request (with retry/backoff) and returns the
// assistant text plus a risk score, with the same risk-block/heuristic
// contract as Client.Complete (§7.2, §7.3).
func (c *AnthropicClient) Complete(ctx context.Context, req Request) (Response, error) {
	body, err := marshalAnthropicRequest(req)
	if err != nil {
		return Response{}, err
	}
	content, err := c.doWithRetry(ctx, body)
	if err != nil {
		return Response{}, err
	}

	stripped, risk, ok := ParseRisk(content)
	if !ok {
		risk = HeuristicRisk(req.Commits, req.Diff)
		risk.Heuristic = true
		slog.Warn("model risk block missing or invalid; using heuristic fallback", "level", risk.Level)
		stripped = strings.TrimRight(content, " \t\r\n") + "\n"
	}
	return Response{Content: stripped, Risk: risk}, nil
}

// CompleteRaw returns the assistant text verbatim — no risk-block parsing —
// implementing the RawCompleter capability (used by changelog --ai).
func (c *AnthropicClient) CompleteRaw(ctx context.Context, req Request) (string, error) {
	body, err := marshalAnthropicRequest(req)
	if err != nil {
		return "", err
	}
	return c.doWithRetry(ctx, body)
}

// doWithRetry wraps the shared package-level retry loop around doOnce.
func (c *AnthropicClient) doWithRetry(ctx context.Context, body []byte) (string, error) {
	return retry(ctx, c.maxRetries, func(ctx context.Context) (string, error) {
		return c.doOnce(ctx, body)
	})
}

// doOnce performs a single request/response round trip against /v1/messages.
func (c *AnthropicClient) doOnce(ctx context.Context, body []byte) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Anthropic auth: x-api-key + anthropic-version, never Authorization: Bearer.
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", c.version)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("llm: request cancelled: %w", ctx.Err())
		}
		return "", &networkError{err: err}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// The API key travels only in the request header; the surfaced error
		// carries just the status and Anthropic's own message.
		return "", classifyStatus(ProviderAnthropic, resp.StatusCode, anthropicErrorMessage(respBody))
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("llm: %s API error: %s", ProviderAnthropic, parsed.Error.Message)
	}

	// Concatenate all text blocks — normally there is exactly one, but the
	// content list is defined as a sequence, so joining is the safe reading.
	var b strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("llm: %s API returned no text content (stop_reason=%q)", ProviderAnthropic, parsed.StopReason)
	}
	return b.String(), nil
}

// anthropicErrorMessage best-effort parses the Anthropic error envelope
// {"type":"error","error":{"type":...,"message":...}}, falling back to a
// truncated raw body. Deliberately separate from extractErrorMessage, which
// is shaped around the OpenAI envelope.
func anthropicErrorMessage(body []byte) string {
	var parsed anthropicResponse
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != nil {
		return parsed.Error.Message
	}
	return truncateRawBody(body, 200)
}
