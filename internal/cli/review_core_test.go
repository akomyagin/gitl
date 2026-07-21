package cli

// Direct unit tests for RunReviewCore — the cmd-free review entrypoint. They
// deliberately never construct a cobra.Command or a pflag.FlagSet: proving the
// core is callable from outside the CLI layer (a future MCP server) is the
// whole point of the extraction. Offline mode (empty API key) keeps every test
// deterministic and network-free.

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
)

// coreTestConfig loads a merged config exactly the way a non-CLI caller would:
// config.Load with Flags: nil (defaults→file→env still apply). The personal
// config path points at a non-existent file and RepoDir at an empty temp dir,
// so no host config leaks in; the API key is cleared to force offline mode.
func coreTestConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("GITL_API_KEY", "")
	cfg, err := config.Load(config.Options{
		RepoDir:      t.TempDir(),
		PersonalPath: filepath.Join(t.TempDir(), "none.yaml"),
		Flags:        nil, // the point: no pflag involved at all
	})
	if err != nil {
		t.Fatalf("config.Load(Flags: nil): %v", err)
	}
	return cfg
}

// coreRangeSource is a synthetic resolved range source — RunReviewCore operates
// on a diffSource, so no git repository is needed for these tests.
func coreRangeSource() diffSource {
	return diffSource{
		Commits: []gitlog.Commit{{
			Hash:    "abc1234def5678900000000000000000000000aa",
			Author:  "Test Author",
			Date:    time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC),
			Subject: "feat: add greeting",
			Files:   []gitlog.FileChange{{Status: "M", Path: "main.go"}},
		}},
		Diff: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n" +
			"@@ -1,1 +1,2 @@\n package main\n+var greeting = \"hi\"\n",
		Label: "HEAD~1..HEAD",
		Mode:  modeRange,
	}
}

func TestRunReviewCoreOfflineReturnsArtifact(t *testing.T) {
	cfg := coreTestConfig(t)
	var errOut bytes.Buffer

	art, err := RunReviewCore(context.Background(), cfg, coreRangeSource(), ReviewOptions{ErrOut: &errOut})
	if err != nil {
		t.Fatalf("RunReviewCore: %v", err)
	}

	if art.Range != "HEAD~1..HEAD" {
		t.Errorf("Range = %q, want %q", art.Range, "HEAD~1..HEAD")
	}
	if !art.Offline {
		t.Error("Offline = false, want true (no API key configured)")
	}
	switch art.RiskLevel {
	case "low", "medium", "high":
	default:
		t.Errorf("RiskLevel = %q, want low|medium|high", art.RiskLevel)
	}
	if art.Stats.Commits != 1 {
		t.Errorf("Stats.Commits = %d, want 1", art.Stats.Commits)
	}
	if art.ReviewMarkdown == "" {
		t.Error("ReviewMarkdown is empty, want the offline review body")
	}
	if !strings.Contains(errOut.String(), "no LLM API key configured") {
		t.Errorf("expected the offline-mode notice on ErrOut, got: %q", errOut.String())
	}
}

// TestRunReviewCoreIsDeterministicOffline: the offline provider is
// deterministic, so two core calls over the same source must agree on
// everything except GeneratedAt.
func TestRunReviewCoreIsDeterministicOffline(t *testing.T) {
	cfg := coreTestConfig(t)
	src := coreRangeSource()

	first, err := RunReviewCore(context.Background(), cfg, src, ReviewOptions{})
	if err != nil {
		t.Fatalf("first RunReviewCore: %v", err)
	}
	second, err := RunReviewCore(context.Background(), cfg, src, ReviewOptions{})
	if err != nil {
		t.Fatalf("second RunReviewCore: %v", err)
	}

	if first.RiskLevel != second.RiskLevel {
		t.Errorf("RiskLevel differs across runs: %q vs %q", first.RiskLevel, second.RiskLevel)
	}
	if first.ReviewMarkdown != second.ReviewMarkdown {
		t.Error("ReviewMarkdown differs across runs — offline core must be deterministic")
	}
	if first.Stats != second.Stats {
		t.Errorf("Stats differ across runs: %+v vs %+v", first.Stats, second.Stats)
	}
}

// TestRunReviewCoreStagedAllExcludedIsError: the exclude_globs shaping and the
// per-mode emptiness check live in the core, not the cobra wrapper. A staged
// diff whose only file matches an exclude glob (*.lock is a built-in default)
// must be a clear user error.
func TestRunReviewCoreStagedAllExcludedIsError(t *testing.T) {
	cfg := coreTestConfig(t)
	src := diffSource{
		Diff: "diff --git a/deps.lock b/deps.lock\n--- a/deps.lock\n+++ b/deps.lock\n" +
			"@@ -1,1 +1,2 @@\n v1\n+v2\n",
		Label:  "staged",
		Staged: true,
		Mode:   modeStaged,
	}

	_, err := RunReviewCore(context.Background(), cfg, src, ReviewOptions{})
	if err == nil {
		t.Fatal("expected an error for a fully excluded staged diff")
	}
	if !strings.Contains(err.Error(), "excluded by exclude_globs") {
		t.Errorf("error %q should mention exclude_globs", err)
	}
}

// TestRunReviewCoreCostGuardBlocks: the --max-cost-usd guard is enforced inside
// the core before any provider call. With an API key set (network mode) and a
// microscopic limit, the core must fail with the cost-guard error — proving no
// network path is reached (the fake key would otherwise fail differently).
func TestRunReviewCoreCostGuardBlocks(t *testing.T) {
	cfg := coreTestConfig(t)
	cfg.LLM.APIKey = "test-key-never-used"
	cfg.Cost.MaxCostUSD = 0.0000001

	_, err := RunReviewCore(context.Background(), cfg, coreRangeSource(), ReviewOptions{NoCache: true})
	if err == nil {
		t.Fatal("expected the cost guard to block the request")
	}
	if !strings.Contains(err.Error(), "estimated cost") || !strings.Contains(err.Error(), "max-cost-usd") {
		t.Errorf("error %q should be the cost-guard message", err)
	}
}
