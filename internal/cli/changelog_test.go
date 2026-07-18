package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/llm"
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

// ---- changelog --ai ----

// gitShortHashes returns the repo's commit hashes (newest first), truncated to
// the 7-character short form used by the changelog artifact.
func gitShortHashes(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%H")
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	var hashes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) > 7 {
			line = line[:7]
		}
		hashes = append(hashes, line)
	}
	return hashes
}

// newChangelogAIServer returns a mock OpenAI-compatible chat/completions
// endpoint that always answers with the given assistant content, plus a call
// counter.
func newChangelogAIServer(t *testing.T, content string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": content}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			panic(fmt.Sprintf("encode mock response: %v", err))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

// aiChangelogContent builds a model response with prose and a valid
// ```changelog block referencing the given hashes (featA = the "feat: add
// feature A" commit, fixB = the "fix: correct bug in B" commit).
func aiChangelogContent(featA, fixB string) string {
	return "Here is the improved changelog.\n\n```changelog\n" +
		fmt.Sprintf(`{"categories": {"Added": [{"subject": "Brand new feature A", "hashes": [%q]}], "Fixed": [{"subject": "Corrected bug in B", "hashes": [%q]}]}, "breaking": []}`, featA, fixB) +
		"\n```\n"
}

func TestChangelogAIOfflineFallsBackToDeterministic(t *testing.T) {
	dir := setupChangelogRepo(t)
	t.Setenv("GITL_API_KEY", "")
	out, stderr, err := runChangelogInDir(t, dir, "--ai")
	if err != nil {
		t.Fatalf("changelog --ai without a key must not fail: %v", err)
	}
	if !strings.Contains(out, "### Added") || !strings.Contains(out, "add feature A") {
		t.Errorf("expected the deterministic changelog output:\n%s", out)
	}
	if !strings.Contains(stderr, "falling back to the deterministic changelog") {
		t.Errorf("expected the offline fallback warning on stderr, got:\n%s", stderr)
	}
}

func TestChangelogAIOnlineHappyPath(t *testing.T) {
	dir := setupChangelogRepo(t)
	hashes := gitShortHashes(t, dir) // newest first: [docs C, fix B, feat A]
	featA, fixB := hashes[2], hashes[1]

	srv, calls := newChangelogAIServer(t, aiChangelogContent(featA, fixB))
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")

	out, stderr, err := runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL, "--no-cache")
	if err != nil {
		t.Fatalf("changelog --ai: %v\nstderr:\n%s", err, stderr)
	}
	if calls.Load() != 1 {
		t.Errorf("server saw %d request(s), want 1", calls.Load())
	}
	if !strings.Contains(out, "Brand new feature A") || !strings.Contains(out, "Corrected bug in B") {
		t.Errorf("AI prose missing from output:\n%s", out)
	}
	if !strings.Contains(out, featA) {
		t.Errorf("commit hash %s missing from output:\n%s", featA, out)
	}
	// The model omitted the docs commit entirely — it must not reappear.
	if strings.Contains(out, "document C") {
		t.Errorf("omitted deterministic entry leaked into AI output:\n%s", out)
	}
	// The surrounding model prose must not leak — only the parsed artifact is rendered.
	if strings.Contains(out, "Here is the improved changelog") {
		t.Errorf("raw model prose leaked into output:\n%s", out)
	}
}

