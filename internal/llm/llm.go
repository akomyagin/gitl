// Package llm talks to an OpenAI-compatible chat/completions API via a
// hand-written net/http client (intentionally without an SDK — see
// docs/TECHNICAL_PLAN.md §2), and provides a deterministic offline provider
// used when no API key is configured.
//
// Both implementations satisfy the Provider interface, which is justified here
// because there are genuinely two implementations (project rule: "the interface
// appears at the second implementation, not the first"). The three supported
// wire providers — openai, ollama, azure_openai — are branches inside the one
// Client type (different endpoint/auth shapes, same request/response JSON), not
// separate Provider implementations.
//
// SSE streaming remains out of scope and lands post-MVP (see
// docs/TECHNICAL_PLAN.md §6).
package llm

import (
	"context"

	"github.com/akomyagin/gitl/internal/gitlog"
)

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

	// Commits and Diff are pass-through context (not sent over the wire) so the
	// risk heuristic (§7.3) can be computed inside the provider without
	// re-deriving structure from the prompt text: the offline provider always
	// uses them, and the network Client uses them as a fallback when the
	// model's risk block is missing or invalid.
	Commits []gitlog.Commit
	Diff    string
}

// Risk is the structured risk score attached to a review (§7). Level is one of
// "low", "medium", "high"; Summary is a one-sentence explanation.
type Risk struct {
	Level   string
	Summary string
}

// Response is a provider-agnostic completion response.
type Response struct {
	// Content is the assistant message text (Markdown for review), with any
	// trailing risk block already stripped (§7.2).
	Content string
	// Risk is always populated: the model supplies it, or the heuristic
	// fallback does (§7.2, §7.3). Consumers never need to handle an empty risk.
	Risk Risk
}

// Provider produces a completion for a Request. Implemented by both the
// network Client and the deterministic Offline provider.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}
