package suggest

import "testing"

func TestDistance(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"both empty", "", "", 0},
		{"one empty", "a", "", 1},
		{"other empty", "", "abc", 3},
		{"identical", "json", "json", 0},
		{"jsn vs json", "jsn", "json", 1},
		{"hgih vs high", "hgih", "high", 2},
		{"opneai vs openai", "opneai", "openai", 2},
		{"unrelated", "xyz", "md", 3},
		{"multibyte runes", "héllo", "hello", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Distance(tt.a, tt.b); got != tt.want {
				t.Errorf("Distance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			if got := Distance(tt.b, tt.a); got != tt.want {
				t.Errorf("Distance(%q, %q) = %d, want %d (symmetry)", tt.b, tt.a, got, tt.want)
			}
		})
	}
}

func TestClosest(t *testing.T) {
	formats := []string{"md", "text", "json"}
	failOn := []string{"never", "low", "medium", "high"}
	providers := []string{"openai", "ollama", "azure_openai", "anthropic", "gemini"}

	tests := []struct {
		name       string
		input      string
		candidates []string
		wantSug    string
		wantOK     bool
	}{
		// Positive: plausible typos get a suggestion.
		{"jsn to json", "jsn", formats, "json", true},
		{"jso to json", "jso", formats, "json", true},
		{"tex to text", "tex", formats, "text", true},
		{"hgih to high", "hgih", failOn, "high", true},
		{"mediium to medium", "mediium", failOn, "medium", true},
		{"opneai to openai", "opneai", providers, "openai", true},
		{"anthropc to anthropic", "anthropc", providers, "anthropic", true},
		// Exact match trivially wins (callers only reach Closest on the error
		// path, so a valid value never gets here in practice — documented).
		{"exact match", "json", formats, "json", true},

		// Negative: garbage or too-distant input gets no suggestion.
		{"garbage vs fail-on", "xyz123", failOn, "", false},
		{"abc vs formats d>=len", "abc", formats, "", false},
		{"empty input", "", formats, "", false},
		{"empty candidates", "jsn", nil, "", false},
		{"unrelated qwerty vs providers", "qwerty", providers, "", false},
		// gemma→gemini has distance 3 > max(1, 5/2)=2, so no suggestion.
		{"gemma too far from gemini", "gemma", providers, "", false},
		// Closest assumes lowercased input (callers lowercase first): an
		// uppercase typo does not match lowercase candidates.
		{"uppercase input not matched", "JSN", formats, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sug, ok := Closest(tt.input, tt.candidates)
			if sug != tt.wantSug || ok != tt.wantOK {
				t.Errorf("Closest(%q, %v) = (%q, %v), want (%q, %v)",
					tt.input, tt.candidates, sug, ok, tt.wantSug, tt.wantOK)
			}
		})
	}
}

// TestClosestTieBreaksFirstInOrder pins the tie-breaking contract: on equal
// distance, the first candidate in slice order wins.
func TestClosestTieBreaksFirstInOrder(t *testing.T) {
	// "lot" is distance 1 from both "low" and "lot"-like "loz".
	sug, ok := Closest("lot", []string{"low", "loz"})
	if !ok || sug != "low" {
		t.Errorf("Closest(\"lot\", [low loz]) = (%q, %v), want (\"low\", true)", sug, ok)
	}
	// Reversed order flips the winner.
	sug, ok = Closest("lot", []string{"loz", "low"})
	if !ok || sug != "loz" {
		t.Errorf("Closest(\"lot\", [loz low]) = (%q, %v), want (\"loz\", true)", sug, ok)
	}
}
