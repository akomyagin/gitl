package prompt

import (
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
