package gitlog

import (
	"reflect"
	"testing"
)

// TestParseLogEdgeCases covers boundary inputs that the happy-path table in
// parser_test.go does not exercise: binary files in --name-status, renames,
// truly empty logs, bodyless commits, and multi-line bodies.
func TestParseLogEdgeCases(t *testing.T) {
	t.Parallel()

	const (
		hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		dateA = "2026-07-01T10:00:00+03:00"
	)

	tests := []struct {
		name string
		in   string
		want []Commit
	}{
		{
			// git --name-status marks a binary change with a normal status
			// letter and a single path ("M\timage.png"); the "B" spelling in
			// the task is just another single-path status and must not panic.
			name: "binary file in name-status",
			in: header(hashA, "Alice", dateA, "chore: add logo", "") +
				"\nB\timage.png\n",
			want: []Commit{{
				Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
				Subject: "chore: add logo",
				Files:   []FileChange{{Status: "B", Path: "image.png"}},
			}},
		},
		{
			name: "rename lands in Files with old and new path",
			in: header(hashA, "Alice", dateA, "refactor: rename", "") +
				"\nR100\told_name.go\tnew_name.go\n",
			want: []Commit{{
				Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
				Subject: "refactor: rename",
				Files:   []FileChange{{Status: "R100", Old: "old_name.go", Path: "new_name.go"}},
			}},
		},
		{
			name: "empty log yields empty slice, not an error",
			in:   "",
			want: nil,
		},
		{
			name: "commit with subject only, no body and no footer",
			in: header(hashA, "Alice", dateA, "docs: tidy", "") +
				"\nM\tREADME.md\n",
			want: []Commit{{
				Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
				Subject: "docs: tidy",
				Files:   []FileChange{{Status: "M", Path: "README.md"}},
			}},
		},
		{
			name: "multi-line body keeps all its lines in the Body field",
			in: header(hashA, "Alice", dateA, "feat: big change",
				"Line one of the body.\nLine two after a newline.\n\nAnd a trailing paragraph.\n") +
				"\nM\tmain.go\n",
			want: []Commit{{
				Hash: hashA, Author: "Alice", Date: mustDate(t, dateA),
				Subject: "feat: big change",
				Body:    "Line one of the body.\nLine two after a newline.\n\nAnd a trailing paragraph.",
				Files:   []FileChange{{Status: "M", Path: "main.go"}},
			}},
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
