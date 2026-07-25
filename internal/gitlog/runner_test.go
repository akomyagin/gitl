package gitlog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestRunnerLogSeparatorInjection reproduces the real exploit end-to-end: a
// commit whose message carries raw \x1e/\x1f bytes (git stores them verbatim
// when the message comes from a file via `git commit -F`). Under the old
// \x1f/\x1e pretty-format separators such a commit split into bogus records
// (corrupting file attribution) or hard-failed ParseLog — a one-commit DoS.
// With NUL separators the bytes are inert body text.
//
// There is deliberately no companion test for "a single NUL inside a commit
// body": that object is physically impossible — git fsck's nulInCommit check
// rejects it at creation time regardless of tooling (even raw
// `git hash-object -w -t commit --stdin`), which is precisely why the
// NUL-separator scheme is safe: a field-sep NUL cannot be forged.
func TestRunnerLogSeparatorInjection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := setupTestRepo(t)

	// Commit a regular file with an attacker-controlled message containing the
	// old field/record separator bytes, via -F (the -m path would also pass
	// them through, but -F mirrors the original exploit exactly).
	writeTestFile(t, dir, "victim.txt", "innocent content\n")
	msg := "feat: attack subject\n\nbody-before\x1einjected-record\x1finjected-field\nmore body\n"
	msgFile := filepath.Join(t.TempDir(), "msg")
	if err := os.WriteFile(msgFile, []byte(msg), 0o644); err != nil {
		t.Fatalf("write commit message file: %v", err)
	}
	runTestGit(t, dir, "add", "victim.txt")
	runTestGit(t, dir, "commit", "-q", "-F", msgFile)

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	commits, err := runner.Log(context.Background(), "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	// setupTestRepo makes 3 commits; the hostile one must be exactly one more,
	// not split into several bogus records.
	if len(commits) != 4 {
		t.Fatalf("expected 4 commits, got %d: %+v", len(commits), commits)
	}

	attack := commits[0] // newest first
	if attack.Subject != "feat: attack subject" {
		t.Errorf("attack commit subject = %q", attack.Subject)
	}
	if !strings.Contains(attack.Body, "body-before\x1einjected-record") ||
		!strings.Contains(attack.Body, "injected-record\x1finjected-field") {
		t.Errorf("attack commit body lost or split the raw separator bytes: %q", attack.Body)
	}
	if len(attack.Files) != 1 || attack.Files[0].Status != "A" || attack.Files[0].Path != "victim.txt" {
		t.Errorf("attack commit files = %+v, want single A victim.txt attributed to it", attack.Files)
	}
	// The neighboring commit must keep its own attribution untouched.
	if neighbor := commits[1]; neighbor.Subject != "refactor: rename a.txt" {
		t.Errorf("commit[1] subject = %q, want the rename commit", neighbor.Subject)
	}
}

