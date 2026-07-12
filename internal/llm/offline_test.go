package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/gitlog"
)

func sampleCommits() []gitlog.Commit {
	d, _ := time.Parse(time.RFC3339, "2026-07-01T10:00:00+03:00")
	return []gitlog.Commit{
		{
			Hash: "aaaaaaabbbbbbb", Author: "Alice", Date: d,
			Subject: "feat: add parser", Body: "Details here.",
			Files: []gitlog.FileChange{
				{Status: "A", Path: "internal/gitlog/parser.go"},
				{Status: "M", Path: "README.md"},
			},
		},
		{
			Hash: "cccccccddddddd", Author: "Bob", Date: d,
			Subject: "refactor: move file",
			Files: []gitlog.FileChange{
				{Status: "R100", Old: "old.go", Path: "new.go"},
				{Status: "D", Path: "legacy.go"},
			},
		},
	}
}

const sampleDiff = `--- a/README.md
+++ b/README.md
@@ -1,2 +1,3 @@
 line one
+added line
-removed line
`

func TestOfflineDeterministic(t *testing.T) {
	commits := sampleCommits()
	o := NewOffline(commits, sampleDiff)

	first, err := o.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	// Same instance, and a fresh instance from identical input, must match.
	second, err := o.Complete(context.Background(), Request{User: "different prompt is ignored"})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	third, err := NewOffline(sampleCommits(), sampleDiff).Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if first.Content != second.Content || first.Content != third.Content {
		t.Error("offline provider output is not deterministic")
	}
}

func TestOfflineContentUseful(t *testing.T) {
	out, err := NewOffline(sampleCommits(), sampleDiff).Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	c := out.Content

	for _, want := range []string{
		"# Code review (offline)",
		"## Commits",
		"feat: add parser",
		"refactor: move file",
		"## Changed files",
		"renamed", // R100 → renamed
		"old.go",  // rename old path shown
		"new.go",  // rename new path shown
		"deleted", // D → deleted
		"## Diff stats",
		"Lines added: 1",
		"Lines removed: 1",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("offline output missing %q\n---\n%s", want, c)
		}
	}
}

func TestOfflineAlwaysCarriesRisk(t *testing.T) {
	out, err := NewOffline(sampleCommits(), sampleDiff).Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if !ValidRiskLevel(out.Risk.Level) {
		t.Errorf("offline must carry a valid risk level, got %q", out.Risk.Level)
	}
	if out.Risk.Summary == "" {
		t.Error("offline risk summary must not be empty")
	}
}

func TestOfflineEmptyRange(t *testing.T) {
	out, err := NewOffline(nil, "").Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if !strings.Contains(out.Content, "No commits found") {
		t.Errorf("expected empty-range notice, got:\n%s", out.Content)
	}
}

// TestOfflineStagedNoCommitsButRealDiff: no commit metadata but a real diff
// (the staged-review case) must NOT claim "no commits found" — that's
// misleading when a real change is actually being reviewed.
func TestOfflineStagedNoCommitsButRealDiff(t *testing.T) {
	diff := "diff --git a/staged.txt b/staged.txt\n--- /dev/null\n+++ b/staged.txt\n+new content\n"
	out, err := NewOffline(nil, diff).Complete(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if strings.Contains(out.Content, "No commits found") {
		t.Errorf("staged review should not claim no commits were found:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "staged.txt") {
		t.Errorf("staged review should list the changed file:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "Files touched") {
		t.Errorf("staged review should report a file count:\n%s", out.Content)
	}
}

func TestOfflineRespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewOffline(sampleCommits(), sampleDiff).Complete(ctx, Request{}); err == nil {
		t.Error("expected error from cancelled context")
	}
}
