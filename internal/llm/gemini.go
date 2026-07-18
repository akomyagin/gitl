package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProviderGemini selects the native Google Gemini client (GeminiClient), a
// separate Provider implementation from the OpenAI-compatible Client: the
// wire format nests messages as contents[].parts[], the system prompt is a
// dedicated systemInstruction object, and auth travels as a ?key= query
// parameter instead of a header.
const ProviderGemini = "gemini"

// defaultGeminiBaseURL targets Google AI Studio
// (generativelanguage.googleapis.com), NOT Vertex AI: AI Studio uses a simple
// ?key= API-key auth that fits a single-user BYOK CLI, whereas Vertex would
// require a GCP service-account / ADC flow — overkill here. Tests override it
// with an httptest server URL.
const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// GeminiClient is a hand-written net/http client for the Google AI Studio
// generateContent endpoint (POST {base}/models/{model}:generateContent?key=…).
// It reuses the package-level retry/backoff loop and error classification
// shared with the other providers (429/5xx retryable, other 4xx fatal).
//
// Because the API key rides in the URL query string, every error path that
// could echo a URL (transport errors wrap *url.Error, which stringifies the
// full URL) is redacted before surfacing — the key must never appear in error
// messages or logs.
type GeminiClient struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	maxRetries int
}

var (
	_ Provider     = (*GeminiClient)(nil)
	_ RawCompleter = (*GeminiClient)(nil)
)

// NewGeminiClient constructs a GeminiClient. The API key is mandatory
// (AI Studio has no keyless mode); base_url is optional and defaults to the
// public v1beta endpoint.
func NewGeminiClient(cfg ClientConfig) (*GeminiClient, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("llm: provider %q requires llm.api_key (or env GITL_API_KEY)", ProviderGemini)
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
		baseURL = defaultGeminiBaseURL
	}
	return &GeminiClient{
		httpClient: &http.Client{Timeout: timeout},
		baseURL:    baseURL,
		apiKey:     cfg.APIKey,
		maxRetries: retries,
	}, nil
}