func TestChangelogAIJSONFormat(t *testing.T) {
	dir := setupChangelogRepo(t)
	hashes := gitShortHashes(t, dir)
	srv, _ := newChangelogAIServer(t, aiChangelogContent(hashes[2], hashes[1]))
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")

	out, stderr, err := runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL, "--no-cache", "--format=json")
	if err != nil {
		t.Fatalf("changelog --ai --format=json: %v\nstderr:\n%s", err, stderr)
	}
	var parsed struct {
		SchemaVersion int                          `json:"schema_version"`
		Command       string                       `json:"command"`
		Categories    map[string][]json.RawMessage `json:"categories"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if parsed.SchemaVersion != 1 || parsed.Command != "changelog" {
		t.Errorf("schema_version=%d command=%q, want 1/changelog", parsed.SchemaVersion, parsed.Command)
	}
	// The full jsonChangelog contract: every category key present, even empty.
	for _, name := range gitlog.CategoryOrder {
		if _, ok := parsed.Categories[name]; !ok {
			t.Errorf("category key %q missing from JSON output:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "Brand new feature A") {
		t.Errorf("AI prose missing from JSON output:\n%s", out)
	}
}

func TestChangelogAIMalformedResponseFallsBack(t *testing.T) {
	dir := setupChangelogRepo(t)
	srv, calls := newChangelogAIServer(t, "Sorry, here is prose without any structured block.")
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")

	out, stderr, err := runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL, "--no-cache")
	if err != nil {
		t.Fatalf("a malformed model response must fall back, not fail: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("server saw %d request(s), want 1", calls.Load())
	}
	if !strings.Contains(out, "add feature A") || !strings.Contains(out, "correct bug in B") {
		t.Errorf("expected the deterministic fallback output:\n%s", out)
	}
	if !strings.Contains(stderr, "falling back to the deterministic changelog") {
		t.Errorf("expected the malformed-response warning on stderr, got:\n%s", stderr)
	}
}

func TestChangelogAIServerErrorIsFatal(t *testing.T) {
	// An operational API failure (5xx after retries) with --ai and a key is a
	// real error — NOT a silent deterministic fallback (symmetric with review).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	dir := setupChangelogRepo(t)
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")
	t.Setenv("GITL_LLM_MAX_RETRIES", "0")

	_, _, err := runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL, "--no-cache")
	if err == nil {
		t.Fatal("expected an error when the API fails with --ai")
	}
	if !strings.Contains(err.Error(), "changelog --ai failed") {
		t.Errorf("error should identify the AI changelog call, got: %v", err)
	}
}

func TestChangelogAICostGuardBlocks(t *testing.T) {
	dir := setupChangelogRepo(t)
	srv, calls := newChangelogAIServer(t, "never reached")
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")

	_, _, err := runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL, "--no-cache", "--max-cost-usd=0.0001")
	if err == nil {
		t.Fatal("expected the cost guard to block the request")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention the exceeded limit, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("cost guard must fire BEFORE any API call, server saw %d", calls.Load())
	}
}

func TestChangelogAIDryRun(t *testing.T) {
	dir := setupChangelogRepo(t)
	srv, calls := newChangelogAIServer(t, "never reached")
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")

	out, _, err := runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL, "--no-cache", "--dry-run")
	if err != nil {
		t.Fatalf("changelog --ai --dry-run: %v", err)
	}
	if !strings.Contains(out, "cost estimate") {
		t.Errorf("expected a cost estimate, got:\n%s", out)
	}
	if calls.Load() != 0 {
		t.Errorf("--dry-run must not call the API, server saw %d", calls.Load())
	}
}

func TestChangelogDryRunIgnoredWithoutAI(t *testing.T) {
	// --dry-run/--max-cost-usd without --ai are silently ignored — the
	// deterministic path is free and never calls a provider.
	dir := setupChangelogRepo(t)
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")
	out, _, err := runChangelogInDir(t, dir, "--dry-run", "--max-cost-usd=0.0001")
	if err != nil {
		t.Fatalf("changelog --dry-run without --ai: %v", err)
	}
	if strings.Contains(out, "cost estimate") {
		t.Errorf("--dry-run must be a no-op without --ai:\n%s", out)
	}
	if !strings.Contains(out, "### Added") {
		t.Errorf("expected the normal deterministic changelog:\n%s", out)
	}
}

func TestChangelogAIEmptyRangeSkipsModel(t *testing.T) {
	dir := setupChangelogRepo(t)
	srv, calls := newChangelogAIServer(t, "never reached")
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")

	out, _, err := runChangelogInDir(t, dir, "HEAD..HEAD", "--ai", "--base-url", srv.URL, "--no-cache")
	if err != nil {
		t.Fatalf("changelog --ai over an empty range: %v", err)
	}
	if !strings.Contains(out, "No changes in this range.") {
		t.Errorf("expected the empty-changelog message:\n%s", out)
	}
	if calls.Load() != 0 {
		t.Errorf("an empty range must never call the model, server saw %d", calls.Load())
	}
}

func TestChangelogAICacheServesAllFormats(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cache isolation via XDG_CACHE_HOME is linux-only")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // isolate os.UserCacheDir from the host

	dir := setupChangelogRepo(t)
	hashes := gitShortHashes(t, dir)
	srv, calls := newChangelogAIServer(t, aiChangelogContent(hashes[2], hashes[1]))
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")

	// First run (md) populates the cache with the RAW model response.
	out, _, err := runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL)
	if err != nil {
		t.Fatalf("first changelog --ai run: %v", err)
	}
	if !strings.Contains(out, "Brand new feature A") {
		t.Errorf("first run missing AI prose:\n%s", out)
	}
	if calls.Load() != 1 {
		t.Fatalf("first run: server saw %d request(s), want 1", calls.Load())
	}

	// Second run with a DIFFERENT format is served from the same cache entry:
	// the raw response is re-parsed, so one hit covers every --format.
	out, _, err = runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL, "--format=json")
	if err != nil {
		t.Fatalf("second changelog --ai run: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("second run must be a cache hit, server saw %d request(s)", calls.Load())
	}
	if !strings.Contains(out, `"command": "changelog"`) || !strings.Contains(out, "Brand new feature A") {
		t.Errorf("cache-served JSON output wrong:\n%s", out)
	}
}

// TestChangelogAIBreakingOnlyResponse: the model returns a breaking change
// ONLY in `breaking`, with empty categories — nothing guarantees the
// duplication into a category that the deterministic path performs. The
// rendered output must still show the BREAKING CHANGES section instead of
// collapsing to "No changes in this range.".
func TestChangelogAIBreakingOnlyResponse(t *testing.T) {
	dir := setupChangelogRepo(t)
	hashes := gitShortHashes(t, dir)
	content := "Summary of the release.\n\n```changelog\n" +
		fmt.Sprintf(`{"categories": {}, "breaking": [{"subject": "Removed the legacy --foo flag", "hashes": [%q]}]}`, hashes[0]) +
		"\n```\n"
	srv, calls := newChangelogAIServer(t, content)
	t.Setenv("GITL_API_KEY", "sk-fake-changelog")

	out, stderr, err := runChangelogInDir(t, dir, "--ai", "--base-url", srv.URL, "--no-cache")
	if err != nil {
		t.Fatalf("changelog --ai: %v\nstderr:\n%s", err, stderr)
	}
	if calls.Load() != 1 {
		t.Errorf("server saw %d request(s), want 1", calls.Load())
	}
	if strings.Contains(out, "No changes in this range.") {
		t.Errorf("breaking-only AI response rendered as empty:\n%s", out)
	}
	if !strings.Contains(out, "BREAKING CHANGES") {
		t.Errorf("BREAKING CHANGES section missing:\n%s", out)
	}
	if !strings.Contains(out, "Removed the legacy --foo flag") || !strings.Contains(out, hashes[0]) {
		t.Errorf("breaking entry missing from output:\n%s", out)
	}
}

