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
	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/llmcache"
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

	// Run metadata (U8): a fresh offline call is never a cache hit, the cache
	// tier is "none" (offline disables the cache), and the duration is a
	// non-negative millisecond count (>= 0, not > 0 — a run can be sub-ms).
	if art.Cache.Hit {
		t.Error("Cache.Hit = true, want false (fresh offline call)")
	}
	if art.Cache.Tier != "none" {
		t.Errorf("Cache.Tier = %q, want %q (offline mode disables the cache)", art.Cache.Tier, "none")
	}
	if art.DurationMS < 0 {
		t.Errorf("DurationMS = %d, want >= 0", art.DurationMS)
	}
}

// TestCacheTier: the cache.tier metadata literal for every configuration that
// disables the cache, plus the local-vs-tiered split. tier only ever emits the
// three documented literals — never a URL or token.
func TestCacheTier(t *testing.T) {
	base := func(t *testing.T) *config.Config {
		cfg := coreTestConfig(t)
		cfg.LLM.APIKey = "test-key" // network mode: cache eligible
		return cfg
	}
	tests := []struct {
		name    string
		mutate  func(cfg *config.Config)
		noCache bool
		want    string
	}{
		{name: "cache disabled", mutate: func(cfg *config.Config) { cfg.Cache.Enabled = false }, want: "none"},
		{name: "--no-cache", noCache: true, want: "none"},
		{name: "offline mode", mutate: func(cfg *config.Config) { cfg.LLM.APIKey = "" }, want: "none"},
		{name: "ttl_hours zero", mutate: func(cfg *config.Config) { cfg.Cache.TTLHours = 0 }, want: "none"},
		{name: "ttl_hours negative", mutate: func(cfg *config.Config) { cfg.Cache.TTLHours = -1 }, want: "none"},
		{name: "enabled disk-only", want: "local"},
		{name: "enabled with remote", mutate: func(cfg *config.Config) { cfg.Cache.Remote.URL = "http://cache.example" }, want: "tiered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base(t)
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			got := cacheTier(cfg, tt.noCache)
			if got != tt.want {
				t.Errorf("cacheTier = %q, want %q", got, tt.want)
			}
			switch got {
			case "none", "local", "tiered":
			default:
				t.Errorf("cacheTier = %q, must be one of none|local|tiered (never a URL or token)", got)
			}
		})
	}
}

// TestCacheTierAgreesWithPrepareReview is the drift guard: cacheTier reports
// "none" exactly when prepareReview wires NO cache (plan.cache == nil), for
// the same cfg+noCache. Both consume the shared useCache predicate; this test
// fails if a future edit makes the reported tier lie about whether caching was
// actually active.
func TestCacheTierAgreesWithPrepareReview(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // hermetic disk-cache dir for llmcache.Open
	tests := []struct {
		name    string
		mutate  func(cfg *config.Config)
		noCache bool
	}{
		{name: "cache disabled", mutate: func(cfg *config.Config) { cfg.Cache.Enabled = false }},
		{name: "--no-cache", noCache: true},
		{name: "offline mode", mutate: func(cfg *config.Config) { cfg.LLM.APIKey = "" }},
		{name: "ttl_hours zero", mutate: func(cfg *config.Config) { cfg.Cache.TTLHours = 0 }},
		{name: "enabled disk-only"},
		{name: "enabled with remote", mutate: func(cfg *config.Config) { cfg.Cache.Remote.URL = "http://cache.example" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := coreTestConfig(t)
			cfg.LLM.APIKey = "test-key"
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			plan, err := prepareReview(cfg, coreRangeSource(), ReviewOptions{NoCache: tt.noCache})
			if err != nil {
				t.Fatalf("prepareReview: %v", err)
			}
			tier := cacheTier(cfg, tt.noCache)
			if (tier != "none") != (plan.cache != nil) {
				t.Errorf("cacheTier = %q but plan.cache != nil is %v — the two predicates drifted",
					tier, plan.cache != nil)
			}
		})
	}
}

