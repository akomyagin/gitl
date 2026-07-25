package gitlog

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitDiffSections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		diff         string
		wantPreamble string
		wantSections []string
	}{
		{
			name:         "empty diff",
			diff:         "",
			wantPreamble: "",
			wantSections: nil,
		},
		{
			name:         "no header at all",
			diff:         "--- a/x\n+++ b/x\n+line\n",
			wantPreamble: "--- a/x\n+++ b/x\n+line\n",
			wantSections: nil,
		},
		{
			name:         "header at position 0",
			diff:         "diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1 +1 @@\n+x\n",
			wantPreamble: "",
			wantSections: []string{"diff --git a/a.go b/a.go\n+++ b/a.go\n@@ -1 +1 @@\n+x\n"},
		},
		{
			name:         "non-empty preamble before first header",
			diff:         "some preamble\ndiff --git a/a.go b/a.go\n+x\n",
			wantPreamble: "some preamble\n",
			wantSections: []string{"diff --git a/a.go b/a.go\n+x\n"},
		},
		{
			name: "multiple sections",
			diff: "diff --git a/a.go b/a.go\n+x\n" +
				"diff --git a/b.go b/b.go\n+y\n",
			wantPreamble: "",
			wantSections: []string{
				"diff --git a/a.go b/a.go\n+x\n",
				"diff --git a/b.go b/b.go\n+y\n",
			},
		},
		{
			name: "mid-line occurrence inside content is NOT a boundary",
			diff: "diff --git a/doc.md b/doc.md\n@@ -1 +1,2 @@\n" +
				"+here is an example: diff --git a/x b/y\n" +
				"diff --git a/real.go b/real.go\n+z\n",
			wantPreamble: "",
			wantSections: []string{
				"diff --git a/doc.md b/doc.md\n@@ -1 +1,2 @@\n+here is an example: diff --git a/x b/y\n",
				"diff --git a/real.go b/real.go\n+z\n",
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			preamble, sections := SplitDiffSections(tc.diff)
			if preamble != tc.wantPreamble {
				t.Errorf("preamble = %q, want %q", preamble, tc.wantPreamble)
			}
			if !reflect.DeepEqual(sections, tc.wantSections) {
				t.Errorf("sections = %q, want %q", sections, tc.wantSections)
			}
			// Re-join invariant: preamble + concat(sections) reconstructs the
			// input byte for byte, so filtering can keep sections verbatim.
			if rejoined := preamble + strings.Join(sections, ""); rejoined != tc.diff {
				t.Errorf("rejoin mismatch:\ngot  %q\nwant %q", rejoined, tc.diff)
			}
		})
	}
}

func TestDiffSectionPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
		want    string
	}{
		{
			name:    "plain path",
			section: "diff --git a/internal/auth/token.go b/internal/auth/token.go\n+x\n",
			want:    "internal/auth/token.go",
		},
		{
			name:    "path with spaces (unquoted — git does not quote plain spaces)",
			section: "diff --git a/my file.txt b/my file.txt\n+x\n",
			want:    "my file.txt",
		},
		{
			name:    "rename extracts the b-side path",
			section: "diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n",
			want:    "new.go",
		},
		{
			name: "quoted path with octal escapes (core.quotepath default, Cyrillic filename)",
			// "данные" in UTF-8: d0 b4 d0 b0 d0 bd d0 bd d1 8b d0 b5 →
			// octal \320\264 \320\260 \320\275 \320\275 \321\213 \320\265.
			section: `diff --git "a/\320\264\320\260\320\275\320\275\321\213\320\265.lock" "b/\320\264\320\260\320\275\320\275\321\213\320\265.lock"` + "\n+x\n",
			want:    "данные.lock",
		},
		{
			name:    "quoted path with escaped quote and backslash in the filename",
			section: `diff --git "a/we\"ird\\name.txt" "b/we\"ird\\name.txt"` + "\n+x\n",
			want:    `we"ird\name.txt`,
		},
		{
			name:    "quoted path with tab escape",
			section: `diff --git "a/tab\there.txt" "b/tab\there.txt"` + "\n+x\n",
			want:    "tab\there.txt",
		},
		{
			name:    "unquoted a-side with quoted b-side (rename to non-ASCII)",
			section: `diff --git a/old.txt "b/\320\264.txt"` + "\n+x\n",
			want:    "д.txt",
		},
		{
			name:    "header only, no trailing newline",
			section: "diff --git a/a.go b/a.go",
			want:    "a.go",
		},
		{
			name:    "not a section header",
			section: "+content line\n",
			want:    "",
		},
		{
			name:    "malformed header without b-side",
			section: "diff --git nonsense\n",
			want:    "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DiffSectionPath(tc.section); got != tc.want {
				t.Errorf("DiffSectionPath(%q) = %q, want %q", tc.section, got, tc.want)
			}
		})
	}
}

func TestUnquoteGitPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no escapes", "plain.txt", "plain.txt"},
		{"escaped quote", `we\"ird.txt`, `we"ird.txt`},
		{"escaped backslash", `back\\slash.txt`, `back\slash.txt`},
		{"tab and newline", `a\tb\nc`, "a\tb\nc"},
		{"octal bytes decode to UTF-8", `\320\264.txt`, "д.txt"},
		{"incomplete octal passes through", `\32`, `\32`},
		{"unknown escape passes through", `\q`, `\q`},
		{"trailing lone backslash passes through", `x\`, `x\`},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := unquoteGitPath(tc.in); got != tc.want {
				t.Errorf("unquoteGitPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDiffFileCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		diff string
		want int
	}{
		{"empty", "", 0},
		{"single file", "diff --git a/a.go b/a.go\n+x\n", 1},
		{"two files", "diff --git a/a.go b/a.go\n+x\ndiff --git a/b.go b/b.go\n+y\n", 2},
		{
			name: "mid-line content occurrence not counted",
			diff: "diff --git a/a.go b/a.go\n@@ -1 +1,2 @@\n+see: diff --git a/x b/y\n",
			want: 1,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DiffFileCount(tc.diff); got != tc.want {
				t.Errorf("DiffFileCount(%q) = %d, want %d", tc.diff, got, tc.want)
			}
		})
	}
}
