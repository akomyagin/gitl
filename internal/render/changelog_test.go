package render

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/akomyagin/gitl/internal/gitlog"
)

var changelogGeneratedAt = time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)

// basicChangelogArtifact covers multiple non-empty categories plus at least
// one empty category, exercising the "only print non-empty sections" rule.
func basicChangelogArtifact() ChangelogArtifact {
	commits := []gitlog.Commit{
		{Hash: "a1b2c3d4567", Subject: "feat: add token refresh endpoint"},
		{Hash: "9f8e7d6c543", Subject: "fix: correct off-by-one in pagination"},
		{Hash: "1122334abcd", Subject: "docs: update contributing guide"},
	}
	cl := gitlog.CategorizeCommits(commits)
	missing := gitlog.MissingRequiredCategories(cl, []string{"Added", "Fixed", "Security"})
	return NewChangelogArtifact(changelogGeneratedAt, "v1.2.0..HEAD", cl, missing)
}

// breakingChangelogArtifact covers the "⚠ BREAKING CHANGES" section and the
// "**BREAKING**:" inline marker (§9.4).
func breakingChangelogArtifact() ChangelogArtifact {
	commits := []gitlog.Commit{
		{
			Hash:    "d4e5f6a0000",
			Subject: "feat: rework session store API",
			Body:    "BREAKING CHANGE: drop support for config schema v0",
		},
	}
	cl := gitlog.CategorizeCommits(commits)
	return NewChangelogArtifact(changelogGeneratedAt, "v2.0.0..HEAD", cl, nil)
}

// emptyChangelogArtifact covers zero commits in range.
func emptyChangelogArtifact() ChangelogArtifact {
	cl := gitlog.CategorizeCommits(nil)
	return NewChangelogArtifact(changelogGeneratedAt, "v1.0.0..HEAD", cl, nil)
}

func TestChangelogGoldenBasicMarkdown(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderChangelog(&b, basicChangelogArtifact(), FormatMarkdown); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}
	assertGolden(t, "testdata/changelog/basic.md", []byte(b.String()))
}

func TestChangelogGoldenBasicText(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderChangelog(&b, basicChangelogArtifact(), FormatText); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}
	assertGolden(t, "testdata/changelog/basic.txt", []byte(b.String()))
}

func TestChangelogGoldenBasicJSON(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderChangelog(&b, basicChangelogArtifact(), FormatJSON); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}
	assertGolden(t, "testdata/changelog/basic.json", []byte(b.String()))
}

func TestChangelogGoldenBreakingMarkdown(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderChangelog(&b, breakingChangelogArtifact(), FormatMarkdown); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}
	assertGolden(t, "testdata/changelog/breaking.md", []byte(b.String()))
}

func TestChangelogGoldenBreakingJSON(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderChangelog(&b, breakingChangelogArtifact(), FormatJSON); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}
	assertGolden(t, "testdata/changelog/breaking.json", []byte(b.String()))
}

func TestChangelogGoldenEmpty(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderChangelog(&b, emptyChangelogArtifact(), FormatMarkdown); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}
	assertGolden(t, "testdata/changelog/empty.md", []byte(b.String()))
}

// TestChangelogBreakingOnlyNotEmpty is the regression test for the AI-path
// scenario where the model returns a breaking change ONLY in `breaking`,
// without duplicating it into any category: Categories is empty, Breaking is
// not. The renderer must show the BREAKING CHANGES section, not "No changes".
// (The deterministic path always duplicates breaking commits into a category,
// so this artifact shape can only come from aiChangelogArtifact.)
func TestChangelogBreakingOnlyNotEmpty(t *testing.T) {
	t.Parallel()
	art := ChangelogArtifact{
		GeneratedAt: changelogGeneratedAt,
		Range:       "v1.0.0..HEAD",
		Categories:  map[string][]ChangelogEntry{},
		Breaking: []ChangelogEntry{
			{Hash: "abc1234", Subject: "Removed the legacy --foo flag", Breaking: true},
		},
		MissingRequiredCategories: []string{},
	}

	var b strings.Builder
	if err := RenderChangelog(&b, art, FormatMarkdown); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}
	out := b.String()
	if strings.Contains(out, "No changes in this range.") {
		t.Errorf("breaking-only artifact rendered as empty:\n%s", out)
	}
	if !strings.Contains(out, "BREAKING CHANGES") {
		t.Errorf("BREAKING CHANGES section missing:\n%s", out)
	}
	if !strings.Contains(out, "Removed the legacy --foo flag") || !strings.Contains(out, "abc1234") {
		t.Errorf("breaking entry missing from output:\n%s", out)
	}
}

func TestChangelogUnknownFormat(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderChangelog(&b, basicChangelogArtifact(), Format("xml")); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestChangelogMissingRequiredCategoriesInJSON(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderChangelog(&b, basicChangelogArtifact(), FormatJSON); err != nil {
		t.Fatalf("RenderChangelog: %v", err)
	}

	var decoded struct {
		MissingRequiredCategories []string `json:"missing_required_categories"`
	}
	if err := json.Unmarshal([]byte(b.String()), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if len(decoded.MissingRequiredCategories) != 1 || decoded.MissingRequiredCategories[0] != "Security" {
		t.Errorf("missing_required_categories = %v, want [\"Security\"]", decoded.MissingRequiredCategories)
	}
}
