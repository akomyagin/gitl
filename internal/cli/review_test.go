package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/render"
	"github.com/akomyagin/gitl/internal/riskhistory"
)

func TestMatchesAnyGlob(t *testing.T) {
	t.Parallel()
	globs := []string{"*.lock", "*.min.js", "vendor/**", "*.svg", "migrations/**"}
	tests := []struct {
		path string
		want bool
	}{
		{"go.sum", false},
		{"package-lock.json", false},
		{"yarn.lock", true},
		{"deep/dir/yarn.lock", true},
		{"app.min.js", true},
		{"vendor/x/y.go", true},
		{"vendorish/x.go", false},
		{"icon.svg", true},
		{"migrations/0001.sql", true},
		{"internal/app.go", false},
	}
	for _, tc := range tests {
		if got := matchesAnyGlob(tc.path, globs); got != tc.want {
			t.Errorf("matchesAnyGlob(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestFilterDiffByGlobs(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/app.go b/app.go\n" +
		"index 111..222 100644\n" +
		"--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old\n+new\n" +
		"diff --git a/yarn.lock b/yarn.lock\n" +
		"index 333..444 100644\n" +
		"--- a/yarn.lock\n+++ b/yarn.lock\n@@ -1 +1 @@\n-x\n+y\n"

	out := filterDiffByGlobs(diff, []string{"*.lock"})
	if strings.Contains(out, "yarn.lock") {
		t.Errorf("excluded file still present:\n%s", out)
	}
	if !strings.Contains(out, "app.go") {
		t.Errorf("kept file was dropped:\n%s", out)
	}
}

// TestFilterDiffByGlobsContentContainingHeaderText is the regression test for
// the empirically-reproduced substring-split bug: a KEPT file whose ADDED
// content contains the literal text "diff --git " used to split the section
// mid-way, and an EXCLUDED file's content could survive filtering and reach
// the LLM prompt. Section boundaries must be line-anchored.
func TestFilterDiffByGlobsContentContainingHeaderText(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/doc.md b/doc.md\n" +
		"index 111..222 100644\n" +
		"--- a/doc.md\n+++ b/doc.md\n@@ -1 +1,2 @@\n" +
		" intro\n" +
		"+here is an example: diff --git a/x b/y\n" + // content, NOT a boundary
		"diff --git a/secrets.lock b/secrets.lock\n" +
		"index 333..444 100644\n" +
		"--- a/secrets.lock\n+++ b/secrets.lock\n@@ -1 +1 @@\n" +
		"-old-secret\n+SUPER_SECRET_VALUE\n"

	out := filterDiffByGlobs(diff, []string{"*.lock"})

	// The excluded file's content must be COMPLETELY absent — this is the
	// security property policy.exclude_globs exists for.
	for _, leaked := range []string{"secrets.lock", "SUPER_SECRET_VALUE", "old-secret"} {
		if strings.Contains(out, leaked) {
			t.Errorf("excluded file content leaked past the glob filter (%q):\n%s", leaked, out)
		}
	}
	// The kept file survives intact, including the tricky content line.
	if !strings.Contains(out, "here is an example: diff --git a/x b/y") {
		t.Errorf("kept file content was dropped:\n%s", out)
	}
}

// TestFilterDiffByGlobsExcludedFileContentTrailsFakeBoundary is the actual
// empirically-reproduced leak vector for the substring-split bug (found
// during independent review of this fix): the EXCLUDED file's own added
// content contains a mid-line "diff --git a/<kept> b/<kept>" occurrence,
// followed on subsequent lines by more of the excluded file's real content.
// Under the old non-line-anchored split, that mid-line occurrence became a
// false section boundary, and everything after it — including the secret
// value on the next line — was reattached to a fabricated "kept" section
// and survived filtering. (The sibling test above, with the marker in a
// KEPT file's content, does not exercise this path: the old code happened
// to still resolve the excluded file's real header correctly in that
// arrangement.)
func TestFilterDiffByGlobsExcludedFileContentTrailsFakeBoundary(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/secrets.lock b/secrets.lock\n" +
		"index 111..222 100644\n" +
		"--- a/secrets.lock\n+++ b/secrets.lock\n@@ -1 +2 @@\n" +
		"+prefix diff --git a/keep.go b/keep.go\n" + // fake boundary inside excluded content
		"+SUPER_SECRET_VALUE_TRAILS\n" // must not survive attached to the fake header

	out := filterDiffByGlobs(diff, []string{"*.lock"})

	for _, leaked := range []string{"secrets.lock", "SUPER_SECRET_VALUE_TRAILS", "keep.go"} {
		if strings.Contains(out, leaked) {
			t.Errorf("excluded file content leaked past the glob filter (%q):\n%s", leaked, out)
		}
	}
}

// TestFilterDiffByGlobsQuotedPath: git quotes non-ASCII filenames in diff
// headers by default (core.quotepath=true). The quoted header must still be
// decoded so the exclude glob matches — previously such files silently
// escaped exclusion.
func TestFilterDiffByGlobsQuotedPath(t *testing.T) {
	t.Parallel()
	// "данные" in UTF-8, octal-escaped per git's core.quotePath output.
	diff := "diff --git \"a/\\320\\264\\320\\260\\320\\275\\320\\275\\321\\213\\320\\265.lock\" \"b/\\320\\264\\320\\260\\320\\275\\320\\275\\321\\213\\320\\265.lock\"\n" +
		"index 111..222 100644\n" +
		"--- \"a/\\320\\264\\320\\260\\320\\275\\320\\275\\321\\213\\320\\265.lock\"\n" +
		"+++ \"b/\\320\\264\\320\\260\\320\\275\\320\\275\\321\\213\\320\\265.lock\"\n" +
		"@@ -1 +1 @@\n-x\n+y\n" +
		"diff --git a/app.go b/app.go\n" +
		"--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old\n+new\n"

	out := filterDiffByGlobs(diff, []string{"*.lock"})
	if strings.Contains(out, ".lock") {
		t.Errorf("quoted-path .lock file was not excluded:\n%s", out)
	}
	if !strings.Contains(out, "app.go") {
		t.Errorf("kept file was dropped:\n%s", out)
	}
}

func TestTruncateDiff(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 100)
	out := truncateDiff(long, 40)
	if !strings.Contains(out, "[... diff truncated ...]") {
		t.Errorf("missing truncation marker:\n%s", out)
	}
	if len(out) <= 40 || !strings.HasPrefix(out, strings.Repeat("x", 40)) {
		t.Errorf("truncation boundary wrong: %q", out)
	}
	// Under the limit: unchanged, no marker.
	if got := truncateDiff("short", 40); got != "short" {
		t.Errorf("short diff was altered: %q", got)
	}
	// Disabled (<=0): unchanged.
	if got := truncateDiff(long, 0); got != long {
		t.Errorf("maxBytes=0 should disable truncation")
	}
}

// TestClassifyReviewArg: the mode classifier for the review positional
// argument. Only a matching-but-invalid PR number (pr/0) is an error;
// non-matching arguments (pr/-1, pr/abc, plain ranges) are ranges, never
// errors — they fail later with the natural git error if bogus.
func TestClassifyReviewArg(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arg     string
		isPR    bool
		prNum   int
		wantErr bool
	}{
		{"pr/42", true, 42, false},
		{"pr/1", true, 1, false},
		{"pr/0", false, 0, true},
		{"pr/-1", false, 0, false},  // no pattern match → range
		{"pr/abc", false, 0, false}, // no pattern match → range
		{"pr/", false, 0, false},
		{"HEAD~1..HEAD", false, 0, false},
		{"main..feature", false, 0, false},
	}
	for _, tc := range tests {
		isPR, prNum, err := classifyReviewArg(tc.arg)
		if (err != nil) != tc.wantErr {
			t.Errorf("classifyReviewArg(%q) err = %v, wantErr %v", tc.arg, err, tc.wantErr)
			continue
		}
		if tc.wantErr && !strings.Contains(err.Error(), "positive integer") {
			t.Errorf("classifyReviewArg(%q) error should mention positive integer, got: %v", tc.arg, err)
		}
		if isPR != tc.isPR || prNum != tc.prNum {
			t.Errorf("classifyReviewArg(%q) = (%v, %d), want (%v, %d)", tc.arg, isPR, prNum, tc.isPR, tc.prNum)
		}
	}
}

func TestBuildArtifactStats(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		LLM: config.LLMConfig{Provider: "openai", Model: "gpt-4o-mini", APIKey: "k"},
	}
	commits := []gitlog.Commit{
		{Hash: "h1", Author: "A", Subject: "s1", Files: []gitlog.FileChange{{Status: "M", Path: "a.go"}}},
		{Hash: "h2", Author: "B", Subject: "s2", Files: []gitlog.FileChange{{Status: "M", Path: "a.go"}, {Status: "A", Path: "b.go"}}},
	}
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1,2 @@\n+added1\n+added2\n-removed1\n"
	resp := llm.Response{Content: "review", Risk: llm.Risk{Level: "medium", Summary: "sum"}}

	art := buildArtifact(cfg, "r..s", commits, diff, resp)
	if art.Stats.Commits != 2 {
		t.Errorf("commits = %d, want 2", art.Stats.Commits)
	}
	// FilesChanged counts the diff's sections (a.go only) — the same view the
	// line stats use — NOT the commit metadata (which also lists b.go). See
	// TestBuildArtifactStatsFilteredDiff for the exclude_globs cases.
	if art.Stats.FilesChanged != 1 {
		t.Errorf("files_changed = %d, want 1 (from diff headers)", art.Stats.FilesChanged)
	}
	if art.Stats.LinesAdded != 2 || art.Stats.LinesRemoved != 1 {
		t.Errorf("lines +%d/-%d, want +2/-1", art.Stats.LinesAdded, art.Stats.LinesRemoved)
	}
	if art.RiskLevel != "medium" || art.Offline {
		t.Errorf("artifact = %+v", art)
	}
	// Round-trips through the JSON renderer.
	var b strings.Builder
	if err := render.Render(&b, art, render.FormatJSON); err != nil {
		t.Fatalf("render json: %v", err)
	}
}

