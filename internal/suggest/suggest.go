// Package suggest offers a minimal "did you mean" nearest-match helper for
// enum-like CLI flag/config values. It is a leaf package — it imports nothing
// from this module (mirroring internal/textsafe) — so config, llm and render
// can all depend on it without creating cross-package cycles.
package suggest

import "unicode/utf8"

// Closest returns the candidate with the smallest Levenshtein distance to
// input, but only when that distance is "close enough" to be a plausible typo:
// distance <= max(1, runes(input)/2) AND distance < runes(input) (a suggestion
// must share more than it differs). Callers are expected to lowercase input
// first (as config validation already does) — comparison is case-sensitive.
// Returns ("", false) when input is empty, candidates is empty, or no
// candidate clears the threshold. On ties, the first candidate in slice order
// wins (callers pass candidates in canonical/help-text order).
func Closest(input string, candidates []string) (string, bool) {
	n := utf8.RuneCountInString(input)
	if n == 0 {
		return "", false
	}
	best := ""
	bestD := -1
	for _, cand := range candidates {
		d := Distance(input, cand)
		if bestD == -1 || d < bestD {
			best, bestD = cand, d
		}
	}
	if bestD == -1 || bestD > max(1, n/2) || bestD >= n {
		return "", false
	}
	return best, true
}

// Distance returns the Levenshtein edit distance between a and b (insertions,
// deletions, substitutions each cost 1). Operates on runes, so multibyte
// input is measured per character, not per byte. Exported for testing.
func Distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	// Two-row dynamic programming: prev is row i, curr is row i+1.
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ca := range ra {
		curr[0] = i + 1
		for j, cb := range rb {
			cost := 1
			if ca == cb {
				cost = 0
			}
			curr[j+1] = min(prev[j]+cost, prev[j+1]+1, curr[j]+1)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}
