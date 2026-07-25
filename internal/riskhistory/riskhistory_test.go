package riskhistory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// TestDataDirResolution: the resolver must honor an absolute XDG_DATA_HOME on
// any OS, and fall back to ~/.local/share on non-Windows. Host isolation via
// t.Setenv, the same approach the cli tests use for XDG_CACHE_HOME.
func TestDataDirResolution(t *testing.T) {
	t.Run("xdg data home set", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		log, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		want := filepath.Join(xdg, "gitl", "risk-history.jsonl")
		if log.path != want {
			t.Errorf("path = %q, want %q", log.path, want)
		}
	})

	t.Run("relative xdg data home is ignored", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("home fallback assertion is unix-shaped")
		}
		t.Setenv("XDG_DATA_HOME", "relative/not-absolute")
		home := t.TempDir()
		t.Setenv("HOME", home)
		log, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		want := filepath.Join(home, ".local", "share", "gitl", "risk-history.jsonl")
		if log.path != want {
			t.Errorf("path = %q, want %q", log.path, want)
		}
	})

	t.Run("home fallback", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("~/.local/share fallback does not apply on windows")
		}
		t.Setenv("XDG_DATA_HOME", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		log, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		want := filepath.Join(home, ".local", "share", "gitl", "risk-history.jsonl")
		if log.path != want {
			t.Errorf("path = %q, want %q", log.path, want)
		}
	})
}

func testRecord(ts time.Time, repo, rng, level string) Record {
	return Record{
		SchemaVersion: RecordSchemaVersion,
		Timestamp:     ts,
		Repo:          repo,
		Range:         rng,
		RiskLevel:     level,
		RiskSummary:   "summary of " + rng,
		Provider:      "openai",
		Model:         "gpt-4o-mini",
		Heuristic:     true,
		Offline:       true,
	}
}

