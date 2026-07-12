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
