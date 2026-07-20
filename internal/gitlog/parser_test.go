package gitlog

import (
	"reflect"
	"testing"
	"time"
)

// header builds a pretty-format record: a single NUL after each of the five
// fields (including the last, %b) — exactly what real git emits with
// --pretty=format:%H%x00%an%x00%aI%x00%s%x00%b%x00. Empty fields contribute
// their NUL like any other, so every record adds exactly 5 NULs.
func header(hash, author, date, subject, body string) string {
	return hash + "\x00" + author + "\x00" + date + "\x00" + subject + "\x00" + body + "\x00"
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
		// A well-formed output always splits into 5*N+1 NUL-separated tokens;
		// 3 tokens -> (3-1)%5 != 0 -> "malformed output".
		{name: "malformed token count", in: "aaaa\x00Alice\x00"},
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

// TestParseLogTreatsOldSeparatorsAsBodyText is a regression test for the
// separator-injection vulnerability: the previous format used \x1f/\x1e as
// delimiters, but git happily stores those bytes verbatim in a commit message
// (via `git commit -F`), letting a single hostile commit split into bogus
// records or hard-fail the parser. With NUL separators these bytes are just
// body text.
func TestParseLogTreatsOldSeparatorsAsBodyText(t *testing.T) {
	t.Parallel()

	const (
		hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		dateA = "2026-07-01T10:00:00+03:00"
	)
	body := "line one\x1erecord-injection\x1ffield-injection\nline two"

	in := header(hashA, "Alice", dateA, "feat: attack subject", body) +
		"\nM\tmain.go\n"

	got, err := ParseLog(in)
	if err != nil {
		t.Fatalf("ParseLog() error = %v", err)
	}
	want := []Commit{{
		Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
		Subject: "feat: attack subject",
		Body:    body,
		Files:   []FileChange{{Status: "M", Path: "main.go"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLog() mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// TestParseLogHandlesEmptySubjectAndBody is a regression test for the
// empty-message bug in the previous (2-NUL-terminator, regex-run-split)
// design: a commit with BOTH subject and body empty — a legitimate
// `git commit --allow-empty-message -m ""` — produced 4 adjacent NULs after
// the date, which the \x00{2,} record-separator regex greedily swallowed as
// one boundary, losing two fields and failing the whole range with a parse
// error. The flat 5*N+1 token scheme has no such collapse: empty fields are
// just empty strings in their positions.
func TestParseLogHandlesEmptySubjectAndBody(t *testing.T) {
	t.Parallel()

	const (
		hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // newest
		hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // empty-message commit
		hashC = "cccccccccccccccccccccccccccccccccccccccc" // oldest
		dateA = "2026-07-20T13:00:00+03:00"
		dateB = "2026-07-20T12:30:00+03:00"
		dateC = "2026-07-20T12:00:00+03:00"
	)

	in := header(hashA, "Alice", dateA, "feat: commit three", "") +
		"\nA\tc.txt\n\n" +
		header(hashB, "Bob", dateB, "", "") + // empty subject AND body
		"\nA\tb.txt\n\n" +
		header(hashC, "Carol", dateC, "feat: commit one", "body one\n") +
		"\nA\ta.txt\n"

	got, err := ParseLog(in)
	if err != nil {
		t.Fatalf("ParseLog() error = %v", err)
	}
	want := []Commit{
		{
			Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
			Subject: "feat: commit three",
			Files:   []FileChange{{Status: "A", Path: "c.txt"}},
		},
		{
			Hash: hashB, Author: "Bob", Date: mustDate(t, dateB),
			Subject: "", Body: "",
			Files: []FileChange{{Status: "A", Path: "b.txt"}},
		},
		{
			Hash: hashC, Author: "Carol", Date: mustDate(t, dateC),
			Subject: "feat: commit one", Body: "body one",
			Files: []FileChange{{Status: "A", Path: "a.txt"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLog() mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}