// TestRunnerLogHandlesEmptyMessageCommit is the end-to-end regression test
// for the empty-message bug: `git commit --allow-empty-message -m ""` yields
// a commit whose %s AND %b are both empty. Under the previous 2-NUL-terminator
// design that produced 4 adjacent NULs after the date; the \x00{2,} record
// regex swallowed them as one boundary, dropped two fields and failed the
// whole range with "expected 4 or 5 NUL-separated fields in record, got 3" —
// one legitimate commit made the entire history unreadable. The flat 5*N+1
// token scheme parses it like any other commit.
func TestRunnerLogHandlesEmptyMessageCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q", "-b", "main")

	writeTestFile(t, dir, "a.txt", "content a\n")
	runTestGit(t, dir, "add", "a.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "feat: commit one\n\nbody one")

	writeTestFile(t, dir, "b.txt", "content b\n")
	runTestGit(t, dir, "add", "b.txt")
	runTestGit(t, dir, "commit", "-q", "--allow-empty-message", "-m", "")

	writeTestFile(t, dir, "c.txt", "content c\n")
	runTestGit(t, dir, "add", "c.txt")
	runTestGit(t, dir, "commit", "-q", "-m", "feat: commit three")

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

	// Newest first.
	if commits[0].Subject != "feat: commit three" {
		t.Errorf("commit[0] subject = %q", commits[0].Subject)
	}
	if len(commits[0].Files) != 1 || commits[0].Files[0].Path != "c.txt" {
		t.Errorf("commit[0] files = %+v, want single A c.txt", commits[0].Files)
	}

	empty := commits[1]
	if empty.Subject != "" || empty.Body != "" {
		t.Errorf("empty-message commit: subject = %q, body = %q, want both empty", empty.Subject, empty.Body)
	}
	if len(empty.Files) != 1 || empty.Files[0].Status != "A" || empty.Files[0].Path != "b.txt" {
		t.Errorf("empty-message commit files = %+v, want single A b.txt", empty.Files)
	}

	first := commits[2]
	if first.Subject != "feat: commit one" || first.Body != "body one" {
		t.Errorf("commit[2] subject/body = %q / %q", first.Subject, first.Body)
	}
	if len(first.Files) != 1 || first.Files[0].Path != "a.txt" {
		t.Errorf("commit[2] files = %+v, want single A a.txt", first.Files)
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

// gitOutput runs a git command in dir and returns its trimmed stdout (for
// reading back real SHAs in tests).
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestRunnerObjectExists: a real commit SHA is reported as locally available;
// a well-formed but nonexistent SHA and a malformed ref are both false — never
// an error (boolean probe semantics).
func TestRunnerObjectExists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := setupTestRepo(t)

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	sha := gitOutput(t, dir, "rev-parse", "HEAD")
	if !runner.ObjectExists(ctx, sha) {
		t.Errorf("ObjectExists(%q) = false, want true for a real local commit", sha)
	}
	if fake := strings.Repeat("deadbeef", 5); runner.ObjectExists(ctx, fake) {
		t.Errorf("ObjectExists(%q) = true, want false for a nonexistent SHA", fake)
	}
	if runner.ObjectExists(ctx, "not-a-sha") {
		t.Error("ObjectExists(malformed) = true, want false")
	}
}

// TestRunnerFetchRef: fetching a branch ref from origin makes its commit
// object locally available (the pr/N best-effort fetch path); fetching a
// nonexistent ref surfaces git's error.
func TestRunnerFetchRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	origin := setupTestRepo(t)

	work := t.TempDir()
	runTestGit(t, work, "clone", "-q", origin, "clone")
	cloneDir := filepath.Join(work, "clone")

	// A commit created in origin AFTER the clone: unknown to the clone until
	// fetched.
	runTestGit(t, origin, "checkout", "-q", "-b", "extra")
	writeTestFile(t, origin, "extra.txt", "extra content\n")
	runTestGit(t, origin, "add", ".")
	runTestGit(t, origin, "commit", "-q", "-m", "feat: extra commit")
	extraSHA := gitOutput(t, origin, "rev-parse", "HEAD")

	runner, err := NewRunner(cloneDir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	if runner.ObjectExists(ctx, extraSHA) {
		t.Fatalf("commit %s unexpectedly present in clone before fetch", extraSHA)
	}
	if err := runner.FetchRef(ctx, "origin", "extra"); err != nil {
		t.Fatalf("FetchRef(origin, extra): %v", err)
	}
	if !runner.ObjectExists(ctx, extraSHA) {
		t.Errorf("commit %s still missing after FetchRef", extraSHA)
	}

	if err := runner.FetchRef(ctx, "origin", "no-such-ref"); err == nil {
		t.Error("FetchRef(origin, no-such-ref): expected error, got nil")
	}
	if err := runner.FetchRef(ctx, "no-such-remote", "extra"); err == nil {
		t.Error("FetchRef(no-such-remote, extra): expected error, got nil")
	}

	// Zero refs: documented no-op — nil error, and crucially NO bare
	// `git fetch <remote>` (which would fetch the default refspec).
	if err := runner.FetchRef(ctx, "no-such-remote"); err != nil {
		t.Errorf("FetchRef with zero refs = %v, want nil no-op", err)
	}
}

// TestRunnerFetchRefMultipleRefs: two refspecs in ONE FetchRef call (the
// combined pr-head + base fetch of prSource) makes both commits available
// locally with a single git invocation.
func TestRunnerFetchRefMultipleRefs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	origin := setupTestRepo(t)

	work := t.TempDir()
	runTestGit(t, work, "clone", "-q", origin, "clone")
	cloneDir := filepath.Join(work, "clone")

	// Two branches created in origin AFTER the clone: both unknown locally.
	runTestGit(t, origin, "checkout", "-q", "-b", "one")
	writeTestFile(t, origin, "one.txt", "one\n")
	runTestGit(t, origin, "add", ".")
	runTestGit(t, origin, "commit", "-q", "-m", "feat: branch one")
	oneSHA := gitOutput(t, origin, "rev-parse", "HEAD")

	runTestGit(t, origin, "checkout", "-q", "-b", "two")
	writeTestFile(t, origin, "two.txt", "two\n")
	runTestGit(t, origin, "add", ".")
	runTestGit(t, origin, "commit", "-q", "-m", "feat: branch two")
	twoSHA := gitOutput(t, origin, "rev-parse", "HEAD")

	runner, err := NewRunner(cloneDir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	if runner.ObjectExists(ctx, oneSHA) || runner.ObjectExists(ctx, twoSHA) {
		t.Fatal("branch commits unexpectedly present in clone before fetch")
	}
	if err := runner.FetchRef(ctx, "origin", "one", "two"); err != nil {
		t.Fatalf("FetchRef(origin, one, two): %v", err)
	}
	if !runner.ObjectExists(ctx, oneSHA) {
		t.Errorf("commit %s (ref one) still missing after combined fetch", oneSHA)
	}
	if !runner.ObjectExists(ctx, twoSHA) {
		t.Errorf("commit %s (ref two) still missing after combined fetch", twoSHA)
	}
}

// TestRunnerNumstatSince: one `git log --numstat` call returns per-commit
// added/removed totals for every commit in the window, keyed by full hash —
// including a pure rename (0/0 via the rename-format path, which the parser
// never inspects) and multi-file commits summed per commit.
func TestRunnerNumstatSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := setupTestRepo(t)

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	since := time.Now().Add(-24 * time.Hour)
	stats, err := runner.NumstatSince(ctx, since)
	if err != nil {
		t.Fatalf("NumstatSince: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("stats = %#v, want entries for all 3 commits", stats)
	}

	// Map git's SHAs to the three known commits via rev-parse.
	renameSHA := gitOutput(t, dir, "rev-parse", "HEAD")
	midSHA := gitOutput(t, dir, "rev-parse", "HEAD~1")
	firstSHA := gitOutput(t, dir, "rev-parse", "HEAD~2")

	// Commit 1: a.txt (+2) and b.txt (+1) added → +3/-0.
	if got := stats[firstSHA]; got != (LineStats{Added: 3, Removed: 0}) {
		t.Errorf("first commit stats = %+v, want +3/-0", got)
	}
	// Commit 2: a.txt +1 line, b.txt -1 line (deleted), c.txt +1 line → +2/-1.
	if got := stats[midSHA]; got != (LineStats{Added: 2, Removed: 1}) {
		t.Errorf("mid commit stats = %+v, want +2/-1", got)
	}
	// Commit 3: pure rename (R100) → 0/0, present but zero.
	if got, ok := stats[renameSHA]; !ok || got != (LineStats{}) {
		t.Errorf("rename commit stats = %+v (present=%v), want zero LineStats present", got, ok)
	}

	// Empty window: future since → empty map, no error.
	empty, err := runner.NumstatSince(ctx, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("NumstatSince(empty window): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty-window stats = %#v, want empty map", empty)
	}
}

// TestRunnerNumstatSinceMergeCommit: a merge commit appears in the window with
// zero stats (git log prints no numstat block for merges) — must not crash or
// mis-attribute neighbouring commits.
func TestRunnerNumstatSinceMergeCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q", "-b", "main")
	writeTestFile(t, dir, "base.txt", "base\n")
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-q", "-m", "feat: base")
	runTestGit(t, dir, "checkout", "-q", "-b", "side")
	writeTestFile(t, dir, "side.txt", "side\n")
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-q", "-m", "feat: side")
	runTestGit(t, dir, "checkout", "-q", "main")
	writeTestFile(t, dir, "main.txt", "main\n")
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-q", "-m", "feat: main")
	runTestGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	mergeSHA := gitOutput(t, dir, "rev-parse", "HEAD")

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	stats, err := runner.NumstatSince(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("NumstatSince: %v", err)
	}
	if len(stats) != 4 {
		t.Fatalf("stats = %#v, want 4 entries (3 commits + merge)", stats)
	}
	if got := stats[mergeSHA]; got != (LineStats{}) {
		t.Errorf("merge commit stats = %+v, want zero", got)
	}
	sideSHA := gitOutput(t, dir, "rev-parse", "side")
	if got := stats[sideSHA]; got != (LineStats{Added: 1, Removed: 0}) {
		t.Errorf("side commit stats = %+v, want +1/-0", got)
	}
}

