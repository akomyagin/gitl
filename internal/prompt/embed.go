package prompt

import _ "embed"

// defaultReviewSystem is the embedded default review system prompt. It is
// byte-identical to the historical reviewSystem string. Custom user templates
// (text/template) override it via BuildReviewWithTemplate (Item 3).
//
//go:embed templates/review_system.tmpl
var defaultReviewSystem string

// defaultChangelogSystem is the embedded default system prompt for
// `changelog --ai`. It instructs the model to rewrite the deterministic
// grouping as release-note prose and to end with a fenced ```changelog JSON
// block (parsed back in internal/llm — the changelog analogue of the review
// risk-block contract).
//
//go:embed templates/changelog_system.tmpl
var defaultChangelogSystem string
