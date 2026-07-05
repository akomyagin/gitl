package gitlog

import (
	"reflect"
	"testing"
	"time"
)

// header builds a pretty-format record: fields joined by 0x1F, terminated by 0x1E.
func header(hash, author, date, subject, body string) string {
	return hash + "\x1f" + author + "\x1f" + date + "\x1f" + subject + "\x1f" + body + "\x1e"
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test date %q: %v", s, err)
	}
	return d
}

func TestParseLog(t *testing.T) {
	t.Parallel()

	const (
		hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		dateA = "2026-07-01T10:00:00+03:00"
		dateB = "2026-06-30T09:30:00+03:00"
	)

	tests := []struct {
		name string
		in   string
		want []Commit
	}{
		{
			name: "empty output",
			in:   "",
			want: nil,
		},
		{
			name: "normal commit with modified file",
			in: header(hashA, "Alice", dateA, "fix: handle empty range", "") +
				"\nM\tinternal/gitlog/parser.go\n",
			want: []Commit{{
				Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
				Subject: "fix: handle empty range",
				Files:   []FileChange{{Status: "M", Path: "internal/gitlog/parser.go"}},
			}},
		},
		{
			name: "multi-line body is preserved (no split on newline)",
			in: header(hashA, "Alice", dateA, "feat: add parser",
				"First body line.\n\nSecond paragraph with\ttabs and spaces.\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n") +
				"\nA\tinternal/gitlog/parser.go\n",
			want: []Commit{{
				Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
				Subject: "feat: add parser",
				Body:    "First body line.\n\nSecond paragraph with\ttabs and spaces.\n\nCo-Authored-By: Claude <noreply@anthropic.com>",
				Files:   []FileChange{{Status: "A", Path: "internal/gitlog/parser.go"}},
			}},
		},
		{
			name: "rename with similarity score",
			in: header(hashA, "Alice", dateA, "refactor: move file", "") +
				"\nR100\told/path.go\tnew/path.go\n",
			want: []Commit{{
				Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
				Subject: "refactor: move file",
				Files:   []FileChange{{Status: "R100", Old: "old/path.go", Path: "new/path.go"}},
			}},
		},
		{
			name: "delete and add in one commit",
			in: header(hashA, "Alice", dateA, "chore: swap files", "") +
				"\nD\tlegacy.go\nA\tshiny.go\n",
			want: []Commit{{
				Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
				Subject: "chore: swap files",
				Files: []FileChange{
					{Status: "D", Path: "legacy.go"},
					{Status: "A", Path: "shiny.go"},
				},
			}},
		},
		{
			name: "multiple commits: name-status block belongs to the previous record",
			in: header(hashA, "Alice", dateA, "feat: second commit", "Body of second.\n") +
				"\nM\tREADME.md\nA\tdocs/PLAN.md\n\n" +
				header(hashB, "Bob", dateB, "feat: first commit", "") +
				"\nA\tREADME.md\n",
			want: []Commit{
				{
					Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
					Subject: "feat: second commit", Body: "Body of second.",
					Files: []FileChange{
						{Status: "M", Path: "README.md"},
						{Status: "A", Path: "docs/PLAN.md"},
					},
				},
				{
					Hash: hashB, Author: "Bob", Date: mustDate(t, dateB),
					Subject: "feat: first commit",
					Files:   []FileChange{{Status: "A", Path: "README.md"}},
				},
			},
		},
		{
			name: "commit without file changes (e.g. merge) between commits",
			in: header(hashA, "Alice", dateA, "Merge branch 'x'", "") +
				"\n" +
				header(hashB, "Bob", dateB, "feat: real work", "") +
				"\nM\tmain.go\n",
			want: []Commit{
				{
					Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
					Subject: "Merge branch 'x'",
				},
				{
					Hash: hashB, Author: "Bob", Date: mustDate(t, dateB),
					Subject: "feat: real work",
					Files:   []FileChange{{Status: "M", Path: "main.go"}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLog(tt.in)
			if err != nil {
				t.Fatalf("ParseLog() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseLog() mismatch:\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func TestParseLogErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "no record separator", in: "just some noise"},
		{name: "too few fields", in: "aaaa\x1fAlice\x1e"},
		{name: "bad date", in: header("aaaa", "Alice", "yesterday", "subj", "")},
		{
			name: "malformed rename line",
			in: header("aaaa", "Alice", "2026-07-01T10:00:00+03:00", "subj", "") +
				"\nR100\tonly-one-path\n",
		},
		{
			name: "malformed name-status line",
			in: header("aaaa", "Alice", "2026-07-01T10:00:00+03:00", "subj", "") +
				"\nM\ta\tb\tc\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseLog(tt.in); err == nil {
				t.Errorf("ParseLog(%q) expected error, got nil", tt.in)
			}
		})
	}
}