// TestRunnerLogRejectsFlagLikeRevision reproduces the git argument-injection
// exploit: a revision that is actually a git flag (`--output=<file>`) must NOT
// be interpreted as an option. Before the `--end-of-options` fix, git wrote its
// log to the attacker-controlled path and gitl silently reported no commits.
// With the fix git rejects the flag-like argument and no file is written.
func TestRunnerLogRejectsFlagLikeRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := setupTestRepo(t)

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	target := filepath.Join(t.TempDir(), "should-not-exist")
	_, err = runner.Log(context.Background(), "--output="+target)
	if err == nil {
		t.Fatal("Log with flag-like revision: expected error, got nil (injection not blocked)")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("Log error text is empty; expected a real git failure message")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("injected --output file was created at %q (stat err = %v); injection NOT blocked", target, statErr)
	}
}

// TestRunnerDiffRejectsFlagLikeRevision is the same argument-injection check for
// Diff: a `--output=<file>` "revision" must be rejected, not executed.
func TestRunnerDiffRejectsFlagLikeRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := setupTestRepo(t)

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	target := filepath.Join(t.TempDir(), "should-not-exist")
	_, err = runner.Diff(context.Background(), "--output="+target)
	if err == nil {
		t.Fatal("Diff with flag-like revision: expected error, got nil (injection not blocked)")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("Diff error text is empty; expected a real git failure message")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("injected --output file was created at %q (stat err = %v); injection NOT blocked", target, statErr)
	}
}