// TestAIChangelogArtifactMultiHashBreaking closes the multi-hash gap: a
// category entry carrying TWO hashes, only one of which appears in a breaking
// entry (which itself lists a full-length hash plus an invented one). The
// intersection must be computed on the validated hash lists, not on a
// re-split of the formatted "h1, h2" display string.
func TestAIChangelogArtifactMultiHashBreaking(t *testing.T) {
	t.Parallel()
	commits := []gitlog.Commit{
		{Hash: "aaaaaaa1111111111111111111111111111111aa"},
		{Hash: "bbbbbbb2222222222222222222222222222222bb"},
		{Hash: "ccccccc3333333333333333333333333333333cc"},
	}
	payload := llm.ChangelogPayload{
		Categories: map[string][]llm.ChangelogItem{
			"Changed": {
				// Two hashes in one entry; only bbbbbbb is breaking.
				{Subject: "Rework storage layer", Hashes: []string{"aaaaaaa", "bbbbbbb"}},
			},
			"Fixed": {
				// Multi-hash entry with no breaking overlap: stays non-breaking.
				{Subject: "Fix flaky retries", Hashes: []string{"ccccccc", "aaaaaaa"}},
			},
		},
		Breaking: []llm.ChangelogItem{
			// Full-length hash (normalized to short form) + an invented one
			// that is dropped by validation.
			{Subject: "Storage format changed", Hashes: []string{"bbbbbbb2222222222222222222222222222222bb", "fffffff"}},
		},
	}

	art := aiChangelogArtifact(time.Now().UTC(), "v1..HEAD", commits, payload, nil)

	changed := art.Categories[gitlog.CategoryChanged]
	if len(changed) != 1 {
		t.Fatalf("Changed = %+v, want exactly one entry", changed)
	}
	if changed[0].Hash != "aaaaaaa, bbbbbbb" {
		t.Errorf("Changed hash = %q, want %q", changed[0].Hash, "aaaaaaa, bbbbbbb")
	}
	if !changed[0].Breaking {
		t.Errorf("Changed entry sharing hash bbbbbbb with a breaking entry must be marked Breaking: %+v", changed[0])
	}

	fixed := art.Categories[gitlog.CategoryFixed]
	if len(fixed) != 1 {
		t.Fatalf("Fixed = %+v, want exactly one entry", fixed)
	}
	if fixed[0].Breaking {
		t.Errorf("Fixed entry has no breaking hash and must stay non-breaking: %+v", fixed[0])
	}

	if len(art.Breaking) != 1 || art.Breaking[0].Hash != "bbbbbbb" || !art.Breaking[0].Breaking {
		t.Errorf("Breaking = %+v, want one entry with the surviving short hash bbbbbbb", art.Breaking)
	}
}