// TestBuildArtifactStatsFilteredDiff: Stats.FilesChanged must reflect the
// FILTERED diff (post exclude_globs) — the same view LinesAdded/LinesRemoved
// already use — not the unfiltered commit metadata. The one exception is a
// metadata-only range review (empty filtered diff, non-empty commits — a
// reachable modeRange state, see prepareReview), which keeps the historical
// commit-metadata count instead of regressing to zero.
func TestBuildArtifactStatsFilteredDiff(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		LLM: config.LLMConfig{Provider: "openai", Model: "gpt-4o-mini", APIKey: "k"},
	}
	resp := llm.Response{Content: "review", Risk: llm.Risk{Level: "low", Summary: "sum"}}
	globs := []string{"*.lock", "vendor/**"}

	section := func(p string) string {
		return "diff --git a/" + p + " b/" + p + "\n--- a/" + p + "\n+++ b/" + p + "\n@@ -1 +1 @@\n-old\n+new\n"
	}
	kept := []string{"main.go", "internal/util.go"}
	excluded := []string{"a.lock", "b.lock", "c.lock", "vendor/x.go", "vendor/y.go", "vendor/z.go"}

	// Two commits touching 8 distinct files (2 kept + 6 excluded).
	toChanges := func(paths []string) []gitlog.FileChange {
		fc := make([]gitlog.FileChange, 0, len(paths))
		for _, p := range paths {
			fc = append(fc, gitlog.FileChange{Status: "M", Path: p})
		}
		return fc
	}
	commits := []gitlog.Commit{
		{Hash: "h1", Author: "A", Subject: "s1", Files: toChanges(append(append([]string{}, kept...), excluded[:3]...))},
		{Hash: "h2", Author: "B", Subject: "s2", Files: toChanges(excluded[3:])},
	}

	var raw strings.Builder
	for _, p := range append(append([]string{}, kept...), excluded...) {
		raw.WriteString(section(p))
	}
	var excludedOnly strings.Builder
	for _, p := range excluded {
		excludedOnly.WriteString(section(p))
	}

	tests := []struct {
		name string
		diff string // already shaped, as buildArtifact receives it (plan.diff)
		want int
	}{
		{
			name: "filtered diff wins over unfiltered commit metadata",
			diff: filterDiffByGlobs(raw.String(), globs),
			want: 2, // only the kept files — consistent with the line stats
		},
		{
			name: "metadata-only fallback keeps commit-based count",
			diff: filterDiffByGlobs(excludedOnly.String(), globs), // fully excluded → ""
			want: 8,                                               // pre-fix ChangedFileCount(commits) behavior, not zero
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			art := buildArtifact(cfg, "r..s", commits, tc.diff, resp)
			if art.Stats.FilesChanged != tc.want {
				t.Errorf("files_changed = %d, want %d", art.Stats.FilesChanged, tc.want)
			}
		})
	}
}

