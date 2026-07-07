package prompt

import _ "embed"

// defaultReviewSystem is the embedded default review system prompt. It is
// byte-identical to the historical reviewSystem string. Custom user templates
// (text/template) override it via BuildReviewWithTemplate (Item 3).
//
//go:embed templates/review_system.tmpl
var defaultReviewSystem string