// TestRunnerFetchRejectsFlagLikeRef checks defense-in-depth on FetchRef: a ref
// that is actually an option (`--upload-pack=...`) must be rejected by git as an
// invalid refspec rather than executed. The marker file the payload would create
// must not appear.
func TestRunnerFetchRejectsFlagLikeRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	origin := setupTestRepo(t)

	work := t.TempDir()
	runTestGit(t, work, "clone", "-q", origin, "clone")
	cloneDir := filepath.Join(work, "clone")

	runner, err := NewRunner(cloneDir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "pwned")
	err = runner.FetchRef(context.Background(), "origin", "--upload-pack=touch "+marker)
	if err == nil {
		t.Error("FetchRef with flag-like ref: expected error, got nil (injection not blocked)")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Errorf("injected --upload-pack payload ran: marker %q exists (stat err = %v)", marker, statErr)
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

// TestRunnerDiffNonASCIIPathUnquoted: the Runner passes -c core.quotepath=false
// to every diff-producing invocation, so git emits non-ASCII filenames as
// plain UTF-8 in the "diff --git" header instead of the quoted/octal-escaped
// form (git's default). Downstream glob matching and path extraction then see
// the real path directly. (DiffSectionPath still decodes quoted headers for
// diff text produced elsewhere — this is defense in depth, not a replacement.)
func TestRunnerDiffNonASCIIPathUnquoted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q", "-b", "main")

	const name = "данные.lock"
	writeTestFile(t, dir, name, "v1\n")
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-q", "-m", "feat: add non-ascii file")
	writeTestFile(t, dir, name, "v2\n")
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-q", "-m", "chore: modify non-ascii file")

	runner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	diff, err := runner.Diff(ctx, "HEAD~1..HEAD")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	wantHeader := "diff --git a/" + name + " b/" + name
	if !strings.Contains(diff, wantHeader) {
		t.Errorf("Diff header not unquoted UTF-8; want %q in:\n%s", wantHeader, diff)
	}
	if strings.Contains(diff, `\320`) {
		t.Errorf("Diff still contains octal-escaped (quoted) path bytes:\n%s", diff)
	}
	if got := DiffPaths(diff); len(got) != 1 || got[0] != name {
		t.Errorf("DiffPaths(diff) = %v, want [%s]", got, name)
	}

	// Same guarantee for the staged path.
	writeTestFile(t, dir, name, "v3\n")
	runTestGit(t, dir, "add", ".")
	staged, err := runner.DiffStaged(ctx)
	if err != nil {
		t.Fatalf("DiffStaged: %v", err)
	}
	if !strings.Contains(staged, wantHeader) {
		t.Errorf("DiffStaged header not unquoted UTF-8; want %q in:\n%s", wantHeader, staged)
	}
}