// TestAIChangelogArtifactDefensive covers the payload→artifact conversion
// edge cases: invented hashes dropped (whole entry when nothing survives),
// unknown categories remapped to Other, full hashes normalized to short form,
// breaking entries marked in their home category, and the required-categories
// policy recomputed from the AI result.
func TestAIChangelogArtifactDefensive(t *testing.T) {
	t.Parallel()
	commits := []gitlog.Commit{
		{Hash: "aaaaaaa1111111111111111111111111111111aa"},
		{Hash: "bbbbbbb2222222222222222222222222222222bb"},
		{Hash: "ccccccc3333333333333333333333333333333cc"},
	}
	payload := llm.ChangelogPayload{
		Categories: map[string][]llm.ChangelogItem{
			"Added": {
				{Subject: "Real change", Hashes: []string{"aaaaaaa"}},
				{Subject: "Invented change", Hashes: []string{"fffffff"}}, // no valid hash → dropped
			},
			"Changed": {
				{Subject: "Breaking rework", Hashes: []string{"ccccccc"}},
			},
			// Unknown category → remapped to Other; full hash → short form.
			"Bogus": {
				{Subject: "Misfiled change", Hashes: []string{"bbbbbbb2222222222222222222222222222222bb"}},
			},
		},
		Breaking: []llm.ChangelogItem{
			{Subject: "Session store reworked", Hashes: []string{"ccccccc"}},
		},
	}

	art := aiChangelogArtifact(time.Now().UTC(), "v1..HEAD", commits, payload, []string{"Security", "Added"})

	added := art.Categories[gitlog.CategoryAdded]
	if len(added) != 1 || added[0].Subject != "Real change" || added[0].Hash != "aaaaaaa" {
		t.Errorf("Added = %+v, want only the real change", added)
	}
	other := art.Categories[gitlog.CategoryOther]
	if len(other) != 1 || other[0].Subject != "Misfiled change" || other[0].Hash != "bbbbbbb" {
		t.Errorf("Other = %+v, want the remapped entry with a short hash", other)
	}
	changed := art.Categories[gitlog.CategoryChanged]
	if len(changed) != 1 || !changed[0].Breaking {
		t.Errorf("Changed = %+v, want one entry marked breaking", changed)
	}
	if len(art.Breaking) != 1 || art.Breaking[0].Subject != "Session store reworked" || !art.Breaking[0].Breaking {
		t.Errorf("Breaking = %+v", art.Breaking)
	}
	// "Added" has entries in the AI result; only "Security" is missing.
	if len(art.MissingRequiredCategories) != 1 || art.MissingRequiredCategories[0] != "Security" {
		t.Errorf("MissingRequiredCategories = %v, want [Security]", art.MissingRequiredCategories)
	}
}

