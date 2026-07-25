package cli

// Direct unit tests for RunDigestCore — the cmd-free digest entrypoint. They
// deliberately never construct a cobra.Command or a pflag.FlagSet: proving the
// core is callable from outside the CLI layer (a future MCP server) is the
// whole point of the extraction. digest never calls an LLM, so the tests only
// need real (temp) git repositories.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/riskhistory"
)

func TestRunDigestCoreExplicitRepos(t *testing.T) {
	dir := setupDigestRepo(t, "feat: core digest")
	cfg := coreTestConfig(t)

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7, Repos: []string{dir}})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}

	if art.Days != 7 {
		t.Errorf("Days = %d, want 7", art.Days)
	}
	if !art.Until.Equal(art.GeneratedAt) {
		t.Errorf("Until = %v, want GeneratedAt %v", art.Until, art.GeneratedAt)
	}
	if len(art.Repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(art.Repos))
	}
	repo := art.Repos[0]
	if !repo.Ok {
		t.Fatalf("repo not Ok: %s", repo.Err)
	}
	if repo.Path != dir {
		t.Errorf("Path = %q, want %q", repo.Path, dir)
	}
	if repo.Commits != 1 {
		t.Errorf("Commits = %d, want 1", repo.Commits)
	}
	if len(repo.ByTopic) != 1 || repo.ByTopic[0].Topic != "feat" {
		t.Errorf("ByTopic = %+v, want a single feat topic", repo.ByTopic)
	}
}

func TestRunDigestCoreInvalidDays(t *testing.T) {
	cfg := coreTestConfig(t)

	_, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 0})
	if err == nil {
		t.Fatal("expected Days=0 to be rejected")
	}
	if !strings.Contains(err.Error(), "--days must be a positive integer") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestRunDigestCoreMissingRepoIsPerRepoError: a repository that does not exist
// must surface as a per-repo failure inside the artifact, not abort the whole
// digest (graceful degradation, §10.4).
func TestRunDigestCoreMissingRepoIsPerRepoError(t *testing.T) {
	good := setupDigestRepo(t, "fix: still works")
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg := coreTestConfig(t)

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7, Repos: []string{good, missing}})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}
	if len(art.Repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(art.Repos))
	}

	byPath := map[string]bool{}
	for _, r := range art.Repos {
		byPath[r.Path] = r.Ok
		if !r.Ok && r.Err == "" {
			t.Errorf("failed repo %q carries no error message", r.Path)
		}
	}
	if !byPath[good] {
		t.Errorf("repo %q should have succeeded", good)
	}
	if byPath[missing] {
		t.Errorf("repo %q should have failed", missing)
	}
}

// seedRiskHistory isolates the riskhistory data dir via XDG_DATA_HOME (honored
// on every OS, so writer and reader resolve to the same hermetic temp location
// with no code seam) and appends one record for the given repo, keyed exactly
// the way the digest reader will key it (riskhistory.RepoKey over a Runner at
// repoDir).
func seedRiskHistory(t *testing.T, repoDir, level string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	runner, err := gitlog.NewRunner(repoDir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	log, err := riskhistory.New()
	if err != nil {
		t.Fatalf("riskhistory.New: %v", err)
	}
	rec := riskhistory.Record{
		SchemaVersion: riskhistory.RecordSchemaVersion,
		Timestamp:     time.Now().UTC(),
		Repo:          riskhistory.RepoKey(context.Background(), runner),
		Range:         "HEAD~1..HEAD",
		RiskLevel:     level,
		RiskSummary:   "seeded",
		Provider:      "openai",
		Model:         "gpt-4o-mini",
		Heuristic:     true,
		Offline:       true,
	}
	if err := log.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// TestRunDigestCoreAttachesRiskTrend: with one matching history record the
// repo's digest carries a RiskTrend; the counts reflect the record.
func TestRunDigestCoreAttachesRiskTrend(t *testing.T) {
	dir := setupDigestRepo(t, "feat: with history")
	seedRiskHistory(t, dir, "high")
	cfg := coreTestConfig(t)

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7, Repos: []string{dir}})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}
	if len(art.Repos) != 1 || !art.Repos[0].Ok {
		t.Fatalf("unexpected repos: %+v", art.Repos)
	}
	trend := art.Repos[0].RiskTrend
	if trend == nil {
		t.Fatal("RiskTrend = nil, want the seeded record aggregated")
	}
	if trend.Total != 1 || trend.High != 1 {
		t.Errorf("trend = %+v, want Total=1 High=1", trend)
	}
	if len(trend.Recent) != 1 || trend.Recent[0].Range != "HEAD~1..HEAD" || trend.Recent[0].Level != "high" {
		t.Errorf("trend.Recent = %+v, want the seeded record", trend.Recent)
	}
}

// TestRunDigestCoreNoHistoryNoTrend: an empty history log (fresh XDG data dir)
// attaches no trend — and is never an error.
func TestRunDigestCoreNoHistoryNoTrend(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := setupDigestRepo(t, "feat: no history")
	cfg := coreTestConfig(t)

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7, Repos: []string{dir}})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}
	if art.Repos[0].RiskTrend != nil {
		t.Errorf("RiskTrend = %+v, want nil without history", art.Repos[0].RiskTrend)
	}
}

// TestRunDigestCoreRiskLogDisabledSkipsTrend: policy.risk_log_enabled: false
// must skip the history read entirely — no trend even when records exist.
func TestRunDigestCoreRiskLogDisabledSkipsTrend(t *testing.T) {
	dir := setupDigestRepo(t, "feat: opted out")
	seedRiskHistory(t, dir, "high")
	cfg := coreTestConfig(t)
	cfg.Policy.RiskLogEnabled = false

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7, Repos: []string{dir}})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}
	if art.Repos[0].RiskTrend != nil {
		t.Errorf("RiskTrend = %+v, want nil when risk_log_enabled is false", art.Repos[0].RiskTrend)
	}
}

// TestRunDigestCoreConfigReposFallback: with DigestOptions.Repos empty the core
// must fall back to cfg digest.repos — the same precedence the CLI has
// (--repos > digest.repos > current directory).
func TestRunDigestCoreConfigReposFallback(t *testing.T) {
	dir := setupDigestRepo(t, "feat: from config")
	cfg := coreTestConfig(t)
	cfg.Digest.Repos = []config.RepoRef{{Path: dir}}

	art, err := RunDigestCore(context.Background(), cfg, DigestOptions{Days: 7})
	if err != nil {
		t.Fatalf("RunDigestCore: %v", err)
	}
	if len(art.Repos) != 1 {
		t.Fatalf("got %d repos, want 1", len(art.Repos))
	}
	if art.Repos[0].Path != dir {
		t.Errorf("Path = %q, want the digest.repos entry %q", art.Repos[0].Path, dir)
	}
	if !art.Repos[0].Ok || art.Repos[0].Commits != 1 {
		t.Errorf("repo = %+v, want Ok with 1 commit", art.Repos[0])
	}
}
