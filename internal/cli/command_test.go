package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnv isolates test git invocations from user/system config.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test Author",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test Author",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupRepo builds a two-commit repo. When sensitive is true the second commit
// touches a security-sensitive path so the offline heuristic scores it "high".
func setupRepo(t *testing.T, sensitive bool) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")

	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "docs: initial")

	name := "notes.txt"
	if sensitive {
		name = "auth_token.go"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	msg := "chore: add notes"
	if sensitive {
		msg = "feat: add auth token handling"
	}
	runGit(t, dir, "commit", "-q", "-m", msg)
	return dir
}

// runReviewInDir chdirs into dir and runs `gitl review` with the given args in
// offline mode (empty personal config). It returns stdout, the run error, and
// restores cwd.
func runReviewInDir(t *testing.T, dir string, env map[string]string, args ...string) (string, error) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	for k, v := range env {
		t.Setenv(k, v)
	}
	// Point the personal config at a non-existent path so no host config leaks
	// in, and ensure no stray API key unless the test sets one.
	empty := filepath.Join(t.TempDir(), "none.yaml")

	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	full := append([]string{"review", "--config", empty}, args...)
	root.SetArgs(full)
	err = root.ExecuteContext(context.Background())
	return stdout.String(), err
}

func TestReviewOfflineFormats(t *testing.T) {
	dir := setupRepo(t, false)
	env := map[string]string{"GITL_API_KEY": ""}

	// md
	out, err := runReviewInDir(t, dir, env, "HEAD~1..HEAD", "--format=md")
	if err != nil {
		t.Fatalf("md review: %v", err)
	}
	if !strings.Contains(out, "**Risk:**") {
		t.Errorf("md output missing risk header:\n%s", out)
	}

	// json
	out, err = runReviewInDir(t, dir, env, "HEAD~1..HEAD", "--format=json")
	if err != nil {
		t.Fatalf("json review: %v", err)
	}
	if !strings.Contains(out, `"schema_version": 1`) || !strings.Contains(out, `"risk"`) {
		t.Errorf("json output malformed:\n%s", out)
	}

	// text
	out, err = runReviewInDir(t, dir, env, "HEAD~1..HEAD", "--format=text")
	if err != nil {
		t.Fatalf("text review: %v", err)
	}
	if strings.Contains(out, "**") {
		t.Errorf("text output still has markdown bold:\n%s", out)
	}
}

func TestReviewFailOnHighExitsNonZero(t *testing.T) {
	dir := setupRepo(t, true) // sensitive path → heuristic "high"
	out, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "HEAD~1..HEAD", "--fail-on=high")
	if err == nil {
		t.Fatal("expected non-zero exit for high risk with --fail-on=high")
	}
	if _, ok := err.(*failError); !ok {
		t.Errorf("expected *failError, got %T: %v", err, err)
	}
	// The review must still have been printed before failing (§9).
	if !strings.Contains(out, "**Risk:** HIGH") {
		t.Errorf("review not printed before failing:\n%s", out)
	}
}

func TestReviewFailOnNeverPasses(t *testing.T) {
	dir := setupRepo(t, true) // high risk...
	_, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "HEAD~1..HEAD", "--fail-on=never")
	if err != nil {
		t.Errorf("--fail-on=never must never fail, got: %v", err)
	}
}

// TestReviewCostGuardBlocksLargeDiff is the stage's literal acceptance
// criterion: a large synthetic diff with --max-cost-usd=0.01 must block with a
// non-zero exit and a message naming the estimate and the limit.
func TestReviewCostGuardBlocksLargeDiff(t *testing.T) {
	dir := setupRepoLargeDiff(t)
	// A fake key forces the online path; the cost guard runs BEFORE any network
	// call, so no real request is made. Raise max_diff_bytes so the full large
	// diff reaches the prompt (the default 120 KB truncation would keep the
	// estimate under $0.01).
	env := map[string]string{
		"GITL_API_KEY":             "sk-fake-not-used",
		"GITL_DIFF_MAX_DIFF_BYTES": "20000000",
	}
	out, err := runReviewInDir(t, dir, env, "HEAD~1..HEAD", "--max-cost-usd=0.01")
	if err == nil {
		t.Fatal("expected cost-guard to block a large diff at --max-cost-usd=0.01")
	}
	msg := err.Error()
	if !strings.Contains(msg, "estimated cost") || !strings.Contains(msg, "0.01") {
		t.Errorf("block message should name estimate and limit: %v", msg)
	}
	if !strings.Contains(msg, "gpt-4o-mini") {
		t.Errorf("block message should name the model: %v", msg)
	}
	if out != "" {
		t.Errorf("no review should be printed when the guard blocks, got:\n%s", out)
	}
}