// TestBuildArtifactStatsStaged: in staged mode there are no commits, so
// FilesChanged must come from the diff headers (gitlog.DiffFileCount), not
// from commit metadata (which is empty/nil).
func TestBuildArtifactStatsStaged(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		LLM: config.LLMConfig{Provider: "openai", Model: "gpt-4o-mini", APIKey: "k"},
	}
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n+added1\n" +
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n+added2\n"
	resp := llm.Response{Content: "review", Risk: llm.Risk{Level: "low", Summary: "sum"}}

	art := buildArtifact(cfg, "staged", nil, diff, resp)
	if art.Stats.Commits != 0 {
		t.Errorf("commits = %d, want 0 (nothing committed yet)", art.Stats.Commits)
	}
	if art.Stats.FilesChanged != 2 {
		t.Errorf("files_changed = %d, want 2 (from diff headers)", art.Stats.FilesChanged)
	}
	if art.Range != "staged" {
		t.Errorf("range = %q, want %q", art.Range, "staged")
	}
}

func TestByteCountWriter(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	cw := &byteCountWriter{w: &buf}
	if cw.written != 0 {
		t.Fatalf("initial written = %d, want 0", cw.written)
	}
	n, err := io.WriteString(cw, "hello")
	if err != nil || n != 5 {
		t.Fatalf("first WriteString: n=%d err=%v", n, err)
	}
	if cw.written != 5 {
		t.Errorf("written after first write = %d, want 5", cw.written)
	}
	_, _ = io.WriteString(cw, " world")
	if cw.written != 11 {
		t.Errorf("written after second write = %d, want 11", cw.written)
	}
	if buf.String() != "hello world" {
		t.Errorf("buf = %q, want %q", buf.String(), "hello world")
	}
}

