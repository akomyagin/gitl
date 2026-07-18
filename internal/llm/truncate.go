package llm

import (
	"strings"
	"unicode/utf8"
)

// truncateRawBody trims and rune-safely truncates a raw response body for a
// fallback error message, so a multi-byte UTF-8 character is never cut in
// half at the truncation boundary.
//
// Used by anthropicErrorMessage and geminiErrorMessage. TODO: client.go's
// extractErrorMessage has the same byte-index truncation pattern and could
// reuse this helper — left untouched for now as pre-existing code outside the
// native-providers change.
func truncateRawBody(body []byte, max int) string {
	s := strings.TrimSpace(string(body))
	if len(s) <= max {
		return s
	}
	// Back off from max until we're not mid-rune.
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "..."
}
