// Package textsafe centralizes the terminal-output character policy shared by
// the render and llm packages: which runes must never reach a terminal-facing
// writer when the text is attacker-influenced (commit subjects/bodies, PR
// titles, LLM output quoting them). It is a leaf package — it imports nothing
// from this module — so both render and llm can depend on it without creating
// a render↔llm cross-package dependency.
package textsafe

// DropRune reports whether r must be removed before attacker-influenced text
// reaches a terminal:
//
//   - C0 controls (0x00–0x1F) except '\t' and '\n' — ANSI/terminal escape
//     injection (ESC, BEL, lone CR, …);
//   - DEL (0x7F);
//   - C1 controls (0x80–0x9F) — 8-bit CSI/OSC/ST equivalents;
//   - Unicode bidi embeddings/overrides U+202A–U+202E and bidi isolates
//     U+2066–U+2069 — the "Trojan Source" visual-spoofing vector
//     (CVE-2021-42574 class): a single U+202E can visually reorder or hide
//     text on a bidi-aware terminal.
//
// Removal — not replacement — is the callers' policy: it avoids injecting
// stray artifacts and keeps table alignment intact.
func DropRune(r rune) bool {
	if r == '\t' || r == '\n' {
		return false
	}
	switch {
	case r < 0x20: // C0 controls (includes lone '\r')
		return true
	case r == 0x7F: // DEL
		return true
	case r >= 0x80 && r <= 0x9F: // C1 controls
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings/overrides: LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates: LRI, RLI, FSI, PDI
		return true
	}
	return false
}
