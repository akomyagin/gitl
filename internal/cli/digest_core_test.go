package cli

// Direct unit tests for RunDigestCore — the cmd-free digest entrypoint. They
// deliberately never construct a cobra.Command or a pflag.FlagSet: proving the
// core is callable from outside the CLI layer (a future MCP server) is the
// whole point of the extraction. digest never calls an LLM, so the tests only
// need real (temp) git repositories.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akomyagin/gitl/internal/config"
)

func TestRunDigestCoreExplicitRepos(t *testing.T) {
	dir := setupDigestRepo(t, "feat: core digest")
	cfg := coreTestConfig(t)

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7, Repos: []string{dir}})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}

	if art.Days != 7 {
		t.Errorf("Days = %d, want 7", art.Days)
	}
	if !art.Until.Equal(art.GeneratedAt) {
		t.Errorf("Until = %v, want GeneratedAt %v", art.Until, art.GeneratedAt)
	}
	if len(art.Repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(art.Repos))
	}
	repo := art.Repos[0]
	if !repo.Ok {
		t.Fatalf("repo not Ok: %s", repo.Err)
	}
	if repo.Path != dir {
		t.Errorf("Path = %q, want %q", repo.Path, dir)
	}
	if repo.Commits != 1 {
		t.Errorf("Commits = %d, want 1", repo.Commits)
	}
	if len(repo.ByTopic) != 1 || repo.ByTopic[0].Topic != "feat" {
		t.Errorf("ByTopic = %+v, want a single feat topic", repo.ByTopic)
	}
}

func TestRunDigestCoreInvalidDays(t *testing.T) {
	cfg := coreTestConfig(t)

	_, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 0})
	if err == nil {
		t.Fatal("expected Days=0 to be rejected")
	}
	if !strings.Contains(err.Error(), "--days must be a positive integer") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestRunDigestCoreMissingRepoIsPerRepoError: a repository that does not exist
// must surface as a per-repo failure inside the artifact, not abort the whole
// digest (graceful degradation, §10.4).
func TestRunDigestCoreMissingRepoIsPerRepoError(t *testing.T) {
	good := setupDigestRepo(t, "fix: still works")
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg := coreTestConfig(t)

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7, Repos: []string{good, missing}})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}
	if len(art.Repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(art.Repos))
	}

	byPath := map[string]bool{}
	for _, r := range art.Repos {
		byPath[r.Path] = r.Ok
		if !r.Ok && r.Err == "" {
			t.Errorf("failed repo %q carries no error message", r.Path)
		}
	}
	if !byPath[good] {
		t.Errorf("repo %q should have succeeded", good)
	}
	if byPath[missing] {
		t.Errorf("repo %q should have failed", missing)
	}
}

// TestRunDigestCoreConfigReposFallback: with DigestOptions.Repos empty the core
// must fall back to cfg digest.repos — the same precedence the CLI has
// (--repos > digest.repos > current directory).
func TestRunDigestCoreConfigReposFallback(t *testing.T) {
	dir := setupDigestRepo(t, "feat: from config")
	cfg := coreTestConfig(t)
	cfg.Digest.Repos = []config.RepoRef{{Path: dir}}

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}
	if len(art.Repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(art.Repos))
	}
	if art.Repos[0].Path != dir {
		t.Errorf("Path = %q, want the digest.repos entry %q", art.Repos[0].Path, dir)
	}
	if !art.Repos[0].Ok || art.Repos[0].Commits != 1 {
		t.Errorf("repo = %+v, want Ok with 1 commit", art.Repos[0])
	}
}
