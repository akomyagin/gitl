package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runChangelogInDir chdirs into dir and runs `gitl changelog` with the given
// args, restoring cwd afterward. Mirrors runReviewInDir in command_test.go.
func runChangelogInDir(t *testing.T, dir string, args ...string) (string, string, error) {
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
	full := append([]string{"changelog", "--config", empty}, args...)
	root.SetArgs(full)
	err = root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// setupChangelogRepo builds a repo with a mix of conventional and
// non-conventional commits, no tags.
func setupChangelogRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")

	writeAndCommit := func(name, content, msg string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-q", "-m", msg)
	}

	writeAndCommit("a.go", "package a\n", "feat: add feature A")
	writeAndCommit("b.go", "package b\n", "fix: correct bug in B")
	writeAndCommit("c.md", "# c\n", "docs: document C")
	return dir
}

func TestChangelogNoTagsDefaultsToFullHistory(t *testing.T) {
	dir := setupChangelogRepo(t)
	out, _, err := runChangelogInDir(t, dir)
	if err != nil {
		t.Fatalf("changelog: %v", err)
	}
	if !strings.Contains(out, "## [Unreleased] — HEAD") {
		t.Errorf("expected range to default to HEAD (no tags):\n%s", out)
	}
	if !strings.Contains(out, "### Added") || !strings.Contains(out, "add feature A") {
		t.Errorf("missing Added section:\n%s", out)
	}
	if !strings.Contains(out, "### Fixed") || !strings.Contains(out, "correct bug in B") {
		t.Errorf("missing Fixed section:\n%s", out)
	}
	if !strings.Contains(out, "### Other") || !strings.Contains(out, "document C") {
		t.Errorf("missing Other section:\n%s", out)
	}
}

func TestChangelogWithTagDefaultsToTagRange(t *testing.T) {
	dir := setupChangelogRepo(t)
	runGit(t, dir, "tag", "v1.0.0")

	if err := os.WriteFile(filepath.Join(dir, "d.go"), []byte("package d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "feat: add feature D after tag")

	out, _, err := runChangelogInDir(t, dir)
	if err != nil {
		t.Fatalf("changelog: %v", err)
	}
	if !strings.Contains(out, "## [Unreleased] — v1.0.0..HEAD") {
		t.Errorf("expected range v1.0.0..HEAD:\n%s", out)
	}
	if strings.Contains(out, "add feature A") {
		t.Errorf("pre-tag commit should not appear:\n%s", out)
	}
	if !strings.Contains(out, "add feature D after tag") {
		t.Errorf("post-tag commit missing:\n%s", out)
	}
}

func TestChangelogExplicitRange(t *testing.T) {
	dir := setupChangelogRepo(t)
	out, _, err := runChangelogInDir(t, dir, "HEAD~1..HEAD")
	if err != nil {
		t.Fatalf("changelog: %v", err)
	}
	if !strings.Contains(out, "## [Unreleased] — HEAD~1..HEAD") {
		t.Errorf("expected explicit range echoed:\n%s", out)
	}
	if strings.Contains(out, "add feature A") || strings.Contains(out, "correct bug in B") {
		t.Errorf("only the last commit should appear:\n%s", out)
	}
	if !strings.Contains(out, "document C") {
		t.Errorf("last commit missing:\n%s", out)
	}
}

func TestChangelogJSONFormat(t *testing.T) {
	dir := setupChangelogRepo(t)
	out, _, err := runChangelogInDir(t, dir, "--format=json")
	if err != nil {
		t.Fatalf("changelog: %v", err)
	}
	if !strings.Contains(out, `"schema_version": 1`) {
		t.Errorf("missing schema_version:\n%s", out)
	}
	if !strings.Contains(out, `"command": "changelog"`) {
		t.Errorf("missing command field:\n%s", out)
	}
	if !strings.Contains(out, `"Added"`) || !strings.Contains(out, `"Fixed"`) || !strings.Contains(out, `"Other"`) {
		t.Errorf("missing category keys:\n%s", out)
	}
}

func TestChangelogRequiredCategoryWarns(t *testing.T) {
	dir := setupChangelogRepo(t)
	writeFile := filepath.Join(dir, ".gitl.yaml")
	if err := os.WriteFile(writeFile, []byte("policy:\n  required_changelog_categories: [\"Security\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// slog.Warn writes to the real os.Stderr (configured by setupLogging),
	// not to the cobra command's SetErr buffer, so it must be captured at the
	// file-descriptor level.
	osStderr := captureStderr(t, func() {
		out, _, err := runChangelogInDir(t, dir)
		if err != nil {
			t.Fatalf("changelog must not fail on a missing required category: %v", err)
		}
		if out == "" {
			t.Error("changelog output should still be printed despite the warning")
		}
	})
	if !strings.Contains(osStderr, "required changelog category") || !strings.Contains(osStderr, "Security") {
		t.Errorf("expected warning about missing Security category on stderr, got:\n%s", osStderr)
	}
}

// captureStderr redirects the process-level os.Stderr for the duration of fn
// and returns everything written to it. Needed because slog's default
// handler (wired up by setupLogging in PersistentPreRun) writes directly to
// os.Stderr, bypassing cobra's SetErr buffer.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestChangelogNeverCallsLLM(t *testing.T) {
	// No GITL_API_KEY is set, and changelog must succeed regardless — it has
	// no online/offline branching at all (§9.1).
	dir := setupChangelogRepo(t)
	t.Setenv("GITL_API_KEY", "")
	_, _, err := runChangelogInDir(t, dir)
	if err != nil {
		t.Fatalf("changelog: %v", err)
	}
}

func TestChangelogEmptyRepoNoCommitsInRange(t *testing.T) {
	dir := setupChangelogRepo(t)
	out, _, err := runChangelogInDir(t, dir, "HEAD..HEAD")
	if err != nil {
		t.Fatalf("changelog with an empty range must not fail: %v", err)
	}
	if !strings.Contains(out, "No changes in this range.") {
		t.Errorf("expected the empty-changelog message:\n%s", out)
	}
}
