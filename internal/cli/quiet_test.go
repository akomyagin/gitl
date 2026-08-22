package cli

// Tests for the U9 --quiet / GITL_QUIET / output.quiet suppression of the
// informational offline banners. Suppression must affect ONLY the two stderr
// banners (the offline-review notice and the changelog --ai fallback notice) —
// never errors, the rendered stdout output, or the --fail-on gate.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akomyagin/gitl/internal/config"
)

const offlineBanner = "using deterministic offline review"

// unsetQuietEnv guarantees GITL_QUIET and GITL_OUTPUT_QUIET are absent for the
// duration of the test (t.Setenv registers restoration of the original values;
// Unsetenv after it yields a guaranteed-unset state — same pattern as the
// NO_COLOR tests in term_test.go). Tests that need the env var set call
// t.Setenv afterwards.
func unsetQuietEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GITL_QUIET", "GITL_OUTPUT_QUIET"} {
		t.Setenv(k, "sentinel")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

// runReviewCapture chdirs into dir and runs `gitl review` with the given args
// in an isolated config environment, returning BOTH stdout and stderr. Mirrors
// runReviewInDir (command_test.go), which discards stderr — the whole point
// here is asserting on it.
func runReviewCapture(t *testing.T, dir string, env map[string]string, args ...string) (string, string, error) {
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
	empty := filepath.Join(t.TempDir(), "none.yaml")

	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"review", "--config", empty}, args...))
	err = root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// TestReviewOfflineBannerSuppression: the offline banner is present by default
// (regression guard) and suppressed by each of the three layers — the --quiet
// flag, GITL_QUIET (presence, not value — an empty value still suppresses, the
// NO_COLOR semantics), and output.quiet: true in a repo .gitl.yaml.
func TestReviewOfflineBannerSuppression(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		env        map[string]string
		repoConfig string // written to <repo>/.gitl.yaml when non-empty
		wantBanner bool
	}{
		{
			name:       "default: banner present",
			wantBanner: true,
		},
		{
			name:       "--quiet flag suppresses",
			args:       []string{"--quiet"},
			wantBanner: false,
		},
		{
			name:       "GITL_QUIET=1 suppresses",
			env:        map[string]string{"GITL_QUIET": "1"},
			wantBanner: false,
		},
		{
			name:       "GITL_QUIET empty value still suppresses (presence, not value)",
			env:        map[string]string{"GITL_QUIET": ""},
			wantBanner: false,
		},
		{
			name:       "output.quiet true in repo config suppresses",
			repoConfig: "output:\n  quiet: true\n",
			wantBanner: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetQuietEnv(t)
			dir := setupRepo(t, false)
			if tc.repoConfig != "" {
				if err := os.WriteFile(filepath.Join(dir, ".gitl.yaml"), []byte(tc.repoConfig), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{"GITL_API_KEY": ""}
			for k, v := range tc.env {
				env[k] = v
			}
			out, stderr, err := runReviewCapture(t, dir, env, append([]string{"HEAD~1..HEAD"}, tc.args...)...)
			if err != nil {
				t.Fatalf("review: %v\nstderr:\n%s", err, stderr)
			}
			if out == "" {
				t.Error("stdout is empty, want the rendered review")
			}
			if got := strings.Contains(stderr, offlineBanner); got != tc.wantBanner {
				t.Errorf("banner present = %v, want %v; stderr:\n%s", got, tc.wantBanner, stderr)
			}
		})
	}
}

// TestChangelogAIFallbackBannerQuiet: the --ai offline-fallback banner is
// present without --quiet and absent with it; the deterministic fallback
// changelog itself is rendered either way.
func TestChangelogAIFallbackBannerQuiet(t *testing.T) {
	unsetQuietEnv(t)
	t.Setenv("GITL_API_KEY", "")
	dir := setupChangelogRepo(t)

	out, stderr, err := runChangelogInDir(t, dir, "--ai")
	if err != nil {
		t.Fatalf("changelog --ai: %v", err)
	}
	if !strings.Contains(stderr, "falling back to the deterministic changelog") {
		t.Errorf("expected the fallback banner without --quiet, got:\n%s", stderr)
	}
	if !strings.Contains(out, "### Added") {
		t.Errorf("deterministic fallback output missing:\n%s", out)
	}

	out, stderr, err = runChangelogInDir(t, dir, "--ai", "--quiet")
	if err != nil {
		t.Fatalf("changelog --ai --quiet: %v", err)
	}
	if strings.Contains(stderr, "falling back to the deterministic changelog") {
		t.Errorf("--quiet must suppress the fallback banner, got:\n%s", stderr)
	}
	if !strings.Contains(out, "### Added") {
		t.Errorf("--quiet must not affect the fallback changelog on stdout:\n%s", out)
	}
}

