package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a hand-written net/http client for an OpenAI-compatible
// chat/completions endpoint. It supports three providers — openai, ollama and
// azure_openai — that differ only in endpoint URL and auth header; the request
// and response JSON shape is identical across all three, so there is a single
// wire format. Requests are retried with exponential backoff + jitter on
// retryable failures (429/5xx/network), bounded by maxRetries; fatal failures
// (400/401/403/404) fail immediately.
type Client struct {
	httpClient *http.Client
	provider   string
	baseURL    string
	apiKey     string
	azure      AzureConfig
	maxRetries int
}

var (
	_ Provider     = (*Client)(nil)
	_ RawCompleter = (*Client)(nil)
)

// AzureConfig holds the Azure OpenAI-specific endpoint coordinates, required
// only when provider == "azure_openai".
type AzureConfig struct {
	Endpoint   string
	Deployment string
	APIVersion string
}

// ClientConfig configures a Client. It mirrors the config.LLMConfig fields the
// network path needs, without importing internal/config (avoids a cycle and
// keeps llm reusable).
type ClientConfig struct {
	Provider   string
	BaseURL    string
	APIKey     string
	Timeout    time.Duration
	MaxRetries int
	Azure      AzureConfig
}

// Supported provider identifiers.
const (
	ProviderOpenAI = "openai"
	ProviderOllama = "ollama"
	ProviderAzure  = "azure_openai"
)

// StatusError is a typed HTTP error carrying the status code and whether the
// request is worth retrying. It is used with errors.As so retry classification
// never relies on string matching.
type StatusError struct {
	StatusCode int
	Retryable  bool
	Provider   string
	Message    string
}

func (e *StatusError) Error() string {
	// Never echo the API key: only status, provider and the provider's own
	// message are surfaced.
	return fmt.Sprintf("llm: %s API returned status %d: %s", e.Provider, e.StatusCode, e.Message)
}

// classifyStatus maps an HTTP status to a StatusError, marking 429 and 5xx as
// retryable and 4xx (other than 429) as fatal.
func classifyStatus(provider string, code int, message string) *StatusError {
	retryable := code == http.StatusTooManyRequests || code >= 500
	return &StatusError{StatusCode: code, Retryable: retryable, Provider: provider, Message: message}
}

// NewClient constructs a Client for one of the three supported providers.
// An unknown provider is a real configuration error. Azure requires its
// endpoint/deployment/api_version sub-block.
func NewClient(cfg ClientConfig) (*Client, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	retries := cfg.MaxRetries
	if retries < 0 {
		retries = 0
	}

	c := &Client{
		httpClient: &http.Client{Timeout: timeout},
		provider:   cfg.Provider,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		azure:      cfg.Azure,
		maxRetries: retries,
	}

	switch cfg.Provider {
	case ProviderOpenAI, ProviderOllama:
		// base_url is required; both use {base_url}/chat/completions.
		if c.baseURL == "" {
			return nil, fmt.Errorf("llm: provider %q requires llm.base_url", cfg.Provider)
		}
	case ProviderAzure:
		if cfg.Azure.Endpoint == "" || cfg.Azure.Deployment == "" || cfg.Azure.APIVersion == "" {
			return nil, fmt.Errorf("llm: provider %q requires llm.azure_openai.{endpoint,deployment,api_version}", cfg.Provider)
		}
	default:
		return nil, fmt.Errorf("llm: unsupported provider %q (supported: %q, %q, %q)",
			cfg.Provider, ProviderOpenAI, ProviderOllama, ProviderAzure)
	}
	return c, nil
}

// chatRequest / chatResponse mirror the OpenAI chat/completions wire format,
// which Ollama's /v1 endpoint and Azure OpenAI both reuse.
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

// endpointURL returns the request URL for the configured provider.
func (c *Client) endpointURL() (string, error) {
	switch c.provider {
	case ProviderOpenAI, ProviderOllama:
		return c.baseURL + "/chat/completions", nil
	case ProviderAzure:
		endpoint := strings.TrimRight(c.azure.Endpoint, "/")
		u := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=%s",
			endpoint, url.PathEscape(c.azure.Deployment), url.QueryEscape(c.azure.APIVersion))
		return u, nil
	default:
		return "", fmt.Errorf("llm: unsupported provider %q", c.provider)
	}
}

// setAuth applies the provider-specific auth header. Ollama sends none (no API
// key by default); openai uses Bearer; Azure uses the api-key header.
func (c *Client) setAuth(h http.Header) {
	switch c.provider {
	case ProviderOpenAI:
		h.Set("Authorization", "Bearer "+c.apiKey)
	case ProviderAzure:
		h.Set("api-key", c.apiKey)
	case ProviderOllama:
		// no auth header, even if an api_key happens to be set
	}
}

// buildRequest constructs the HTTP request for body. It is separated from
// Complete so each provider's URL/headers/body are unit-testable in isolation.
func (c *Client) buildRequest(ctx context.Context, body []byte) (*http.Request, error) {
	endpoint, err := c.endpointURL()
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setAuth(httpReq.Header)
	return httpReq, nil
}

