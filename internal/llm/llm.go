// Package llm talks to an OpenAI-compatible chat/completions API via a
// hand-written net/http client (intentionally without an SDK — see
// docs/TECHNICAL_PLAN.md §2), and provides a deterministic offline provider
// used when no API key is configured.
//
// Both implementations satisfy the Provider interface, which is justified here
// because there are genuinely two implementations (project rule: "the interface
// appears at the second implementation, not the first").
//
// Этап 1 is deliberately minimal: a single request/response round trip, no
// retry/backoff, and only the "openai" provider. Retry with backoff+jitter,
// typed retryable-vs-fatal errors, Ollama/Azure OpenAI branching, and
// SSE streaming all land in Этап 2 (see docs/TECHNICAL_PLAN.md §6).
package llm

import "context"

// Request is a provider-agnostic completion request.
type Request struct {
	// System is the system prompt (role=system); may be empty.
	System string
	// User is the user prompt (role=user).
	User string
	// Model, MaxTokens and Temperature come from config; the offline provider
	// ignores them.
	Model       string
	MaxTokens   int
	Temperature float64
}

// Response is a provider-agnostic completion response.
type Response struct {
	// Content is the assistant message text (Markdown for review).
	Content string
}

// Provider produces a completion for a Request. Implemented by both the
// network Client and the deterministic Offline provider.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}
