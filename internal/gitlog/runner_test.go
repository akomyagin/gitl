package gitlog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnv isolates test git invocations from user/system configuration and
// makes author data deterministic.
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

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "diff.renames=true"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// setupTestRepo builds a repo with three commits:
//  1. add a.txt + b.txt (multi-line body)
//  2. modify a.txt, delete b.txt, add c.txt
//  3. rename a.txt -> renamed.txt (pure rename => R100)
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q", "-b", "main")

	writeTestFile(t, dir, "a.txt", "line one\nline two\n")
	writeTestFile(t, dir, "b.txt", "temporary\n")
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-q", "-m", "feat: initial files\n\nBody first line.\n\nBody second paragraph.")

	writeTestFile(t, dir, "a.txt", "line one\nline two\nline three\n")
	writeTestFile(t, dir, "c.txt", "new file\n")
	runTestGit(t, dir, "rm", "-q", "b.txt")
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-q", "-m", "chore: modify, delete, add")

	runTestGit(t, dir, "mv", "a.txt", "renamed.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "refactor: rename a.txt")

	return dir
}

func TestRunnerLogAgainstRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := setupTestRepo(t)

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	commits, err := runner.Log(context.Background(), "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d: %+v", len(commits), commits)
	}

	// Newest first: rename commit.
	rename := commits[0]
	if rename.Subject != "refactor: rename a.txt" {
		t.Errorf("commit[0] subject = %q", rename.Subject)
	}
	if len(rename.Files) != 1 || rename.Files[0].Status != "R100" ||
		rename.Files[0].Old != "a.txt" || rename.Files[0].Path != "renamed.txt" {
		t.Errorf("commit[0] files = %+v, want single R100 a.txt->renamed.txt", rename.Files)
	}

	mid := commits[1]
	wantStatuses := map[string]string{"a.txt": "M", "b.txt": "D", "c.txt": "A"}
	if len(mid.Files) != len(wantStatuses) {
		t.Fatalf("commit[1] files = %+v, want 3 entries", mid.Files)
	}
	for _, f := range mid.Files {
		if want, ok := wantStatuses[f.Path]; !ok || f.Status != want {
			t.Errorf("commit[1] unexpected file change %+v", f)
		}
	}

	first := commits[2]
	if first.Author != "Test Author" {
		t.Errorf("commit[2] author = %q", first.Author)
	}
	if want := "Body first line.\n\nBody second paragraph."; first.Body != want {
		t.Errorf("commit[2] body = %q, want %q", first.Body, want)
	}
	if first.Date.IsZero() {
		t.Errorf("commit[2] date is zero")
	}
}

func TestRunnerDiffAndErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := setupTestRepo(t)

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	diff, err := runner.Diff(ctx, "HEAD~2..HEAD")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "c.txt") || !strings.Contains(diff, "renamed.txt") {
		t.Errorf("diff does not mention expected files:\n%s", diff)
	}

	if _, err := runner.Log(ctx, "no-such-ref..HEAD"); err == nil {
		t.Error("Log with bad range: expected error, got nil")
	} else if !strings.Contains(err.Error(), "no-such-ref") {
		t.Errorf("Log error should carry git stderr, got: %v", err)
	}
}

func TestRunnerDiffStaged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := setupTestRepo(t)

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	// Nothing staged yet: empty diff, no error.
	diff, err := runner.DiffStaged(ctx)
	if err != nil {
		t.Fatalf("DiffStaged with empty index: %v", err)
	}
	if diff != "" {
		t.Errorf("DiffStaged with empty index = %q, want empty", diff)
	}

	// Stage a new file without committing.
	writeTestFile(t, dir, "staged.txt", "staged content\n")
	runTestGit(t, dir, "add", "staged.txt")

	diff, err = runner.DiffStaged(ctx)
	if err != nil {
		t.Fatalf("DiffStaged: %v", err)
	}
	if !strings.Contains(diff, "staged.txt") || !strings.Contains(diff, "+staged content") {
		t.Errorf("DiffStaged does not mention the staged file:\n%s", diff)
	}
}
