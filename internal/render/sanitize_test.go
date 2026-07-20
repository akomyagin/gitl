package render

import (
	"strings"
	"testing"
)

// assertNoControlBytes fails the test if s contains any rune from the removal
// set: C0 controls other than '\t'/'\n' (which includes '\r'), DEL, or C1.
func assertNoControlBytes(t *testing.T, s string) {
	t.Helper()
	for i, r := range s {
		if r == '\t' || r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F) {
			t.Errorf("control rune %#U survived at index %d in %q", r, i, s)
		}
	}
}

func TestSanitizeTerminal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "esc csi removed",
			in:   "\x1b[31mred\x1b[0m",
			want: "[31mred[0m",
		},
		{
			name: "osc and bel removed",
			in:   "a\x1b]0;title\x07b",
			want: "a]0;titleb",
		},
		{
			name: "cyrillic and emoji untouched",
			in:   "фикс: добавил ✅ проверку",
			want: "фикс: добавил ✅ проверку",
		},
		{
			name: "tabs and newlines preserved",
			in:   "line1\nline2\tcol",
			want: "line1\nline2\tcol",
		},
		{
			name: "crlf to lf and lone cr removed",
			in:   "a\r\nb\rc",
			want: "a\nbc",
		},
		{
			// C1 as a rune (U+009C = ST, string terminator) in valid UTF-8 —
			// per policy C1 is a rune range, not a byte range: a lone raw
			// 0x9C byte is invalid UTF-8 and decodes to U+FFFD instead, which
			// is harmless for a terminal and passes through.
			name: "del and c1 removed",
			in:   "a\x7fb\u009cc",
			want: "abc",
		},
		{
			name: "hidden text attack",
			in:   "feat: normal\x1b[8mHIDDEN\x1b[0m",
			want: "feat: normal[8mHIDDEN[0m",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTerminal(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeTerminal(%q) = %q, want %q", tt.in, got, tt.want)
			}
			assertNoControlBytes(t, got)
			if strings.ContainsRune(got, 0x1b) {
				t.Errorf("output still contains ESC: %q", got)
			}
		})
	}
}
