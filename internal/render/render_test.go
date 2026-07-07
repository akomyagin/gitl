package render

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleArtifact() Artifact {
	return Artifact{
		GeneratedAt: time.Date(2026, 7, 5, 18, 32, 10, 0, time.UTC),
		Range:       "HEAD~5..HEAD",
		Offline:     false,
		Provider:    "openai",
		Model:       "gpt-4o-mini",
		RiskLevel:   "medium",
		RiskSummary: "Touches auth middleware without new tests.",
		Stats: Stats{
			Commits:      5,
			FilesChanged: 12,
			LinesAdded:   340,
			LinesRemoved: 58,
		},
		Commits: []Commit{{
			Hash:    "abcd123",
			Author:  "Jane Doe",
			Date:    time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
			Subject: "feat: add token refresh",
		}},
		ReviewMarkdown: "## Summary\n\nDoes things.\n\n## Concerns\n\n- `auth.go` risky.\n",
	}
}

func TestRenderMarkdownPrependsRiskHeader(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, sampleArtifact(), FormatMarkdown); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := b.String()
	if !strings.HasPrefix(got, "**Risk:** MEDIUM — Touches auth middleware without new tests.\n\n") {
		t.Errorf("markdown missing risk header, got:\n%s", got)
	}
	if !strings.Contains(got, "## Summary") {
		t.Errorf("markdown missing review body, got:\n%s", got)
	}
}

func TestRenderEmptyFormatDefaultsToMarkdown(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, sampleArtifact(), ""); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(b.String(), "**Risk:** MEDIUM") {
		t.Errorf("empty format did not default to markdown: %q", b.String())
	}
}

func TestRenderText(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, sampleArtifact(), FormatText); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := b.String()
	// Markdown markers should be stripped.
	if strings.Contains(got, "##") || strings.Contains(got, "**") || strings.Contains(got, "`") {
		t.Errorf("text output still contains markdown markers:\n%s", got)
	}
	if !strings.Contains(got, "Summary") || !strings.Contains(got, "auth.go risky.") {
		t.Errorf("text output lost content:\n%s", got)
	}
	if !strings.Contains(got, "Risk: MEDIUM") {
		t.Errorf("text output missing risk header:\n%s", got)
	}
}

func TestRenderJSONSchema(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, sampleArtifact(), FormatJSON); err != nil {
		t.Fatalf("Render: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(b.String()), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}

	// Exact documented field names (§7.4).
	if got := m["schema_version"]; got != float64(1) {
		t.Errorf("schema_version = %v, want 1", got)
	}
	if m["generated_at"] != "2026-07-05T18:32:10Z" {
		t.Errorf("generated_at = %v", m["generated_at"])
	}
	if m["range"] != "HEAD~5..HEAD" {
		t.Errorf("range = %v", m["range"])
	}
	if m["offline"] != false {
		t.Errorf("offline = %v", m["offline"])
	}
	if m["provider"] != "openai" || m["model"] != "gpt-4o-mini" {
		t.Errorf("provider/model = %v/%v", m["provider"], m["model"])
	}

	risk, ok := m["risk"].(map[string]any)
	if !ok {
		t.Fatalf("risk is not an object: %v", m["risk"])
	}
	if risk["level"] != "medium" || risk["summary"] != "Touches auth middleware without new tests." {
		t.Errorf("risk = %v", risk)
	}
	if risk["heuristic"] != false {
		t.Errorf("risk.heuristic = %v, want false", risk["heuristic"])
	}

	stats, ok := m["stats"].(map[string]any)
	if !ok {
		t.Fatalf("stats is not an object: %v", m["stats"])
	}
	for k, want := range map[string]float64{
		"commits": 5, "files_changed": 12, "lines_added": 340, "lines_removed": 58,
	} {
		if stats[k] != want {
			t.Errorf("stats.%s = %v, want %v", k, stats[k], want)
		}
	}

	commits, ok := m["commits"].([]any)
	if !ok || len(commits) != 1 {
		t.Fatalf("commits = %v", m["commits"])
	}
	c0 := commits[0].(map[string]any)
	if c0["hash"] != "abcd123" || c0["author"] != "Jane Doe" ||
		c0["date"] != "2026-07-01T10:00:00Z" || c0["subject"] != "feat: add token refresh" {
		t.Errorf("commit[0] = %v", c0)
	}

	if _, ok := m["review_markdown"].(string); !ok {
		t.Errorf("review_markdown missing or not a string: %v", m["review_markdown"])
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, sampleArtifact(), Format("xml")); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestRenderHeuristicAnnotation(t *testing.T) {
	art := sampleArtifact()
	art.RiskHeuristic = true

	// Markdown: risk header must carry "*(heuristic)*".
	var md strings.Builder
	if err := Render(&md, art, FormatMarkdown); err != nil {
		t.Fatalf("Render md: %v", err)
	}
	if !strings.Contains(md.String(), "*(heuristic)*") {
		t.Errorf("markdown missing heuristic annotation:\n%s", md.String())
	}

	// Text: strip removes * but "heuristic" word must still be present.
	var txt strings.Builder
	if err := Render(&txt, art, FormatText); err != nil {
		t.Fatalf("Render text: %v", err)
	}
	if !strings.Contains(txt.String(), "heuristic") {
		t.Errorf("text output missing heuristic annotation:\n%s", txt.String())
	}

	// JSON: risk.heuristic must be true.
	var js strings.Builder
	if err := Render(&js, art, FormatJSON); err != nil {
		t.Fatalf("Render json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(js.String()), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	risk := m["risk"].(map[string]any)
	if risk["heuristic"] != true {
		t.Errorf("risk.heuristic = %v, want true", risk["heuristic"])
	}
}
