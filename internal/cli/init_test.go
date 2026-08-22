package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/akomyagin/gitl/internal/config"
)

// runInitInDir chdirs into dir and runs `gitl init` with the given args
// through the real command tree. It returns stdout and the run error, and
// restores cwd.
func runInitInDir(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()

	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"init"}, args...))
	err = root.ExecuteContext(context.Background())
	return stdout.String(), err
}

// loadGenerated round-trips the generated file through the real config loader,
// isolating the personal-config layer so no host config leaks in.
func loadGenerated(t *testing.T, repoDir string) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.Options{
		RepoDir:      repoDir,
		PersonalPath: filepath.Join(t.TempDir(), "none.yaml"),
	})
	if err != nil {
		t.Fatalf("config.Load on generated .gitl.yaml: %v", err)
	}
	return cfg
}

// TestInitWritesToRepoRoot runs `gitl init` from a subdirectory of a git repo
// and asserts the file lands at the repo root, not the cwd.
func TestInitWritesToRepoRoot(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runInitInDir(t, sub)
	if err != nil {
		t.Fatalf("gitl init: %v", err)
	}
	rootFile := filepath.Join(dir, ".gitl.yaml")
	if _, err := os.Stat(rootFile); err != nil {
		t.Fatalf("expected %s to exist: %v", rootFile, err)
	}
	if _, err := os.Stat(filepath.Join(sub, ".gitl.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no .gitl.yaml in subdir, stat err = %v", err)
	}
	if !strings.Contains(out, ".gitl.yaml") {
		t.Fatalf("confirmation output should mention the target, got: %q", out)
	}
}

// TestInitOutputFlag asserts --output writes exactly to the given path.
func TestInitOutputFlag(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	custom := filepath.Join(t.TempDir(), "custom.yaml")

	if _, err := runInitInDir(t, dir, "--output", custom); err != nil {
		t.Fatalf("gitl init --output: %v", err)
	}
	got, err := os.ReadFile(custom)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", custom, err)
	}
	if string(got) != starterConfig {
		t.Fatal("--output file content does not match the starter template")
	}
	// The default target must NOT have been written.
	if _, err := os.Stat(filepath.Join(dir, ".gitl.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no .gitl.yaml at repo root, stat err = %v", err)
	}
}

// TestInitRefusesOverwriteWithoutForce pre-creates the target and asserts init
// errors, mentions --force, and leaves the original contents untouched.
func TestInitRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	target := filepath.Join(dir, ".gitl.yaml")
	sentinel := "llm:\n  provider: \"ollama\"\n"
	if err := os.WriteFile(target, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runInitInDir(t, dir)
	if err == nil {
		t.Fatal("expected an error when the target already exists")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error should mention --force, got: %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != sentinel {
		t.Fatal("existing file was modified despite the refusal")
	}
}

// TestInitForceOverwrites asserts --force replaces an existing file with the
// starter template.
func TestInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	target := filepath.Join(dir, ".gitl.yaml")
	if err := os.WriteFile(target, []byte("sentinel: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runInitInDir(t, dir, "--force"); err != nil {
		t.Fatalf("gitl init --force: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != starterConfig {
		t.Fatal("--force did not replace the file with the starter template")
	}
}

// TestInitRoundTripsThroughLoader is the load-bearing test: the generated
// template must parse and validate through the real config.Load, and its
// active values must equal the built-in defaults.
func TestInitRoundTripsThroughLoader(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	if _, err := runInitInDir(t, dir); err != nil {
		t.Fatalf("gitl init: %v", err)
	}

	cfg := loadGenerated(t, dir)
	if cfg.LLM.Provider != "openai" {
		t.Errorf("llm.provider = %q, want %q", cfg.LLM.Provider, "openai")
	}
	if cfg.LLM.Model != "gpt-4o-mini" {
		t.Errorf("llm.model = %q, want %q", cfg.LLM.Model, "gpt-4o-mini")
	}
	if cfg.Policy.FailOn != "never" {
		t.Errorf("policy.fail_on = %q, want %q", cfg.Policy.FailOn, "never")
	}
	if !cfg.Policy.RiskLogEnabled {
		t.Error("policy.risk_log_enabled = false, want true")
	}
	if !slices.Contains(cfg.Diff.ExcludeGlobs, "vendor/**") {
		t.Errorf("diff.exclude_globs = %v, want to contain %q", cfg.Diff.ExcludeGlobs, "vendor/**")
	}
	if !cfg.Cache.Enabled {
		t.Error("cache.enabled = false, want true")
	}
	if cfg.Cache.TTLHours != 24 {
		t.Errorf("cache.ttl_hours = %d, want 24", cfg.Cache.TTLHours)
	}
}

// TestInitNonGitDirFallback runs init where repo-root discovery fails and
// asserts the file is written to the cwd and still round-trips.
func TestInitNonGitDirFallback(t *testing.T) {
	// Force `git rev-parse --show-toplevel` to fail regardless of where the
	// test temp dir lives.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "no-such-git-dir"))
	dir := t.TempDir()

	if _, err := runInitInDir(t, dir); err != nil {
		t.Fatalf("gitl init outside a git repo: %v", err)
	}
	target := filepath.Join(dir, ".gitl.yaml")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected %s to exist: %v", target, err)
	}
	loadGenerated(t, dir)
}

// TestInitRejectsArgs asserts cobra.NoArgs rejects positional arguments.
func TestInitRejectsArgs(t *testing.T) {
	dir := t.TempDir()
	if _, err := runInitInDir(t, dir, "extra-arg"); err == nil {
		t.Fatal("expected an error for a positional argument")
	}
}

// TestInitTemplateHasNoActiveAPIKey is a regression guard: the template must
// never contain an active (uncommented) api_key key that would invite users to
// commit a secret.
func TestInitTemplateHasNoActiveAPIKey(t *testing.T) {
	active := regexp.MustCompile(`(?m)^\s*api_key\s*:`)
	if active.MatchString(starterConfig) {
		t.Fatal("starter template contains an active api_key key; keys must come from GITL_API_KEY only")
	}
}
