package textsafe

import "testing"

func TestDropRune(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		r    rune
		want bool
	}{
		// Preserved whitespace.
		{"tab kept", '\t', false},
		{"newline kept", '\n', false},

		// C0 controls (except \t/\n) dropped.
		{"NUL dropped", 0x00, true},
		{"BEL dropped", 0x07, true},
		{"ESC dropped", 0x1B, true},
		{"lone CR dropped", '\r', true},
		{"US dropped", 0x1F, true},

		// DEL and C1 controls dropped.
		{"DEL dropped", 0x7F, true},
		{"C1 low bound 0x80 dropped", 0x80, true},
		{"C1 CSI U+009B dropped", 0x9B, true},
		{"C1 high bound 0x9F dropped", 0x9F, true},
		{"NBSP U+00A0 kept (just past C1)", 0xA0, false},

		// Bidi embeddings/overrides (Trojan Source).
		{"LRE U+202A dropped", 0x202A, true},
		{"RLE U+202B dropped", 0x202B, true},
		{"PDF U+202C dropped", 0x202C, true},
		{"LRO U+202D dropped", 0x202D, true},
		{"RLO U+202E dropped", 0x202E, true},
		{"U+2029 kept (just before bidi range)", 0x2029, false},
		{"U+202F kept (just past bidi range)", 0x202F, false},

		// Bidi isolates (Trojan Source).
		{"LRI U+2066 dropped", 0x2066, true},
		{"RLI U+2067 dropped", 0x2067, true},
		{"FSI U+2068 dropped", 0x2068, true},
		{"PDI U+2069 dropped", 0x2069, true},
		{"U+2065 kept (just before isolate range)", 0x2065, false},
		{"U+206A kept (just past isolate range)", 0x206A, false},

		// Ordinary content passes.
		{"ascii letter kept", 'a', false},
		{"space kept", ' ', false},
		{"cyrillic kept", 'ф', false},
		{"emoji kept", '✅', false},
		{"box drawing kept", '─', false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DropRune(tt.r); got != tt.want {
				t.Errorf("DropRune(%#U) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}
