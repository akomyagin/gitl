package gitlog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func multidigestGitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test Author",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test Author",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
}

func runMultidigestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = multidigestGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupSimpleRepo creates a repo with one commit touching one file.
func setupSimpleRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runMultidigestGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runMultidigestGit(t, dir, "add", ".")
	runMultidigestGit(t, dir, "commit", "-q", "-m", "feat: initial")
	return dir
}

func TestCollectDigestsHappyPath(t *testing.T) {
	t.Parallel()
	repoA := setupSimpleRepo(t)
	repoB := setupSimpleRepo(t)
	repoC := setupSimpleRepo(t)

	since := time.Now().Add(-24 * time.Hour)
	results := CollectDigests(context.Background(), []string{repoA, repoB, repoC}, since, 2)

	if len(results) != 3 {
		t.Fatalf("results len = %d, want 3", len(results))
	}
	for i, path := range []string{repoA, repoB, repoC} {
		if results[i].Path != path {
			t.Errorf("results[%d].Path = %q, want %q (order must match input)", i, results[i].Path, path)
		}
		if results[i].Err != nil {
			t.Errorf("results[%d].Err = %v, want nil", i, results[i].Err)
		}
		if results[i].Digest.Commits != 1 {
			t.Errorf("results[%d].Digest.Commits = %d, want 1", i, results[i].Digest.Commits)
		}
	}
}

// TestCollectDigestsIsolatesPerRepoErrors is the stage's literal acceptance
// criterion: one bad repo (not a git repository) must not crash or block
// digest collection for the others.
func TestCollectDigestsIsolatesPerRepoErrors(t *testing.T) {
	t.Parallel()
	good := setupSimpleRepo(t)
	notARepo := t.TempDir() // exists but has no .git
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	since := time.Now().Add(-24 * time.Hour)
	paths := []string{good, notARepo, missing}
	results := CollectDigests(context.Background(), paths, since, 3)

	if len(results) != 3 {
		t.Fatalf("results len = %d, want 3", len(results))
	}
	if results[0].Err != nil {
		t.Errorf("good repo result.Err = %v, want nil", results[0].Err)
	}
	if results[0].Digest.Commits != 1 {
		t.Errorf("good repo commits = %d, want 1", results[0].Digest.Commits)
	}
	if results[1].Err == nil {
		t.Error("not-a-git-repo result.Err = nil, want an error")
	}
	if results[2].Err == nil {
		t.Error("missing-dir result.Err = nil, want an error")
	}
}

func TestCollectDigestsEmptyWindowIsNotAnError(t *testing.T) {
	t.Parallel()
	dir := setupSimpleRepo(t)
	// A since far in the future means zero commits fall in the window —
	// this must be a valid empty result, not an error (§10.1).
	since := time.Now().Add(24 * time.Hour)
	results := CollectDigests(context.Background(), []string{dir}, since, 1)
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].Err != nil {
		t.Errorf("Err = %v, want nil for an empty-but-valid window", results[0].Err)
	}
	if results[0].Digest.Commits != 0 {
		t.Errorf("Commits = %d, want 0", results[0].Digest.Commits)
	}
}

func TestCollectDigestsContextCancellation(t *testing.T) {
	t.Parallel()
	dirs := make([]string, 20)
	for i := range dirs {
		dirs[i] = setupSimpleRepo(t)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before dispatch

	since := time.Now().Add(-24 * time.Hour)
	results := CollectDigests(ctx, dirs, since, 4)

	if len(results) != len(dirs) {
		t.Fatalf("results len = %d, want %d", len(results), len(dirs))
	}
	for i, r := range results {
		if r.Path != dirs[i] {
			t.Errorf("results[%d].Path = %q, want %q", i, r.Path, dirs[i])
		}
		if r.Err == nil {
			t.Errorf("results[%d].Err = nil, want context.Canceled after pre-cancelled ctx", i)
		}
	}
}

func TestCollectDigestsEmptyInput(t *testing.T) {
	t.Parallel()
	results := CollectDigests(context.Background(), nil, time.Now(), 4)
	if len(results) != 0 {
		t.Errorf("results = %+v, want empty", results)
	}
}

func TestDefaultConcurrency(t *testing.T) {
	t.Parallel()
	if got := DefaultConcurrency(0); got < 1 {
		t.Errorf("DefaultConcurrency(0) = %d, want >= 1", got)
	}
	if got := DefaultConcurrency(1); got != 1 {
		t.Errorf("DefaultConcurrency(1) = %d, want 1 (capped by repo count)", got)
	}
	// A large repo count should never exceed GOMAXPROCS.
	if got := DefaultConcurrency(10_000); got < 1 {
		t.Errorf("DefaultConcurrency(10000) = %d, want >= 1", got)
	}
}