func TestFailErrorMessage(t *testing.T) {
	t.Parallel()
	e := &failError{level: "high", threshold: "medium"}
	if !strings.Contains(e.Error(), "high") || !strings.Contains(e.Error(), "medium") {
		t.Errorf("failError message unhelpful: %q", e.Error())
	}
}

// riskHistoryTestDir points the riskhistory data-dir resolver at a temp dir
// via XDG_DATA_HOME (honored on every OS — checked before the platform
// branches), so BOTH the review writer and the digest reader resolve to the
// same hermetic location without any code seam. Returns the temp dir.
func riskHistoryTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	return dir
}

// TestReviewWritesRiskHistory: a successful offline review appends exactly one
// risk-history record with the review's level/range and the offline/heuristic
// markers.
func TestReviewWritesRiskHistory(t *testing.T) {
	dataDir := riskHistoryTestDir(t)
	dir := setupRepo(t, false)

	if _, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "HEAD~1..HEAD"); err != nil {
		t.Fatalf("review: %v", err)
	}

	records, err := riskhistory.NewInDir(filepath.Join(dataDir, "gitl")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want exactly 1", len(records))
	}
	rec := records[0]
	if rec.SchemaVersion != riskhistory.RecordSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", rec.SchemaVersion, riskhistory.RecordSchemaVersion)
	}
	if rec.Range != "HEAD~1..HEAD" {
		t.Errorf("Range = %q, want HEAD~1..HEAD", rec.Range)
	}
	switch rec.RiskLevel {
	case "low", "medium", "high":
	default:
		t.Errorf("RiskLevel = %q, want low|medium|high", rec.RiskLevel)
	}
	if !rec.Offline || !rec.Heuristic {
		t.Errorf("Offline/Heuristic = %v/%v, want true/true for the offline heuristic path", rec.Offline, rec.Heuristic)
	}
	if rec.Repo == "" {
		t.Errorf("Repo key must be non-empty inside a git worktree (worktree-root fallback)")
	}
	if rec.Timestamp.IsZero() {
		t.Errorf("Timestamp must be set")
	}
}

