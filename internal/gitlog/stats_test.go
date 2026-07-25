package gitlog

import (
	"reflect"
	"testing"
)

func TestDiffPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		diff string
		want []string
	}{
		{
			name: "empty diff",
			diff: "",
			want: nil,
		},
		{
			name: "no diff --git header",
			diff: "--- a/x\n+++ b/x\n+line\n",
			want: nil,
		},
		{
			name: "single file",
			diff: "diff --git a/internal/auth/token.go b/internal/auth/token.go\n--- a/internal/auth/token.go\n+++ b/internal/auth/token.go\n+x\n",
			want: []string{"internal/auth/token.go"},
		},
		{
			name: "multiple files",
			diff: "diff --git a/a.go b/a.go\n+x\n" + "diff --git a/b.go b/b.go\n+y\n",
			want: []string{"a.go", "b.go"},
		},
		{
			name: "rename uses b-side path",
			diff: "diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n",
			want: []string{"new.go"},
		},
		{
			name: "mid-line content occurrence is not a header",
			diff: "diff --git a/doc.md b/doc.md\n@@ -1 +1,2 @@\n+example: diff --git a/fake.go b/fake.go\n",
			want: []string{"doc.md"},
		},
		{
			name: "quoted non-ASCII path (core.quotepath default)",
			diff: `diff --git "a/\320\264\320\260\320\275\320\275\321\213\320\265.lock" "b/\320\264\320\260\320\275\320\275\321\213\320\265.lock"` + "\n+x\n",
			want: []string{"данные.lock"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DiffPaths(tc.diff)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DiffPaths(%q) = %v, want %v", tc.diff, got, tc.want)
			}
		})
	}
}

func TestDiffLineStats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		diff        string
		wantAdded   int
		wantRemoved int
	}{
		{
			name: "empty diff",
			diff: "", wantAdded: 0, wantRemoved: 0,
		},
		{
			name: "basic hunk counts content, not file headers",
			diff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
				"@@ -1,2 +1,2 @@\n context\n+added\n-removed\n",
			wantAdded: 1, wantRemoved: 1,
		},
		{
			name: "content lines starting with literal +++/--- inside a hunk are counted",
			diff: "diff --git a/main.c b/main.c\n--- a/main.c\n+++ b/main.c\n" +
				"@@ -1,2 +1,2 @@\n" +
				"+++i;\n" + // added C line, NOT a file header
				"---\n" + // removed YAML separator, NOT a file header
				"+plain added\n",
			wantAdded: 2, wantRemoved: 1,
		},
		{
			name: "no-newline marker is not counted either way",
			diff: "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n" +
				"@@ -1 +1 @@\n-old\n+new\n\\ No newline at end of file\n",
			wantAdded: 1, wantRemoved: 1,
		},
		{
			name: "second file section resets to header state",
			diff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n+x\n" +
				"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-y\n",
			wantAdded: 1, wantRemoved: 1,
		},
		{
			name:      "lines outside any hunk are never counted",
			diff:      "+stray added\n-stray removed\n",
			wantAdded: 0, wantRemoved: 0,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			added, removed := DiffLineStats(tc.diff)
			if added != tc.wantAdded || removed != tc.wantRemoved {
				t.Errorf("DiffLineStats(%q) = (+%d, -%d), want (+%d, -%d)",
					tc.diff, added, removed, tc.wantAdded, tc.wantRemoved)
			}
		})
	}
}
