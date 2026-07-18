package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateRawBody(t *testing.T) {
	t.Parallel()

	// 199 ASCII bytes, then a 2-byte Cyrillic "ж" spanning byte indexes
	// 199–200: byte 200 lands mid-rune, forcing the boundary back-off.
	midRune := strings.Repeat("a", 199) + "ж" + strings.Repeat("б", 50)

	tests := []struct {
		name string
		body string
		max  int
		want string
	}{
		{
			name: "short ASCII passes through untruncated",
			body: "  bad gateway  ",
			max:  200,
			want: "bad gateway",
		},
		{
			name: "exactly max is not truncated",
			body: strings.Repeat("a", 200),
			max:  200,
			want: strings.Repeat("a", 200),
		},
		{
			name: "long ASCII truncated exactly at max",
			body: strings.Repeat("a", 300),
			max:  200,
			want: strings.Repeat("a", 200) + "...",
		},
		{
			name: "multi-byte rune straddling max is not cut in half",
			body: midRune,
			max:  200,
			// Backs off past the ж's first byte to 199 clean ASCII bytes.
			want: strings.Repeat("a", 199) + "...",
		},
		{
			name: "cyrillic-only body truncates on a rune boundary",
			body: strings.Repeat("ж", 150), // 300 bytes, every odd index is mid-rune
			max:  151,
			want: strings.Repeat("ж", 75) + "...", // 150 bytes: 151 is mid-rune, back off to 150
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateRawBody([]byte(tt.body), tt.max)
			if got != tt.want {
				t.Errorf("truncateRawBody() = %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateRawBody() produced invalid UTF-8: %q", got)
			}
			if len(got) > tt.max+len("...") {
				t.Errorf("truncateRawBody() length %d exceeds max %d plus ellipsis", len(got), tt.max)
			}
		})
	}
}
