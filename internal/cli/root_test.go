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

// execute runs the command tree with args, returning captured stdout/stderr.
func execute(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), errBuf.String(), err
}

func TestVersionCommand(t *testing.T) {
	stdout, _, err := execute(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(stdout, "gitl 0.0.0-dev") {
		t.Errorf("version output = %q", stdout)
	}
}

func TestHelp(t *testing.T) {
	stdout, _, err := execute(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"review", "version", "--verbose", "--config"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--help output missing %q:\n%s", want, stdout)
		}
	}
}

// setupCLIRepo builds a two-commit repo and chdirs into it for the test.
func setupCLIRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping CLI integration test")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-c", "commit.gpgsign=false"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=CLI Tester", "GIT_AUTHOR_EMAIL=cli@example.com",
			"GIT_COMMITTER_NAME=CLI Tester", "GIT_COMMITTER_EMAIL=cli@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "feat: first")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "fix: second")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	return dir
}

func TestReviewOfflineEndToEnd(t *testing.T) {
	dir := setupCLIRepo(t)
	t.Setenv("GITL_API_KEY", "") // force offline mode regardless of the host env

	stdout, stderr, err := execute(t,
		"review", "HEAD~1..HEAD",
		"--config", filepath.Join(dir, "no-such-personal-config.yaml"),
	)
	if err != nil {
		t.Fatalf("review: %v (stderr: %s)", err, stderr)
	}
	for _, want := range []string{"# Code review (offline)", "fix: second", "a.txt"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("review output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "offline") {
		t.Errorf("expected offline warning on stderr, got: %q", stderr)
	}
}

func TestReviewUnsupportedProviderFails(t *testing.T) {
	dir := setupCLIRepo(t)
	t.Setenv("GITL_API_KEY", "sk-configured") // online mode → provider matters

	// "gemini" is not one of the three supported providers (openai/ollama/
	// azure_openai) — a real configuration error, caught before any request.
	repoCfg := "llm:\n  provider: gemini\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitl.yaml"), []byte(repoCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := execute(t,
		"review", "HEAD~1..HEAD",
		"--config", filepath.Join(dir, "no-such-personal-config.yaml"),
	)
	if err == nil {
		t.Fatal("expected unsupported-provider error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("error should say the provider is unsupported, got: %v", err)
	}
}

func TestReviewBadRangeFails(t *testing.T) {
	dir := setupCLIRepo(t)
	t.Setenv("GITL_API_KEY", "")

	_, _, err := execute(t,
		"review", "no-such-ref..HEAD",
		"--config", filepath.Join(dir, "no-such-personal-config.yaml"),
	)
	if err == nil {
		t.Fatal("expected error for unknown revision, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-ref") {
		t.Errorf("error should mention the bad ref, got: %v", err)
	}
}
