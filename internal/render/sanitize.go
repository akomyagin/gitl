package render

import (
	"strings"

	"github.com/akomyagin/gitl/internal/textsafe"
)

// sanitizeTerminal removes terminal control characters from attacker-influenced
// text (commit subjects/authors, PR titles, LLM output quoting them) before it
// reaches a terminal-facing writer, defusing ANSI/terminal escape injection
// (hidden text, terminal title changes, etc.) and bidi "Trojan Source"
// visual spoofing.
//
// Policy: CRLF is first normalized to LF, then every rune in the shared
// textsafe.DropRune removal set — C0 controls (0x00–0x1F) except '\t'/'\n',
// DEL (0x7F), C1 controls (0x80–0x9F), and Unicode bidi override/isolate
// characters (U+202A–U+202E, U+2066–U+2069) — is removed. Printable ASCII,
// '\t', '\n', and all other Unicode (Cyrillic, emoji, box drawing, …) pass
// through unchanged. The predicate lives in the leaf package textsafe so the
// llm package's streaming sanitizer applies the exact same policy without a
// render↔llm cross-package dependency.
func sanitizeTerminal(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if textsafe.DropRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