// TestReviewDryRunWritesNoRiskHistory: --dry-run returns before any provider
// call — no risk outcome exists, so nothing may be logged.
func TestReviewDryRunWritesNoRiskHistory(t *testing.T) {
	dataDir := riskHistoryTestDir(t)
	dir := setupRepo(t, false)

	if _, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "HEAD~1..HEAD", "--dry-run"); err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	records, err := riskhistory.NewInDir(filepath.Join(dataDir, "gitl")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("dry-run must log nothing, got %d records: %+v", len(records), records)
	}
}

// TestReviewRiskLogDisabledWritesNothing: policy.risk_log_enabled: false (the
// single config-only opt-out) suppresses the write entirely.
func TestReviewRiskLogDisabledWritesNothing(t *testing.T) {
	dataDir := riskHistoryTestDir(t)
	dir := setupRepo(t, false)
	// Repo-level .gitl.yaml in the cwd is auto-loaded by loadConfig.
	if err := os.WriteFile(filepath.Join(dir, ".gitl.yaml"), []byte("policy:\n  risk_log_enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "HEAD~1..HEAD"); err != nil {
		t.Fatalf("review: %v", err)
	}

	records, err := riskhistory.NewInDir(filepath.Join(dataDir, "gitl")).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("risk_log_enabled: false must log nothing, got %d records", len(records))
	}
}

// TestReviewThenDigestShowsRiskTrend is the end-to-end pass: an offline
// `review` writes the history record, and a `digest --format=json` over the
// SAME repo and the same XDG_DATA_HOME reads it back as a risk_trend section.
func TestReviewThenDigestShowsRiskTrend(t *testing.T) {
	riskHistoryTestDir(t)
	dir := setupRepo(t, false)

	if _, err := runReviewInDir(t, dir, map[string]string{"GITL_API_KEY": ""}, "HEAD~1..HEAD"); err != nil {
		t.Fatalf("review: %v", err)
	}

	out, _, err := runDigestInDir(t, dir, "--format=json")
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !strings.Contains(out, `"risk_trend"`) {
		t.Fatalf("digest JSON missing risk_trend after a review:\n%s", out)
	}
	if !strings.Contains(out, `"total": 1`) {
		t.Errorf("risk_trend should count the single review:\n%s", out)
	}
	if !strings.Contains(out, `"range": "HEAD~1..HEAD"`) {
		t.Errorf("risk_trend recent list should carry the review's range:\n%s", out)
	}
}