// marshalChatRequest builds the wire body shared by Complete and CompleteRaw.
func marshalChatRequest(req Request) ([]byte, error) {
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
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}
	return body, nil
}

// Complete sends a chat/completions request (with retry/backoff) and returns the
// assistant text plus a risk score. If the model's trailing risk block is
// missing or invalid, the deterministic heuristic (§7.3) supplies the risk and
// a warning is logged. The context (carrying the configured timeout and Ctrl-C
// cancellation) is threaded through the HTTP call and the retry sleeps.
func (c *Client) Complete(ctx context.Context, req Request) (Response, error) {
	body, err := marshalChatRequest(req)
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

// CompleteRaw sends a chat/completions request with the same retry/backoff as
// Complete and returns the assistant text verbatim — no risk-block parsing, no
// heuristic fallback. It exists for prompts whose response contract is not the
// review risk block (e.g. changelog --ai's ```changelog payload): running
// ParseRisk over such a response would log a spurious "risk block missing"
// warning and compute a meaningless heuristic score. It implements the
// RawCompleter capability interface (the same scheme Streamer already uses):
// callers probe for it with a type assertion rather than depending on the
// concrete *Client type, and the base Provider interface stays untouched —
// the Offline provider deliberately does not implement it (callers fall back
// to deterministic output before selecting a provider at all).
func (c *Client) CompleteRaw(ctx context.Context, req Request) (string, error) {
	body, err := marshalChatRequest(req)
	if err != nil {
		return "", err
	}
	return c.doWithRetry(ctx, body)
}

// doWithRetry wraps retry with the doOnce round-trip closure (backoff/jitter
// details are documented on retry itself).
func (c *Client) doWithRetry(ctx context.Context, body []byte) (string, error) {
	return retry(ctx, c.maxRetries, func(ctx context.Context) (string, error) {
		return c.doOnce(ctx, body)
	})
}

// retry runs once — a single request/response round trip — up to 1+maxRetries
// times with exponential backoff + jitter between attempts, retrying only
// failures classified retryable by isRetryable (429/5xx/network). Backoff
// sleeps respect ctx cancellation so Ctrl-C / timeout interrupts a pending
// retry immediately. It is a package-level function, parameterized by the
// round-trip closure, so any provider implementation can reuse the one
// backoff loop instead of duplicating it.
func retry(ctx context.Context, maxRetries int, once func(context.Context) (string, error)) (string, error) {
	var lastErr error
	// Attempts: 1 initial + maxRetries retries.
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepWithBackoff(ctx, attempt); err != nil {
				return "", err
			}
			slog.Debug("retrying LLM request", "attempt", attempt, "max_retries", maxRetries)
		}

		content, err := once(ctx)
		if err == nil {
			return content, nil
		}
		lastErr = err

		// Context cancellation is never retryable.
		if ctx.Err() != nil {
			return "", err
		}
		if !isRetryable(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("llm: giving up after %d attempt(s): %w", maxRetries+1, lastErr)
}

// doOnce performs a single request/response round trip.
func (c *Client) doOnce(ctx context.Context, body []byte) (string, error) {
	httpReq, err := c.buildRequest(ctx, body)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// Network/transport errors are retryable (unless the context is done).
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
		return "", classifyStatus(c.provider, resp.StatusCode, extractErrorMessage(respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("llm: %s API error: %s", c.provider, parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm: %s API returned no choices", c.provider)
	}
	return parsed.Choices[0].Message.Content, nil
}

// networkError marks a transport-level failure as retryable.
type networkError struct{ err error }

func (e *networkError) Error() string { return "llm: request failed: " + e.err.Error() }
func (e *networkError) Unwrap() error { return e.err }

// isRetryable reports whether err is worth retrying: retryable HTTP statuses or
// transport-level network errors.
func isRetryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Retryable
	}
	var ne *networkError
	return errors.As(err, &ne)
}

// backoffBase is the base delay for exponential backoff; attempt N sleeps
// roughly backoffBase * 2^(N-1) plus jitter, capped at backoffMax.
const (
	backoffBase      = 200 * time.Millisecond
	backoffMax       = 10 * time.Second
	maxResponseBytes = 8 << 20 // 8 MiB cap on LLM response bodies
)

// sleepWithBackoff sleeps for an exponentially-growing, jittered duration
// before the given retry attempt (1-based). It returns early with the context
// error if ctx is cancelled during the wait — never a bare time.Sleep.
func sleepWithBackoff(ctx context.Context, attempt int) error {
	d := backoffBase << (attempt - 1)
	if d > backoffMax || d <= 0 {
		d = backoffMax
	}
	// Full jitter in [d/2, d].
	jittered := d/2 + time.Duration(rand.Int63n(int64(d/2)+1)) //nolint:gosec // jitter, not security-sensitive
	timer := time.NewTimer(jittered)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("llm: retry cancelled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
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