// TestRunReviewCoreCacheHitStampsMetadata: a warm disk cache makes the core
// return the cached artifact with Cache.Hit=true, Tier="local", and a
// non-negative duration — with no provider call (the fake API key would fail
// any real network path).
func TestRunReviewCoreCacheHitStampsMetadata(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // hermetic disk cache
	cfg := coreTestConfig(t)
	cfg.LLM.APIKey = "test-key-never-used"
	src := coreRangeSource()

	// Seed the cache under the exact key the core will compute: prepareReview
	// with the same cfg+src+opts yields the same plan.cacheKey.
	plan, err := prepareReview(cfg, src, ReviewOptions{})
	if err != nil {
		t.Fatalf("prepareReview: %v", err)
	}
	if plan.cache == nil || plan.cacheKey == "" {
		t.Fatal("expected an active cache for a network-mode config")
	}
	seeded := llm.Response{
		Content: "cached review body",
		Risk:    llm.Risk{Level: "low", Summary: "cached"},
	}
	if err := plan.cache.Put(plan.cacheKey, seeded); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}

	art, err := RunReviewCore(context.Background(), cfg, src, ReviewOptions{})
	if err != nil {
		t.Fatalf("RunReviewCore: %v", err)
	}
	if art.ReviewMarkdown != "cached review body" {
		t.Errorf("ReviewMarkdown = %q, want the seeded cached body", art.ReviewMarkdown)
	}
	if !art.Cache.Hit {
		t.Error("Cache.Hit = false, want true (served from the seeded cache)")
	}
	if art.Cache.Tier != "local" {
		t.Errorf("Cache.Tier = %q, want %q (disk-only cache)", art.Cache.Tier, "local")
	}
	if art.DurationMS < 0 {
		t.Errorf("DurationMS = %d, want >= 0", art.DurationMS)
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

// TestLlmCacheKeyKindSeparatesReviewAndChangelog guards TD-5 at the production
// call-site level (not just llmcache.Key in isolation): review and
// changelog --ai must produce different cache keys from llmCacheKey even when
// given the identical cfg/system/user — e.g. a custom system_template_file
// that happens to render the same prompt for both commands must not let one
// command's cached response be served to the other.
func TestLlmCacheKeyKindSeparatesReviewAndChangelog(t *testing.T) {
	cfg := &config.Config{}
	reviewKey := llmCacheKey(cfg, "review", "same system", "same user")
	changelogKey := llmCacheKey(cfg, "changelog", "same system", "same user")
	if reviewKey == changelogKey {
		t.Error("review and changelog cache keys must differ even with identical system+user text")
	}
}

// TestStoreCacheSkipsEmptyResponse: defense in depth against cache poisoning —
// an empty/whitespace-only provider response must never be written to the
// shared LLM cache, whichever path produced it (a poisoned entry would be
// served to the buffered CLI, --no-stream, and MCP gitl_review for the whole
// TTL). Non-empty responses must still be cached normally.
func TestStoreCacheSkipsEmptyResponse(t *testing.T) {
	cache := llmcache.NewInDir(t.TempDir(), time.Hour)
	key := llmcache.Key(llmcache.KeyParams{Provider: "openai", Model: "gpt-4o-mini", System: "s", User: "u"})
	plan := &reviewPlan{cache: cache, cacheKey: key}

	tests := []struct {
		name    string
		content string
	}{
		{name: "empty content", content: ""},
		{name: "whitespace-only content", content: "  \n\t \n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan.storeCache(llm.Response{Content: tt.content, Risk: llm.Risk{Level: "low"}})
			if _, ok, err := cache.Get(key); err != nil {
				t.Fatalf("cache.Get: %v", err)
			} else if ok {
				t.Fatal("empty response was written to the cache; it must be skipped")
			}
		})
	}

	// Sanity: a real response with the same plan/key IS cached.
	plan.storeCache(llm.Response{Content: "a real review body", Risk: llm.Risk{Level: "low"}})
	resp, ok, err := cache.Get(key)
	if err != nil {
		t.Fatalf("cache.Get after non-empty put: %v", err)
	}
	if !ok {
		t.Fatal("non-empty response should be cached")
	}
	if resp.Content != "a real review body" {
		t.Errorf("cached Content = %q, want %q", resp.Content, "a real review body")
	}
}
