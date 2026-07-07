package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/gitlog"
)

func sampleReview() Review {
	date := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	return Review{
		Range: "HEAD~1..HEAD",
		Commits: []gitlog.Commit{{
			Hash: "abcdef1234567", Author: "Alice", Date: date,
			Subject: "feat: thing", Body: "line1\nline2",
			Files: []gitlog.FileChange{
				{Status: "M", Path: "a.go"},
				{Status: "R100", Old: "old.go", Path: "new.go"},
			},
		}},
		Diff: "--- a/a.go\n+++ b/a.go\n+added\n",
	}
}

func TestBuildReview(t *testing.T) {
	t.Parallel()
	system, user := BuildReview(sampleReview())

	if !strings.Contains(system, "code review") {
		t.Errorf("system prompt missing reviewer instruction: %q", system)
	}
	for _, want := range []string{
		"HEAD~1..HEAD",
		"feat: thing",
		"abcdef1", // short hash (7 chars)
		"> line1", // body line quoted
		"> line2", // multi-line body preserved
		"M a.go",
		"R100 old.go -> new.go", // rename shows both paths
		"```diff",
		"+added",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q\n---\n%s", want, user)
		}
	}
}

func TestBuildReviewDeterministic(t *testing.T) {
	t.Parallel()
	system1, user1 := BuildReview(sampleReview())
	system2, user2 := BuildReview(sampleReview())
	if system1 != system2 || user1 != user2 {
		t.Error("BuildReview is not deterministic")
	}
}

func TestBuildReviewEmpty(t *testing.T) {
	t.Parallel()
	_, user := BuildReview(Review{Range: "x..y"})
	if !strings.Contains(user, "no commits in range") {
		t.Errorf("expected empty-commits notice, got:\n%s", user)
	}
	if !strings.Contains(user, "empty diff") {
		t.Errorf("expected empty-diff notice, got:\n%s", user)
	}
}

// TestBuildReviewWithTemplateEmpty: an empty path yields output identical to
// BuildReview (embedded default system prompt, same user message).
func TestBuildReviewWithTemplateEmpty(t *testing.T) {
	t.Parallel()
	wantSystem, wantUser := BuildReview(sampleReview())
	system, user, err := BuildReviewWithTemplate(sampleReview(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if system != wantSystem {
		t.Errorf("system differs from BuildReview:\ngot:  %q\nwant: %q", system, wantSystem)
	}
	if user != wantUser {
		t.Errorf("user differs from BuildReview:\ngot:  %q\nwant: %q", user, wantUser)
	}
}

// TestBuildReviewWithTemplateCustom: a custom template file has its {{.Range}}
// placeholder substituted, and the user message is still the standard one.
func TestBuildReviewWithTemplateCustom(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "system.tmpl")
	if err := os.WriteFile(path, []byte("Custom reviewer for range {{.Range}}."), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	system, user, err := BuildReviewWithTemplate(sampleReview(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "Custom reviewer for range HEAD~1..HEAD."; system != want {
		t.Errorf("system not rendered:\ngot:  %q\nwant: %q", system, want)
	}
	if !strings.Contains(user, "feat: thing") {
		t.Errorf("user message not built as usual:\n%s", user)
	}
}

// TestBuildReviewWithTemplateMissing: a non-existent path returns an error from
// template.ParseFiles (config.validate() catches this earlier in normal flow).
func TestBuildReviewWithTemplateMissing(t *testing.T) {
	t.Parallel()
	_, _, err := BuildReviewWithTemplate(sampleReview(), filepath.Join(t.TempDir(), "nope.tmpl"))
	if err == nil {
		t.Fatal("expected error for missing template file, got nil")
	}
}