// setupRepoLargeDiff builds a repo whose second commit adds a very large file,
// so the prompt is big enough for the built-in gpt-4o-mini pricing to exceed a
// $0.01 estimate. Uses a non-excluded extension (.txt).
func setupRepoLargeDiff(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "chore: seed")

	// ~4 MB added → ~1M raw tokens → ≈$0.17 at gpt-4o-mini input pricing,
	// comfortably over $0.01 (the test lifts max_diff_bytes so the full diff
	// reaches the prompt rather than being truncated to 120 KB).
	big := strings.Repeat("this is a line of synthetic diff content\n", 100_000)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "feat: add large file")
	return dir
}

func TestReviewDryRunOffline(t *testing.T) {
	dir := setupRepo(t, false)
	out, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "HEAD~1..HEAD", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run offline: %v", err)
	}
	if !strings.Contains(out, "offline mode — no API call, no cost") {
		t.Errorf("offline dry-run message missing:\n%s", out)
	}
}

func TestReviewDryRunOnline(t *testing.T) {
	dir := setupRepo(t, false)
	env := map[string]string{"GITL_API_KEY": "sk-fake-not-used"}
	out, err := runReviewInDir(t, dir, env, "HEAD~1..HEAD", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run online: %v", err)
	}
	if !strings.Contains(out, "estimated cost") || !strings.Contains(out, "estimate, not exact") {
		t.Errorf("online dry-run estimate missing:\n%s", out)
	}
}

// TestReviewStagedOffline: --staged reviews the index instead of a commit
// range, with no range argument.
func TestReviewStagedOffline(t *testing.T) {
	dir := setupRepo(t, false)
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("new staged content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "staged.txt")

	out, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "--staged", "--format=md")
	if err != nil {
		t.Fatalf("staged review: %v", err)
	}
	if !strings.Contains(out, "**Risk:**") {
		t.Errorf("staged review missing risk header:\n%s", out)
	}
}

// TestReviewStagedNoChanges: --staged with an empty index is a clear user
// error, not a silent empty review.
func TestReviewStagedNoChanges(t *testing.T) {
	dir := setupRepo(t, false)
	_, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "--staged")
	if err == nil {
		t.Fatal("expected error for --staged with nothing staged")
	}
	if !strings.Contains(err.Error(), "no staged changes") {
		t.Errorf("error should mention no staged changes, got: %v", err)
	}
}

// TestReviewStagedAndRangeConflict: --staged and a positional range are
// mutually exclusive.
func TestReviewStagedAndRangeConflict(t *testing.T) {
	dir := setupRepo(t, false)
	_, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "--staged", "HEAD~1..HEAD")
	if err == nil {
		t.Fatal("expected error when combining --staged with a range")
	}
	if !strings.Contains(err.Error(), "cannot combine --staged") {
		t.Errorf("error should name the conflict, got: %v", err)
	}
}

// TestReviewStagedAllExcluded: staged changes that are entirely excluded by
// exclude_globs must error clearly, not silently review an empty diff.
func TestReviewStagedAllExcluded(t *testing.T) {
	dir := setupRepo(t, false)
	if err := os.WriteFile(filepath.Join(dir, "excluded.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "excluded.txt")

	cfgPath := filepath.Join(dir, "gitl-exclude.yaml")
	cfgYAML := "diff:\n  exclude_globs: [\"excluded.txt\"]\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()
	t.Setenv("GITL_API_KEY", "")

	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"review", "--config", cfgPath, "--staged"})
	err = root.ExecuteContext(context.Background())

	if err == nil {
		t.Fatal("expected error when all staged files are excluded by exclude_globs")
	}
	if !strings.Contains(err.Error(), "excluded") {
		t.Errorf("error should mention exclude_globs, got: %v", err)
	}
}

// TestReviewNoRangeNoStaged: neither a range nor --staged is also a clear
// user error.
func TestReviewNoRangeNoStaged(t *testing.T) {
	dir := setupRepo(t, false)
	_, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""})
	if err == nil {
		t.Fatal("expected error when neither a range nor --staged is given")
	}
	if !strings.Contains(err.Error(), "provide a revision range") {
		t.Errorf("error should prompt for a range or --staged, got: %v", err)
	}
}