// TestQuietDoesNotSwallowErrors: --quiet suppresses the informational banner
// only — a real configuration error (keyed config with an unknown provider,
// rejected by llm.NewClient inside newNetworkClient) still fails the command.
func TestQuietDoesNotSwallowErrors(t *testing.T) {
	unsetQuietEnv(t)
	dir := setupRepo(t, false)
	env := map[string]string{"GITL_API_KEY": "test-key"} // keyed → network path

	_, stderr, err := runReviewCapture(t, dir, env, "HEAD~1..HEAD", "--quiet", "--provider=bogus")
	if err == nil {
		t.Fatal("expected an error for unknown provider under --quiet, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name the bad provider, got: %v", err)
	}
	if strings.Contains(stderr, offlineBanner) {
		t.Errorf("offline banner must not appear on a keyed config:\n%s", stderr)
	}
}

// TestQuietStdoutByteIdentical: --quiet only changes stderr — the rendered
// offline review on stdout is byte-identical with and without it (the offline
// provider is deterministic and the md render carries no timestamp), mirroring
// the U2 "stdout provably byte-identical" guard.
func TestQuietStdoutByteIdentical(t *testing.T) {
	unsetQuietEnv(t)
	dir := setupRepo(t, false)
	env := map[string]string{"GITL_API_KEY": ""}

	loud, _, err := runReviewCapture(t, dir, env, "HEAD~1..HEAD", "--format=md")
	if err != nil {
		t.Fatalf("review without --quiet: %v", err)
	}
	quiet, _, err := runReviewCapture(t, dir, env, "HEAD~1..HEAD", "--format=md", "--quiet")
	if err != nil {
		t.Fatalf("review with --quiet: %v", err)
	}
	if loud != quiet {
		t.Errorf("stdout must be byte-identical with and without --quiet:\nwithout:\n%q\nwith:\n%q", loud, quiet)
	}
}

// TestRunReviewCoreQuietFromConfig: the MCP path has no flags — RunReviewCore
// derives quiet from cfg.Output.Quiet (output.quiet in the server's config),
// and the default config keeps the banner (already asserted in
// TestRunReviewCoreOfflineReturnsArtifact; re-checked here as the pair).
func TestRunReviewCoreQuietFromConfig(t *testing.T) {
	unsetQuietEnv(t)
	t.Setenv("GITL_API_KEY", "")

	load := func(repoCfg string) *config.Config {
		t.Helper()
		repoDir := t.TempDir()
		if repoCfg != "" {
			if err := os.WriteFile(filepath.Join(repoDir, ".gitl.yaml"), []byte(repoCfg), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cfg, err := config.Load(config.Options{
			RepoDir:      repoDir,
			PersonalPath: filepath.Join(t.TempDir(), "none.yaml"),
		})
		if err != nil {
			t.Fatalf("config.Load: %v", err)
		}
		return cfg
	}

	// output.quiet: true → no banner on the MCP server's stderr.
	var errOut bytes.Buffer
	art, err := RunReviewCore(context.Background(), load("output:\n  quiet: true\n"), coreRangeSource(), ReviewOptions{ErrOut: &errOut})
	if err != nil {
		t.Fatalf("RunReviewCore (quiet): %v", err)
	}
	if art.ReviewMarkdown == "" {
		t.Error("quiet run produced no review body")
	}
	if strings.Contains(errOut.String(), offlineBanner) {
		t.Errorf("output.quiet: true must suppress the banner on ErrOut, got: %q", errOut.String())
	}

	// Default config → banner present.
	errOut.Reset()
	if _, err := RunReviewCore(context.Background(), load(""), coreRangeSource(), ReviewOptions{ErrOut: &errOut}); err != nil {
		t.Fatalf("RunReviewCore (default): %v", err)
	}
	if !strings.Contains(errOut.String(), offlineBanner) {
		t.Errorf("default config must keep the banner on ErrOut, got: %q", errOut.String())
	}
}

// TestQuietKeepsFailOnGate: the --fail-on gate message and non-zero exit are
// NOT classed as informational — they survive --quiet, and the review is still
// printed before the gate fires.
func TestQuietKeepsFailOnGate(t *testing.T) {
	unsetQuietEnv(t)
	dir := setupRepo(t, true) // sensitive change → offline heuristic scores "high"
	env := map[string]string{"GITL_API_KEY": ""}

	out, stderr, err := runReviewCapture(t, dir, env, "HEAD~1..HEAD", "--quiet", "--fail-on=high")
	if err == nil {
		t.Fatal("expected the --fail-on=high gate to fail the command under --quiet")
	}
	if !strings.Contains(err.Error(), "--fail-on=high") {
		t.Errorf("gate error should mention the threshold, got: %v", err)
	}
	if out == "" {
		t.Error("the review must still be printed before the gate fires")
	}
	if strings.Contains(stderr, offlineBanner) {
		t.Errorf("--quiet must still suppress the banner on the gated run:\n%s", stderr)
	}
}
