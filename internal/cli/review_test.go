package cli

import (
	"strings"
	"testing"

	"github.com/akomyagin/gitl/internal/config"
	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/llm"
	"github.com/akomyagin/gitl/internal/render"
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

func TestBuildArtifactStats(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		LLM: config.LLMConfig{Provider: "openai", Model: "gpt-4o-mini", APIKey: "k"},
	}
	commits := []gitlog.Commit{
		{Hash: "h1", Author: "A", Subject: "s1", Files: []gitlog.FileChange{{Status: "M", Path: "a.go"}}},
		{Hash: "h2", Author: "B", Subject: "s2", Files: []gitlog.FileChange{{Status: "M", Path: "a.go"}, {Status: "A", Path: "b.go"}}},
	}
	diff := "--- a/a.go\n+++ b/a.go\n+added1\n+added2\n-removed1\n"
	resp := llm.Response{Content: "review", Risk: llm.Risk{Level: "medium", Summary: "sum"}}

	art := buildArtifact(cfg, "r..s", commits, diff, resp)
	if art.Stats.Commits != 2 {
		t.Errorf("commits = %d, want 2", art.Stats.Commits)
	}
	if art.Stats.FilesChanged != 2 { // a.go deduped across commits
		t.Errorf("files_changed = %d, want 2", art.Stats.FilesChanged)
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

func TestFailErrorMessage(t *testing.T) {
	t.Parallel()
	e := &failError{level: "high", threshold: "medium"}
	if !strings.Contains(e.Error(), "high") || !strings.Contains(e.Error(), "medium") {
		t.Errorf("failError message unhelpful: %q", e.Error())
	}
}
