// Package llm talks to an OpenAI-compatible chat/completions API via a
// hand-written net/http client (intentionally without an SDK — see
// docs/TECHNICAL_PLAN.md §2), and provides a deterministic offline provider
// used when no API key is configured.
//
// All implementations satisfy the Provider interface, which is justified here
// because there are genuinely multiple implementations (project rule: "the
// interface appears at the second implementation, not the first"). The three
// OpenAI-shaped wire providers — openai, ollama, azure_openai — are branches
// inside the one Client type (different endpoint/auth shapes, same
// request/response JSON). Anthropic (AnthropicClient) and Google Gemini
// (GeminiClient) are separate Provider implementations: their wire formats
// differ fundamentally from chat/completions, so branching inside Client would
// have meant per-branch request/response types anyway.
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
// Heuristic is true when the score was computed by the deterministic fallback
// (offline mode, or the model omitted a valid risk block) rather than parsed
// from the model's own fenced risk block.
type Risk struct {
	Level     string
	Summary   string
	Heuristic bool
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

// RawCompleter is an optional capability for providers that can return the
// assistant text verbatim, without the review risk-block parsing that Complete
// performs. It mirrors Streamer: an optional capability probed via a type
// assertion (provider.(llm.RawCompleter)), not part of the base Provider
// interface. changelog --ai uses it because ParseRisk over a ```changelog
// payload would log a spurious "risk block missing" warning and compute a
// meaningless heuristic. The Offline provider deliberately does NOT implement
// it — changelog falls back to the deterministic path before provider selection.
type RawCompleter interface {
	CompleteRaw(ctx context.Context, req Request) (string, error)
}
