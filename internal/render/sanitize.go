package render

import "strings"

// sanitizeTerminal removes terminal control characters from attacker-influenced
// text (commit subjects/authors, PR titles, LLM output quoting them) before it
// reaches a terminal-facing writer, defusing ANSI/terminal escape injection
// (hidden text, terminal title changes, etc.).
//
// Policy: CRLF is first normalized to LF, then every C0 control rune
// (0x00–0x1F) except '\t' and '\n', DEL (0x7F), and every C1 control rune
// (0x80–0x9F) is removed. Printable ASCII, '\t', '\n', and all Unicode from
// U+00A0 up (Cyrillic, emoji, box drawing, …) pass through unchanged. Removal
// — not replacement with a placeholder — is deliberate: it avoids injecting
// stray spaces/artifacts and keeps table alignment intact.
func sanitizeTerminal(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n':
			b.WriteRune(r)
		case r < 0x20: // C0 controls (includes any remaining lone '\r')
			// drop
		case r == 0x7F: // DEL
			// drop
		case r >= 0x80 && r <= 0x9F: // C1 controls
			// drop
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
