package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runDigestInDir chdirs into dir and runs `gitl digest` with the given args,
// restoring cwd afterward.
func runDigestInDir(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	empty := filepath.Join(t.TempDir(), "none.yaml")
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	full := append([]string{"digest", "--config", empty}, args...)
	root.SetArgs(full)
	err = root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// setupDigestRepo builds a repo with one commit right now (within any
// reasonable --days window) touching one file.
func setupDigestRepo(t *testing.T, subject string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", subject)
	return dir
}

func TestDigestSingleRepoDefaultWindow(t *testing.T) {
	dir := setupDigestRepo(t, "feat: add main")
	out, _, err := runDigestInDir(t, dir)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !strings.Contains(out, "# Digest — last 7 days") {
		t.Errorf("expected default 7-day single-repo digest heading:\n%s", out)
	}
	if !strings.Contains(out, "Commits: 1") {
		t.Errorf("expected 1 commit counted:\n%s", out)
	}
	if !strings.Contains(out, "| feat | 1 |") {
		t.Errorf("expected feat topic row:\n%s", out)
	}
}

func TestDigestDaysFlag(t *testing.T) {
	dir := setupDigestRepo(t, "feat: recent")
	out, _, err := runDigestInDir(t, dir, "--days=30")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !strings.Contains(out, "last 30 days") {
		t.Errorf("expected 30-day window in heading:\n%s", out)
	}
}

func TestDigestInvalidDaysRejected(t *testing.T) {
	dir := setupDigestRepo(t, "feat: x")
	_, _, err := runDigestInDir(t, dir, "--days=0")
	if err == nil {
		t.Fatal("expected --days=0 to be rejected")
	}
	if !strings.Contains(err.Error(), "--days must be a positive integer") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDigestNeverCallsLLM(t *testing.T) {
	dir := setupDigestRepo(t, "feat: x")
	t.Setenv("GITL_API_KEY", "")
	_, _, err := runDigestInDir(t, dir)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
}

// TestDigestMultiRepoFlagGracefulDegradation is the stage's literal
// acceptance criterion: digest across multiple repos aggregates correctly
// and does not crash if one repo is unavailable.
func TestDigestMultiRepoFlagGracefulDegradation(t *testing.T) {
	repoA := setupDigestRepo(t, "feat: a")
	repoBad := filepath.Join(t.TempDir(), "does-not-exist")
	repoC := setupDigestRepo(t, "fix: c")

	cwd := t.TempDir()
	reposArg := strings.Join([]string{repoA, repoBad, repoC}, ",")
	out, _, err := runDigestInDir(t, cwd, "--repos="+reposArg, "--format=json")
	if err != nil {
		t.Fatalf("multi-repo digest must not fail when one repo is bad: %v", err)
	}
	if !strings.Contains(out, `"repos_requested": 3`) {
		t.Errorf("expected repos_requested=3:\n%s", out)
	}
	if !strings.Contains(out, `"repos_ok": 2`) {
		t.Errorf("expected repos_ok=2:\n%s", out)
	}
	if !strings.Contains(out, `"repos_failed": 1`) {
		t.Errorf("expected repos_failed=1:\n%s", out)
	}
	if !strings.Contains(out, `"ok": false`) {
		t.Errorf("expected an ok:false repo entry:\n%s", out)
	}
}

func TestDigestReposFlagOverridesConfigEntirely(t *testing.T) {
	repoFromFlag := setupDigestRepo(t, "feat: from-flag")
	repoFromConfig := setupDigestRepo(t, "feat: from-config")

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".gitl.yaml"), []byte(
		"digest:\n  repos:\n    - path: \""+filepath.ToSlash(repoFromConfig)+"\"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := runDigestInDir(t, cwd, "--repos="+repoFromFlag, "--format=json")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !strings.Contains(out, repoJSONPath(repoFromFlag)) {
		t.Errorf("expected the --repos path in output:\n%s", out)
	}
	if strings.Contains(out, repoJSONPath(repoFromConfig)) {
		t.Errorf("--repos must fully replace digest.repos, but config path leaked into output:\n%s", out)
	}
}

func TestDigestUsesConfigRepposWhenNoFlag(t *testing.T) {
	repoFromConfig := setupDigestRepo(t, "feat: from-config")

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".gitl.yaml"), []byte(
		"digest:\n  repos:\n    - path: \""+filepath.ToSlash(repoFromConfig)+"\"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := runDigestInDir(t, cwd, "--format=json")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !strings.Contains(out, repoJSONPath(repoFromConfig)) {
		t.Errorf("expected digest.repos path from config in output:\n%s", out)
	}
}

// repoJSONPath renders path as it would appear inside the JSON "path" field
// (Go's json encoder escapes backslashes, irrelevant on POSIX but harmless).
func repoJSONPath(path string) string {
	return path
}

// TestDigestTUIFallbackNonTerminal verifies that with --tui set but stdout not
// a terminal (a bytes.Buffer via the test harness), runDigest falls back to
// plain rendered output and warns on stderr — it must not attempt to launch the
// interactive TUI (which would hang without a TTY).
func TestDigestTUIFallbackNonTerminal(t *testing.T) {
	dir := setupDigestRepo(t, "feat: tui fallback")
	out, errOut, err := runDigestInDir(t, dir, "--tui")
	if err != nil {
		t.Fatalf("digest --tui must not fail without a terminal: %v", err)
	}
	if !strings.Contains(out, "# Digest — last 7 days") {
		t.Errorf("expected plain digest output on fallback:\n%s", out)
	}
	if !strings.Contains(errOut, "--tui requires a terminal") {
		t.Errorf("expected fallback warning on stderr, got:\n%s", errOut)
	}
}

func TestDigestEmptyWindowIsNotAnError(t *testing.T) {
	dir := setupDigestRepo(t, "feat: old")
	// Move the commit far in the past by rewriting author/committer dates,
	// then request a short window that excludes it.
	past := time.Now().Add(-365 * 24 * time.Hour).Format(time.RFC3339)
	cmd := exec.Command("git", "-c", "commit.gpgsign=false", "commit", "--amend", "--no-edit", "--date="+past)
	cmd.Dir = dir
	cmd.Env = append(gitEnv(), "GIT_COMMITTER_DATE="+past)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit --amend: %v\n%s", err, out)
	}

	out, _, err := runDigestInDir(t, dir, "--days=1")
	if err != nil {
		t.Fatalf("digest with zero commits in window must not fail: %v", err)
	}
	if !strings.Contains(out, "Commits: 0") {
		t.Errorf("expected zero commits in a too-short window:\n%s", out)
	}
}