// geminiPart / geminiContent / geminiRequest / geminiResponse mirror the
// Google AI Studio generateContent wire format (per the public docs at
// ai.google.dev/api/generate-content as of 2026-07).
type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"` // "user" / "model"
	Parts []geminiPart `json:"parts"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	// Temperature has NO omitempty: 0 is a meaningful, explicitly configurable
	// value ("fully deterministic output") and must reach the wire instead of
	// silently falling back to the provider default. MaxOutputTokens keeps
	// omitempty — 0 there genuinely means "not set" (Gemini has no meaningful
	// maxOutputTokens: 0).
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason,omitempty"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason,omitempty"`
	} `json:"promptFeedback,omitempty"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// marshalGeminiRequest builds the wire body shared by Complete and
// CompleteRaw. The system prompt goes into systemInstruction (omitted
// entirely when empty), the user prompt into a single user-role content.
func marshalGeminiRequest(req Request) ([]byte, error) {
	wire := geminiRequest{
		Contents: []geminiContent{{Role: "user", Parts: []geminiPart{{Text: req.User}}}},
	}
	if req.System != "" {
		wire.SystemInstruction = &geminiSystemInstruction{Parts: []geminiPart{{Text: req.System}}}
	}
	// Always build generationConfig so temperature (0 included) is always
	// serialized — a conditional here would silently drop an explicit
	// temperature: 0 when max_tokens happens to be unset too.
	wire.GenerationConfig = &geminiGenerationConfig{
		Temperature:     req.Temperature,
		MaxOutputTokens: req.MaxTokens,
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal request: %w", err)
	}
	return body, nil
}

// Complete sends a generateContent request (with retry/backoff) and returns
// the model text plus a risk score, with the same risk-block/heuristic
// contract as Client.Complete (§7.2, §7.3).
func (c *GeminiClient) Complete(ctx context.Context, req Request) (Response, error) {
	body, err := marshalGeminiRequest(req)
	if err != nil {
		return Response{}, err
	}
	content, err := c.doWithRetry(ctx, req.Model, body)
	if err != nil {
		return Response{}, err
	}
	return finishWithRisk(content, req.Commits, req.Diff), nil
}

// CompleteRaw returns the model text verbatim — no risk-block parsing —
// implementing the RawCompleter capability (used by changelog --ai).
func (c *GeminiClient) CompleteRaw(ctx context.Context, req Request) (string, error) {
	body, err := marshalGeminiRequest(req)
	if err != nil {
		return "", err
	}
	return c.doWithRetry(ctx, req.Model, body)
}

// doWithRetry wraps the shared package-level retry loop around doOnce.
func (c *GeminiClient) doWithRetry(ctx context.Context, model string, body []byte) (string, error) {
	return retry(ctx, c.maxRetries, func(ctx context.Context) (string, error) {
		return c.doOnce(ctx, model, body)
	})
}

// doOnce performs a single request/response round trip against
// /models/{model}:generateContent. Auth is the ?key= query parameter only —
// no auth header.
func (c *GeminiClient) doOnce(ctx context.Context, model string, body []byte) (string, error) {
	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s",
		c.baseURL, url.PathEscape(model), url.QueryEscape(c.apiKey))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// The endpoint (with the key) may be embedded in the error — redact.
		return "", fmt.Errorf("llm: build request: %w", redactGeminiURLError(err))
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// redactGeminiURLError is passed so a transport-level *url.Error (which
	// stringifies the full ?key= URL) is redacted before it is wrapped as a
	// retryable networkError — keeping the key out of logs while preserving
	// retry classification.
	respBody, err := doHTTPRoundTrip(ctx, httpReq, c.httpClient, ProviderGemini, geminiErrorMessage, redactGeminiURLError)
	if err != nil {
		return "", err
	}

	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("llm: %s API error: %s (status=%s)", ProviderGemini, parsed.Error.Message, parsed.Error.Status)
	}

	// Safety-blocked prompts come back as HTTP 200 with empty candidates and
	// promptFeedback.blockReason — an explicit, non-retryable error, never a
	// nil-index panic.
	if len(parsed.Candidates) == 0 {
		blockReason := ""
		if parsed.PromptFeedback != nil {
			blockReason = parsed.PromptFeedback.BlockReason
		}
		return "", fmt.Errorf("llm: %s API returned no candidates (blockReason=%q)", ProviderGemini, blockReason)
	}

	// Concatenate all parts of the first candidate — long replies may arrive
	// split across multiple text parts.
	var b strings.Builder
	for _, part := range parsed.Candidates[0].Content.Parts {
		b.WriteString(part.Text)
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("llm: %s API returned an empty candidate (finishReason=%q)", ProviderGemini, parsed.Candidates[0].FinishReason)
	}
	return b.String(), nil
}

// geminiErrorMessage best-effort parses the Google API error envelope
// {"error":{"code":…,"message":…,"status":…}}, falling back to a truncated
// raw body.
func geminiErrorMessage(body []byte) string {
	var parsed geminiResponse
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != nil {
		if parsed.Error.Status != "" {
			return fmt.Sprintf("%s (status=%s)", parsed.Error.Message, parsed.Error.Status)
		}
		return parsed.Error.Message
	}
	return truncateRawBody(body, 200)
}

// redactGeminiURLError strips the query string (which carries the API key)
// from any *url.Error found in err's chain, so transport-level failures never
// leak the key into error messages or logs. Non-URL errors pass through
// unchanged.
func redactGeminiURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	redacted := ue.URL
	if i := strings.IndexByte(redacted, '?'); i >= 0 {
		redacted = redacted[:i] + "?key=REDACTED"
	}
	return &url.Error{Op: ue.Op, URL: redacted, Err: ue.Err}
}