// completeOnlyProvider implements llm.Provider but NOT llm.RawCompleter — the
// shape of a hypothetical future provider without raw completion.
type completeOnlyProvider struct{}

func (completeOnlyProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

// A provider that does not implement llm.RawCompleter must produce an explicit
// "does not support raw completion" error from the changelog --ai path — never
// a panic. Tested via rawCompleterFor, the exact check runChangelogAI performs
// after newNetworkClient (which today always returns an *llm.Client, so the
// full command path cannot reach this branch yet).
func TestChangelogAIProviderWithoutRawCompletion(t *testing.T) {
	t.Parallel()
	rc, err := rawCompleterFor(completeOnlyProvider{}, "someprovider")
	if err == nil {
		t.Fatal("expected an error for a provider without RawCompleter, got nil")
	}
	if rc != nil {
		t.Errorf("rc = %v, want nil on error", rc)
	}
	if !strings.Contains(err.Error(), `provider "someprovider" does not support raw completion`) {
		t.Errorf("error should name the provider and the missing capability, got: %v", err)
	}

	// And the happy path: *llm.Client satisfies the capability.
	client, cerr := llm.NewClient(llm.ClientConfig{Provider: "openai", BaseURL: "http://127.0.0.1:0", APIKey: "k"})
	if cerr != nil {
		t.Fatalf("NewClient: %v", cerr)
	}
	if _, err := rawCompleterFor(client, "openai"); err != nil {
		t.Errorf("rawCompleterFor(*llm.Client) must succeed, got: %v", err)
	}
}

// A breaking entry whose hashes are all invented is discarded like any other.
func TestAIChangelogArtifactDropsInventedBreaking(t *testing.T) {
	t.Parallel()
	commits := []gitlog.Commit{{Hash: "aaaaaaa1111111"}}
	payload := llm.ChangelogPayload{
		Categories: map[string][]llm.ChangelogItem{
			"Fixed": {{Subject: "Real fix", Hashes: []string{"aaaaaaa"}}},
		},
		Breaking: []llm.ChangelogItem{{Subject: "Invented break", Hashes: []string{"0000000"}}},
	}
	art := aiChangelogArtifact(time.Now().UTC(), "r", commits, payload, nil)
	if len(art.Breaking) != 0 {
		t.Errorf("Breaking = %+v, want empty", art.Breaking)
	}
	if len(art.Categories[gitlog.CategoryFixed]) != 1 {
		t.Errorf("Fixed = %+v", art.Categories[gitlog.CategoryFixed])
	}
}