// TestRunnerTopLevel: TopLevel must return the absolute working-tree root both
// when the Runner is rooted at the repo root itself AND when rooted at a
// nested subdirectory (git walks up to .git) — the load-bearing property that
// lets config discovery find a committed root-level .gitl.yaml from anywhere
// inside the repo. Outside any repo it must error, never guess.
func TestRunnerTopLevel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping integration test")
	}
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-q", "-b", "main")
	writeTestFile(t, dir, "a.txt", "one\n")
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-q", "-m", "feat: initial file")

	// git resolves symlinks in --show-toplevel output (e.g. /tmp vs
	// /private/tmp on macOS), so compare symlink-resolved paths on both sides.
	wantRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	ctx := context.Background()

	resolve := func(runner *Runner) string {
		t.Helper()
		got, err := runner.TopLevel(ctx)
		if err != nil {
			t.Fatalf("TopLevel: %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("TopLevel = %q, want an absolute path", got)
		}
		resolved, err := filepath.EvalSymlinks(got)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", got, err)
		}
		return resolved
	}

	rootRunner, err := NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if got := resolve(rootRunner); got != wantRoot {
		t.Errorf("TopLevel from repo root = %q, want %q", got, wantRoot)
	}

	// From a nested subdirectory: still the OUTER repo root.
	sub := filepath.Join(dir, "src", "internal")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	subRunner, err := NewRunner(sub)
	if err != nil {
		t.Fatalf("NewRunner(sub): %v", err)
	}
	if got := resolve(subRunner); got != wantRoot {
		t.Errorf("TopLevel from subdirectory = %q, want %q", got, wantRoot)
	}

	// Outside any git repository: an error, so callers fall back to cwd-only
	// config discovery.
	outside, err := NewRunner(t.TempDir())
	if err != nil {
		t.Fatalf("NewRunner(outside): %v", err)
	}
	if got, err := outside.TopLevel(ctx); err == nil {
		t.Errorf("TopLevel outside a repo = %q, want error", got)
	}
}