func TestAppendLoadRoundTrip(t *testing.T) {
	t.Parallel()
	log := NewInDir(t.TempDir())

	base := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	recs := []Record{
		testRecord(base, "https://example.com/a/b", "HEAD~5..HEAD", "low"),
		testRecord(base.Add(time.Hour), "https://example.com/a/b", "pr/42", "high"),
		testRecord(base.Add(2*time.Hour), "https://example.com/a/b", "staged", "medium"),
	}
	for _, r := range recs {
		if err := log.Append(r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := log.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(recs) {
		t.Fatalf("got %d records, want %d", len(got), len(recs))
	}
	for i, want := range recs {
		if !got[i].Timestamp.Equal(want.Timestamp) {
			t.Errorf("record %d: Timestamp = %v, want %v (chronological file order)", i, got[i].Timestamp, want.Timestamp)
		}
		got[i].Timestamp = want.Timestamp // normalize wall-clock representation for the struct compare
		if got[i] != want {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	log := NewInDir(t.TempDir())
	got, err := log.Load()
	if err != nil {
		t.Fatalf("Load on a missing file must not error, got: %v", err)
	}
	if got != nil {
		t.Errorf("Load on a missing file = %v, want nil", got)
	}
}

// TestLoadSkipsMalformedAndNewerSchema: a corrupt line and a future-schema
// line are skipped (best-effort history), the valid records survive.
func TestLoadSkipsMalformedAndNewerSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log := NewInDir(dir)

	base := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	if err := log.Append(testRecord(base, "repo", "r1..r2", "low")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// A malformed line and a newer-schema line, injected by hand.
	future, _ := json.Marshal(testRecord(base.Add(time.Hour), "repo", "r2..r3", "high"))
	future = []byte(strings.Replace(string(future), `"schema_version":1`, `"schema_version":99`, 1))
	f, err := os.OpenFile(filepath.Join(dir, "risk-history.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not json at all\n" + string(future) + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(testRecord(base.Add(2*time.Hour), "repo", "r3..r4", "medium")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := log.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2 (malformed + newer-schema skipped): %+v", len(got), got)
	}
	if got[0].Range != "r1..r2" || got[1].Range != "r3..r4" {
		t.Errorf("wrong survivors: %+v", got)
	}
}

func TestAggregate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	const repo = "https://example.com/me/proj"
	days := 14
	// Window: [now-14d, now]; midpoint at now-7d.
	recent := func(d int, level string) Record { // d days before now (recent half for d < 7)
		return testRecord(now.Add(-time.Duration(d)*24*time.Hour), repo, "HEAD~1..HEAD", level)
	}

	tests := []struct {
		name         string
		records      []Record
		wantTotal    int
		wantCounts   map[string]int
		wantRecent   int      // RecentHigh
		wantPrevious int      // PreviousHigh
		wantList     []string // expected Recent ranges, newest first
	}{
		{
			name:       "empty input",
			records:    nil,
			wantTotal:  0,
			wantCounts: map[string]int{},
		},
		{
			name: "other repos filtered out",
			records: []Record{
				testRecord(now.Add(-time.Hour), "https://example.com/other/repo", "HEAD~1..HEAD", "high"),
				recent(1, "low"),
			},
			wantTotal:  1,
			wantCounts: map[string]int{"low": 1},
			wantList:   []string{"HEAD~1..HEAD"},
		},
		{
			name: "outside window excluded",
			records: []Record{
				recent(20, "high"), // older than now-14d
				testRecord(now.Add(time.Hour), repo, "future..range", "high"), // after now
				recent(2, "medium"),
			},
			wantTotal:  1,
			wantCounts: map[string]int{"medium": 1},
			wantList:   []string{"HEAD~1..HEAD"},
		},
		{
			name: "recent vs previous half split",
			records: []Record{
				recent(10, "high"), // older half
				recent(9, "high"),  // older half
				recent(3, "high"),  // recent half
				recent(2, "low"),
			},
			wantTotal:    4,
			wantCounts:   map[string]int{"high": 3, "low": 1},
			wantRecent:   1,
			wantPrevious: 2,
			wantList:     []string{"HEAD~1..HEAD", "HEAD~1..HEAD", "HEAD~1..HEAD", "HEAD~1..HEAD"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Aggregate(tc.records, repo, days, now)
			if got.Days != days || got.RepoKey != repo {
				t.Errorf("Days/RepoKey = %d/%q, want %d/%q", got.Days, got.RepoKey, days, repo)
			}
			if got.Total != tc.wantTotal {
				t.Errorf("Total = %d, want %d", got.Total, tc.wantTotal)
			}
			if got.Counts == nil {
				t.Fatal("Counts must never be nil, even for an empty window")
			}
			if len(got.Counts) != len(tc.wantCounts) {
				t.Errorf("Counts = %v, want %v", got.Counts, tc.wantCounts)
			}
			for level, n := range tc.wantCounts {
				if got.Counts[level] != n {
					t.Errorf("Counts[%q] = %d, want %d", level, got.Counts[level], n)
				}
			}
			if got.RecentHigh != tc.wantRecent || got.PreviousHigh != tc.wantPrevious {
				t.Errorf("RecentHigh/PreviousHigh = %d/%d, want %d/%d", got.RecentHigh, got.PreviousHigh, tc.wantRecent, tc.wantPrevious)
			}
			if len(got.Recent) != len(tc.wantList) {
				t.Errorf("len(Recent) = %d, want %d", len(got.Recent), len(tc.wantList))
			}
			for i := 1; i < len(got.Recent); i++ {
				if got.Recent[i].Timestamp.After(got.Recent[i-1].Timestamp) {
					t.Errorf("Recent not newest-first at %d: %v after %v", i, got.Recent[i].Timestamp, got.Recent[i-1].Timestamp)
				}
			}
		})
	}
}

// TestAggregateRecentListTruncation: more than recentListLimit matching
// records → Recent holds exactly the 5 newest, newest first.
func TestAggregateRecentListTruncation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	const repo = "key"
	var records []Record
	for i := 0; i < 8; i++ {
		rec := testRecord(now.Add(-time.Duration(8-i)*time.Hour), repo, "", "low")
		rec.Range = string(rune('a' + i)) // a (oldest) .. h (newest)
		records = append(records, rec)
	}

	got := Aggregate(records, repo, 7, now)
	if got.Total != 8 {
		t.Fatalf("Total = %d, want 8", got.Total)
	}
	if len(got.Recent) != recentListLimit {
		t.Fatalf("len(Recent) = %d, want %d", len(got.Recent), recentListLimit)
	}
	want := []string{"h", "g", "f", "e", "d"}
	for i, w := range want {
		if got.Recent[i].Range != w {
			t.Errorf("Recent[%d].Range = %q, want %q", i, got.Recent[i].Range, w)
		}
	}
}

// gitEnv/runGit mirror the temp-repo pattern of the cli/gitlog test suites:
// isolate git from user/system config so tests are hermetic.
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

// TestRepoKey: origin remote URL (normalized) is preferred; without an origin
// remote the key falls back to the worktree root.
func TestRepoKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")

	runner, err := gitlog.NewRunner(dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	// No origin remote → worktree root fallback. Compare against git's own
	// notion of the root (symlink normalization, e.g. /tmp vs /private/tmp).
	root, err := runner.TopLevel(ctx)
	if err != nil {
		t.Fatalf("TopLevel: %v", err)
	}
	if key := RepoKey(ctx, runner); key != root {
		t.Errorf("RepoKey without origin = %q, want worktree root %q", key, root)
	}

	// With an origin remote → normalized URL (trimmed, .git suffix dropped).
	runGit(t, dir, "remote", "add", "origin", "https://example.com/me/proj.git")
	if key := RepoKey(ctx, runner); key != "https://example.com/me/proj" {
		t.Errorf("RepoKey with origin = %q, want %q", key, "https://example.com/me/proj")
	}

	// Total failure tolerance: a nil runner yields "" — never a panic.
	if key := RepoKey(ctx, nil); key != "" {
		t.Errorf("RepoKey(nil runner) = %q, want empty", key)
	}
}

// TestConcurrentAppends: two goroutines appending to the same log must produce
// only complete, valid JSON lines (O_APPEND single-write atomicity — run under
// -race).
func TestConcurrentAppends(t *testing.T) {
	t.Parallel()
	log := NewInDir(t.TempDir())
	const perWriter = 25

	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				rec := testRecord(time.Now().UTC(), "repo", "HEAD~1..HEAD", "low")
				rec.RiskSummary = strings.Repeat("x", 100) // non-trivial line size
				if err := log.Append(rec); err != nil {
					t.Errorf("writer %d Append: %v", writer, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Every line must be independently valid JSON — no interleaved/torn lines.
	data, err := os.ReadFile(log.path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := 0
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("torn/interleaved line %q: %v", line, err)
		}
		lines++
	}
	if lines != 2*perWriter {
		t.Errorf("got %d lines, want %d", lines, 2*perWriter)
	}

	got, err := log.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2*perWriter {
		t.Errorf("Load returned %d records, want %d", len(got), 2*perWriter)
	}
}
