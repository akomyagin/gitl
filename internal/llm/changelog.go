package llm

import (
	"encoding/json"
	"strings"
)

// ChangelogItem is one AI-rewritten changelog line: prose subject plus the
// short hash(es) of the commit(s) it covers — the model may merge closely
// related commits into a single entry.
type ChangelogItem struct {
	Subject string   `json:"subject"`
	Hashes  []string `json:"hashes"`
}

// ChangelogPayload is the structured changelog the model emits inside its
// trailing ```changelog fenced block — the changelog analogue of the review
// ```risk contract (ParseRisk).
type ChangelogPayload struct {
	// Categories maps Keep a Changelog category names to entries. The prompt
	// tells the model to include only non-empty categories.
	Categories map[string][]ChangelogItem `json:"categories"`
	// Breaking lists breaking changes; may be empty or omitted.
	Breaking []ChangelogItem `json:"breaking"`
}

// ParseChangelogResponse extracts the model's structured changelog from a
// response body. It looks for the LAST fenced block tagged `changelog`,
// unmarshals it, and requires the "categories" object to be present. ok is
// false when no valid block exists — the caller then falls back to the
// deterministic changelog (warn, never fail), mirroring the ParseRisk /
// HeuristicRisk pattern. Only ```changelog blocks are accepted — a generic
// ```json block is never treated as a changelog payload.
func ParseChangelogResponse(content string) (payload ChangelogPayload, ok bool) {
	matches := fencedBlockRe.FindAllStringSubmatchIndex(content, -1)
	// Iterate from the last block backward so the LAST valid block wins.
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		lang := strings.ToLower(strings.TrimSpace(content[m[2]:m[3]]))
		if lang != "changelog" {
			continue
		}
		body := strings.TrimSpace(content[m[4]:m[5]])

		var p ChangelogPayload
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			continue
		}
		if p.Categories == nil {
			// The schema mandates a categories object; a payload without one
			// is malformed even if it is syntactically valid JSON.
			continue
		}
		return p, true
	}
	return ChangelogPayload{}, false
}
