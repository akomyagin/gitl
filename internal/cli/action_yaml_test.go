package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestActionYAMLDoesNotEchoSecrets is the cheap, best-effort static guard from
// docs/TECHNICAL_PLAN.md §12.5(3)/§12.8: it does not prove action.yml never
// leaks GITL_API_KEY (composite-action bash isn't otherwise unit-testable
// from Go), but it catches the most likely regressions by construction:
//   - `set -x` anywhere in a run: step that also references the secret env
//     var (it would echo every command line, including any that touch it);
//   - `echo`/`printf` of the secret env var;
//   - the secret input interpolated directly into a run: script
//     (${{ inputs.gitl-api-key }}) instead of being passed via env:, which
//     would put the raw value in the rendered shell command.
func TestActionYAMLDoesNotEchoSecrets(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "GITL_API_KEY: ${{ inputs.gitl-api-key }}") {
		t.Error("action.yml should pass the API key via env: GITL_API_KEY, not some other mechanism")
	}
	if strings.Contains(content, "${{ inputs.gitl-api-key }}") &&
		strings.Count(content, "${{ inputs.gitl-api-key }}") > 1 {
		t.Error("gitl-api-key must be referenced exactly once (in env:) — any other reference risks interpolating the secret into a run: script")
	}
	if regexp.MustCompile(`(?m)^\s*set\s+-\S*x\S*`).MatchString(content) {
		t.Error("action.yml must not `set -x` (it would echo commands, including any touching the secret env var, to the log)")
	}
	for _, pattern := range []string{
		`echo\s+"?\$GITL_API_KEY`,
		`echo\s+"?\$\{GITL_API_KEY`,
		`printf\s+.*\$GITL_API_KEY`,
	} {
		if regexp.MustCompile(pattern).MatchString(content) {
			t.Errorf("action.yml appears to echo/printf the secret env var (pattern %q matched)", pattern)
		}
	}
}

// repoRoot walks up from this test file's directory to find go.mod, so the
// test works regardless of the working directory `go test` is invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod) walking up from test file")
		}
		dir = parent
	}
}
