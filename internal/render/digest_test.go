package render

import (
	"strings"
	"testing"
	"time"
)

var (
	digestGeneratedAt = time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	digestSince       = time.Date(2026, 6, 29, 9, 0, 0, 0, time.UTC)
	digestUntil       = time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
)

func singleRepoDigestArtifact() DigestArtifact {
	return DigestArtifact{
		GeneratedAt: digestGeneratedAt,
		Days:        7,
		Since:       digestSince,
		Until:       digestUntil,
		Repos: []RepoDigest{
			{
				Path:         ".",
				Ok:           true,
				Commits:      23,
				FilesChanged: 14,
				LinesAdded:   812,
				LinesRemoved: 145,
				ByAuthor: []DigestAuthorStat{
					{Author: "Jane Doe", Commits: 14, LinesAdded: 610, LinesRemoved: 90},
					{Author: "John Roe", Commits: 9, LinesAdded: 202, LinesRemoved: 55},
				},
				ByTopic: []DigestTopicStat{
					{Topic: "feat", Commits: 10},
					{Topic: "fix", Commits: 7},
					{Topic: "other", Commits: 6},
				},
				TopFiles: []DigestFileStat{
					{Path: "internal/llm/client.go", Commits: 5},
					{Path: "README.md", Commits: 3},
				},
			},
		},
	}
}

func multiRepoDigestArtifact() DigestArtifact {
	art := singleRepoDigestArtifact()
	art.Repos[0].Path = "../service-a"
	art.Repos = append(art.Repos, RepoDigest{
		Path:         "../service-c",
		Ok:           true,
		Commits:      18,
		FilesChanged: 6,
		LinesAdded:   392,
		LinesRemoved: 245,
		ByAuthor: []DigestAuthorStat{
			{Author: "Amy Lin", Commits: 18, LinesAdded: 392, LinesRemoved: 245},
		},
		ByTopic: []DigestTopicStat{
			{Topic: "fix", Commits: 18},
		},
		TopFiles: []DigestFileStat{
			{Path: "main.go", Commits: 4},
		},
	})
	return art
}

func multiRepoWithErrorDigestArtifact() DigestArtifact {
	art := multiRepoDigestArtifact()
	// Insert a failing repo between the two successful ones (§10.5 example).
	art.Repos = []RepoDigest{
		art.Repos[0],
		{Path: "../service-b", Ok: false, Err: "not a git repository (or any of the parent directories)"},
		art.Repos[1],
	}
	return art
}

func TestDigestGoldenSingleRepoMarkdown(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, singleRepoDigestArtifact(), FormatMarkdown); err != nil {
		t.Fatalf("RenderDigest: %v", err)
	}
	assertGolden(t, "testdata/digest/single_repo.md", []byte(b.String()))
}

func TestDigestGoldenSingleRepoText(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, singleRepoDigestArtifact(), FormatText); err != nil {
		t.Fatalf("RenderDigest: %v", err)
	}
	assertGolden(t, "testdata/digest/single_repo.txt", []byte(b.String()))
}

func TestDigestGoldenSingleRepoJSON(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, singleRepoDigestArtifact(), FormatJSON); err != nil {
		t.Fatalf("RenderDigest: %v", err)
	}
	assertGolden(t, "testdata/digest/single_repo.json", []byte(b.String()))
}

func TestDigestGoldenMultiRepoMarkdown(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, multiRepoDigestArtifact(), FormatMarkdown); err != nil {
		t.Fatalf("RenderDigest: %v", err)
	}
	assertGolden(t, "testdata/digest/multi_repo.md", []byte(b.String()))
}

func TestDigestGoldenMultiRepoJSON(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, multiRepoDigestArtifact(), FormatJSON); err != nil {
		t.Fatalf("RenderDigest: %v", err)
	}
	assertGolden(t, "testdata/digest/multi_repo.json", []byte(b.String()))
}

func TestDigestGoldenMultiRepoWithErrorMarkdown(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, multiRepoWithErrorDigestArtifact(), FormatMarkdown); err != nil {
		t.Fatalf("RenderDigest: %v", err)
	}
	assertGolden(t, "testdata/digest/multi_repo_with_error.md", []byte(b.String()))
}

func TestDigestGoldenMultiRepoWithErrorJSON(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, multiRepoWithErrorDigestArtifact(), FormatJSON); err != nil {
		t.Fatalf("RenderDigest: %v", err)
	}
	assertGolden(t, "testdata/digest/multi_repo_with_error.json", []byte(b.String()))
}

func TestDigestJSONNullFieldsOnError(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, multiRepoWithErrorDigestArtifact(), FormatJSON); err != nil {
		t.Fatalf("RenderDigest: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, `"ok": false`) {
		t.Errorf("expected an ok:false repo entry:\n%s", out)
	}
	if !strings.Contains(out, `"stats": null`) {
		t.Errorf("expected stats:null for the failed repo:\n%s", out)
	}
}

func TestDigestUnknownFormat(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderDigest(&b, singleRepoDigestArtifact(), Format("xml")); err == nil {
		t.Error("expected error for unknown format")
	}
}
