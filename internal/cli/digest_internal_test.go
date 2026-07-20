package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// TestBuildDigestArtifactUntilIsDeterministic is a regression test: the
// displayed Until of a multi-repo digest must always equal generatedAt (the
// single time capture in runDigest), never the per-repo processing-start
// timestamps that the worker pool records in RepoResult.Until. Previously the
// artifact's Until was overwritten by the Until of whichever successful repo
// came last in the results slice — a non-deterministic time.Now() value.
func TestBuildDigestArtifactUntilIsDeterministic(t *testing.T) {
	generatedAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	since := generatedAt.AddDate(0, 0, -7)

	okA := gitlog.RepoResult{
		Path:   "/repo/a",
		Until:  generatedAt.Add(-time.Hour),
		Digest: gitlog.Digest{Commits: 3},
	}
	okB := gitlog.RepoResult{
		Path:   "/repo/b",
		Until:  generatedAt.Add(-30 * time.Minute),
		Digest: gitlog.Digest{Commits: 5},
	}
	failed := gitlog.RepoResult{
		Path:  "/repo/broken",
		Until: generatedAt.Add(-15 * time.Minute),
		Err:   errors.New("not a git repository"),
	}

	orderings := map[string][]gitlog.RepoResult{
		"ok-ok-err": {okA, okB, failed},
		"err-ok-ok": {failed, okA, okB},
		"ok-err-ok": {okB, failed, okA},
	}

	for name, results := range orderings {
		art := buildDigestArtifact(generatedAt, 7, since, results)
		if !art.Until.Equal(generatedAt) {
			t.Errorf("%s: Until = %v, want generatedAt %v", name, art.Until, generatedAt)
		}
		if !art.GeneratedAt.Equal(generatedAt) {
			t.Errorf("%s: GeneratedAt = %v, want %v", name, art.GeneratedAt, generatedAt)
		}
		if len(art.Repos) != len(results) {
			t.Errorf("%s: got %d repos, want %d", name, len(art.Repos), len(results))
		}
	}
}