// TestNewNetworkClientDispatchesByProvider verifies that newNetworkClient
// routes each llm.provider value to its concrete client type: the new native
// providers get their own implementations, everything OpenAI-shaped stays on
// *llm.Client, and an unknown provider is a configuration error.
func TestNewNetworkClientDispatchesByProvider(t *testing.T) {
	t.Parallel()

	newCfg := func(provider string) *config.Config {
		cfg := &config.Config{}
		cfg.LLM.Provider = provider
		cfg.LLM.APIKey = "test-key"
		cfg.LLM.BaseURL = "http://localhost:1"
		return cfg
	}

	if p, err := newNetworkClient(newCfg("anthropic")); err != nil {
		t.Errorf("anthropic: %v", err)
	} else if _, ok := p.(*llm.AnthropicClient); !ok {
		t.Errorf("anthropic dispatched to %T, want *llm.AnthropicClient", p)
	}

	if p, err := newNetworkClient(newCfg("gemini")); err != nil {
		t.Errorf("gemini: %v", err)
	} else if _, ok := p.(*llm.GeminiClient); !ok {
		t.Errorf("gemini dispatched to %T, want *llm.GeminiClient", p)
	}

	if p, err := newNetworkClient(newCfg("openai")); err != nil {
		t.Errorf("openai: %v", err)
	} else if _, ok := p.(*llm.Client); !ok {
		t.Errorf("openai dispatched to %T, want *llm.Client", p)
	}

	if _, err := newNetworkClient(newCfg("not-a-provider")); err == nil {
		t.Error("expected error for unknown provider, got nil")
	}

	// Both native clients must expose the RawCompleter capability so
	// changelog --ai works with them (probed via rawCompleterFor).
	for _, provider := range []string{"anthropic", "gemini"} {
		p, err := newNetworkClient(newCfg(provider))
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if _, err := rawCompleterFor(p, provider); err != nil {
			t.Errorf("rawCompleterFor(%s) must succeed, got: %v", provider, err)
		}
	}
}

// syncBuffer is a mutex-guarded bytes.Buffer, safe to write from the review
// goroutine while the test goroutine reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestBufferedReviewRendersBeforeCacheStore is the regression test for the
// buffered-path store ordering: the rendered review must reach the user BEFORE
// the cache store runs, so a slow remote-cache PUT (up to
// cache.remote.timeout_ms) can never delay the visible output. The remote
// cache's PUT handler blocks until the test releases it; the review runs in a
// goroutine, and the output is asserted non-empty while the PUT is still held.
func TestBufferedReviewRendersBeforeCacheStore(t *testing.T) {
	const reviewBody = "buffered review body before store"

	// Mock LLM endpoint (OpenAI-compatible), reusing the streaming test helper.
	llmSrv := httptest.NewServer(&oneShotJSONHandler{
		content: reviewBody + "\n```risk\n{\"level\":\"low\",\"summary\":\"ok\"}\n```",
	})
	t.Cleanup(llmSrv.Close)

	// Remote cache: GET is always a miss; PUT signals putStarted then blocks
	// until releaseCh is closed.
	putStarted := make(chan struct{})
	releaseCh := make(chan struct{})
	var putOnce sync.Once
	cacheSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			putOnce.Do(func() { close(putStarted) })
			<-releaseCh
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(cacheSrv.Close)

	dir := setupRepo(t, false)

	// All Setenv/Chdir on the main goroutine (t.Setenv forbids goroutine use).
	t.Setenv("GITL_API_KEY", "sk-fake-cache-order")
	t.Setenv("GITL_CACHE_REMOTE_URL", cacheSrv.URL)
	t.Setenv("GITL_CACHE_REMOTE_TIMEOUT_MS", "30000")
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // isolate the disk cache tier
	t.Setenv("XDG_DATA_HOME", t.TempDir())  // isolate risk history
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	emptyCfg := filepath.Join(t.TempDir(), "none.yaml")
	out := &syncBuffer{}
	var stderr bytes.Buffer
	root := newRootCmd()
	root.SetOut(out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"review", "--config", emptyCfg, "HEAD~1..HEAD",
		"--base-url", llmSrv.URL, "--no-stream"})

	done := make(chan error, 1)
	go func() {
		done <- root.ExecuteContext(context.Background())
	}()

	// The PUT must start (proving the store path runs at all) ...
	select {
	case <-putStarted:
	case err := <-done:
		t.Fatalf("review finished without a remote cache PUT (err=%v); output:\n%s", err, out.String())
	case <-time.After(10 * time.Second):
		t.Fatal("remote cache PUT never started")
	}
	// ... and by then the review must ALREADY be rendered: the store runs
	// strictly after the output. Before the fix the store (and this blocked
	// PUT) ran inside complete(), before rendering — out would still be empty.
	if got := out.String(); !strings.Contains(got, reviewBody) {
		t.Errorf("review output must be rendered before the cache store; got so far:\n%q", got)
	}

	close(releaseCh)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("review: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("review did not finish after releasing the cache PUT")
	}
}
