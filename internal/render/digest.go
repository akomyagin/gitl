// Digest rendering (§10.3/§10.5/§10.6 of docs/TECHNICAL_PLAN.md). Same
// package as review/changelog rendering, for the same reason: a new artifact
// type and a new render function, reusing the existing format-agnostic
// mechanics (stripMarkdown, indented JSON encoding).
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// DigestSchemaVersion is the digest JSON schema version — its own counter,
// independent of review's and changelog's (§9.4/§10.3): unrelated wire
// contract, unrelated consumers.
const DigestSchemaVersion = 1

// DigestAuthorStat, DigestTopicStat and DigestFileStat mirror
// gitlog.AuthorStat/TopicStat/FileStat as plain render-owned types, so
// internal/render does not need to import gitlog's aggregation internals
// beyond the data it actually renders.
type DigestAuthorStat struct {
	Author       string
	Commits      int
	LinesAdded   int
	LinesRemoved int
}

type DigestTopicStat struct {
	Topic   string
	Commits int
}

type DigestFileStat struct {
	Path    string
	Commits int
}

// RepoDigest is one repository's digest result, or an error (§10.5). When Err
// is non-empty, the aggregate fields are the zero value and must not be
// treated as "zero activity" — Ok distinguishes the two cases explicitly in
// JSON output.
type RepoDigest struct {
	Path         string
	Ok           bool
	Err          string
	Commits      int
	FilesChanged int
	LinesAdded   int
	LinesRemoved int
	ByAuthor     []DigestAuthorStat
	ByTopic      []DigestTopicStat
	TopFiles     []DigestFileStat
}

// DigestArtifact is the fully-computed digest, single- or multi-repo (§10.5):
// a single-repo digest is simply Repos with one element, so there is exactly
// one Go type and one JSON schema for both cases.
type DigestArtifact struct {
	GeneratedAt time.Time
	Days        int
	Since       time.Time
	Until       time.Time
	Repos       []RepoDigest
}

// RenderDigest writes the digest artifact in the requested format to w.
func RenderDigest(w io.Writer, art DigestArtifact, format Format) error {
	switch format {
	case FormatMarkdown, "":
		return renderDigestMarkdown(w, art)
	case FormatText:
		return renderDigestText(w, art)
	case FormatJSON:
		return renderDigestJSON(w, art)
	default:
		return fmt.Errorf("render: unknown output format %q (supported: md, text, json)", format)
	}
}

// digestMarkdownBody builds the Markdown body shared by md and text formats.
func digestMarkdownBody(art DigestArtifact) string {
	var b strings.Builder

	if len(art.Repos) == 1 {
		fmt.Fprintf(&b, "# Digest — last %d days (%s → %s)\n\n", art.Days, dateOnly(art.Since), dateOnly(art.Until))
		writeRepoDigestBody(&b, art.Repos[0], false)
		return b.String()
	}

	fmt.Fprintf(&b, "# Multi-repo digest — last %d days (%s → %s)\n\n", art.Days, dateOnly(art.Since), dateOnly(art.Until))
	for _, r := range art.Repos {
		fmt.Fprintf(&b, "## %s\n\n", r.Path)
		writeRepoDigestBody(&b, r, true)
	}
	writeOverallSummary(&b, art.Repos)

	return b.String()
}

// writeRepoDigestBody writes one repository's section body. When headed is
// true, sub-sections use "###" (nested under a "## <path>" heading);
// otherwise "##" (single-repo top-level sections).
func writeRepoDigestBody(b *strings.Builder, r RepoDigest, headed bool) {
	if !r.Ok {
		fmt.Fprintf(b, "**Error:** %s\n\n", r.Err)
		return
	}

	h := "##"
	if headed {
		h = "###"
	}

	fmt.Fprintf(b, "%s Summary\n\n", h)
	fmt.Fprintf(b, "- Commits: %d\n", r.Commits)
	fmt.Fprintf(b, "- Files touched: %d\n", r.FilesChanged)
	fmt.Fprintf(b, "- Lines: +%d / -%d\n\n", r.LinesAdded, r.LinesRemoved)

	fmt.Fprintf(b, "%s By author\n\n", h)
	if len(r.ByAuthor) == 0 {
		b.WriteString("(no commits in window)\n\n")
	} else {
		b.WriteString("| Author | Commits | Lines (+/-) |\n|---|---|---|\n")
		for _, a := range r.ByAuthor {
			fmt.Fprintf(b, "| %s | %d | +%d / -%d |\n", a.Author, a.Commits, a.LinesAdded, a.LinesRemoved)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(b, "%s By topic\n\n", h)
	if len(r.ByTopic) == 0 {
		b.WriteString("(no commits in window)\n\n")
	} else {
		b.WriteString("| Topic (type) | Commits |\n|---|---|\n")
		for _, t := range r.ByTopic {
			fmt.Fprintf(b, "| %s | %d |\n", t.Topic, t.Commits)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(b, "%s Top changed files\n\n", h)
	if len(r.TopFiles) == 0 {
		b.WriteString("(no files touched in window)\n\n")
	} else {
		b.WriteString("| File | Commits |\n|---|---|\n")
		for _, f := range r.TopFiles {
			fmt.Fprintf(b, "| %s | %d |\n", f.Path, f.Commits)
		}
		b.WriteString("\n")
	}
}

// writeOverallSummary writes the "## Overall summary" section for a
// multi-repo digest (§10.5), combining only the successful repos.
func writeOverallSummary(b *strings.Builder, repos []RepoDigest) {
	var okCount, failCount, combinedCommits, combinedAdded, combinedRemoved int
	var failedPaths []string
	for _, r := range repos {
		if r.Ok {
			okCount++
			combinedCommits += r.Commits
			combinedAdded += r.LinesAdded
			combinedRemoved += r.LinesRemoved
		} else {
			failCount++
			failedPaths = append(failedPaths, r.Path)
		}
	}

	b.WriteString("## Overall summary\n\n")
	fmt.Fprintf(b, "- Repos requested: %d\n", len(repos))
	fmt.Fprintf(b, "- Repos OK: %d\n", okCount)
	if failCount > 0 {
		fmt.Fprintf(b, "- Repos failed: %d (%s)\n", failCount, strings.Join(failedPaths, ", "))
	} else {
		fmt.Fprintf(b, "- Repos failed: %d\n", failCount)
	}
	fmt.Fprintf(b, "- Combined commits: %d\n", combinedCommits)
	fmt.Fprintf(b, "- Combined lines: +%d / -%d\n", combinedAdded, combinedRemoved)
}

// dateOnly renders a time.Time as "YYYY-MM-DD" for human-readable headings
// (§10.3) — the machine-facing git invocation and JSON since/until still use
// RFC3339; this is a display-only alternate representation of the same value.
func dateOnly(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func renderDigestMarkdown(w io.Writer, art DigestArtifact) error {
	if _, err := io.WriteString(w, sanitizeTerminal(digestMarkdownBody(art))); err != nil {
		return fmt.Errorf("render digest markdown: %w", err)
	}
	return nil
}

func renderDigestText(w io.Writer, art DigestArtifact) error {
	if _, err := io.WriteString(w, sanitizeTerminal(stripMarkdown(digestMarkdownBody(art)))); err != nil {
		return fmt.Errorf("render digest text: %w", err)
	}
	return nil
}

// jsonDigest is the wire shape of digest --format=json (§10.3/§10.5). Single-
// and multi-repo digests share this one schema (repos has one element in the
// single-repo case).
type jsonDigest struct {
	SchemaVersion int              `json:"schema_version"`
	Command       string           `json:"command"`
	GeneratedAt   string           `json:"generated_at"`
	Days          int              `json:"days"`
	Since         string           `json:"since"`
	Until         string           `json:"until"`
	Repos         []jsonRepoDigest `json:"repos"`
	Overall       jsonOverall      `json:"overall"`
}

type jsonRepoDigest struct {
	Path     string             `json:"path"`
	Ok       bool               `json:"ok"`
	Error    string             `json:"error"`
	Stats    *jsonDigestStats   `json:"stats"`
	ByAuthor []jsonDigestAuthor `json:"by_author"`
	ByTopic  []jsonDigestTopic  `json:"by_topic"`
	TopFiles []jsonDigestFile   `json:"top_files"`
}

type jsonDigestStats struct {
	Commits      int `json:"commits"`
	FilesChanged int `json:"files_changed"`
	LinesAdded   int `json:"lines_added"`
	LinesRemoved int `json:"lines_removed"`
}

type jsonDigestAuthor struct {
	Author       string `json:"author"`
	Commits      int    `json:"commits"`
	LinesAdded   int    `json:"lines_added"`
	LinesRemoved int    `json:"lines_removed"`
}

type jsonDigestTopic struct {
	Topic   string `json:"topic"`
	Commits int    `json:"commits"`
}

type jsonDigestFile struct {
	Path    string `json:"path"`
	Commits int    `json:"commits"`
}

type jsonOverall struct {
	ReposRequested       int `json:"repos_requested"`
	ReposOK              int `json:"repos_ok"`
	ReposFailed          int `json:"repos_failed"`
	CombinedCommits      int `json:"combined_commits"`
	CombinedLinesAdded   int `json:"combined_lines_added"`
	CombinedLinesRemoved int `json:"combined_lines_removed"`
}

func renderDigestJSON(w io.Writer, art DigestArtifact) error {
	repos := make([]jsonRepoDigest, 0, len(art.Repos))
	overall := jsonOverall{ReposRequested: len(art.Repos)}

	for _, r := range art.Repos {
		jr := jsonRepoDigest{Path: r.Path, Ok: r.Ok, Error: r.Err}
		if r.Ok {
			overall.ReposOK++
			overall.CombinedCommits += r.Commits
			overall.CombinedLinesAdded += r.LinesAdded
			overall.CombinedLinesRemoved += r.LinesRemoved

			jr.Stats = &jsonDigestStats{
				Commits:      r.Commits,
				FilesChanged: r.FilesChanged,
				LinesAdded:   r.LinesAdded,
				LinesRemoved: r.LinesRemoved,
			}
			jr.ByAuthor = make([]jsonDigestAuthor, 0, len(r.ByAuthor))
			for _, a := range r.ByAuthor {
				jr.ByAuthor = append(jr.ByAuthor, jsonDigestAuthor{
					Author: a.Author, Commits: a.Commits,
					LinesAdded: a.LinesAdded, LinesRemoved: a.LinesRemoved,
				})
			}
			jr.ByTopic = make([]jsonDigestTopic, 0, len(r.ByTopic))
			for _, t := range r.ByTopic {
				jr.ByTopic = append(jr.ByTopic, jsonDigestTopic{Topic: t.Topic, Commits: t.Commits})
			}
			jr.TopFiles = make([]jsonDigestFile, 0, len(r.TopFiles))
			for _, f := range r.TopFiles {
				jr.TopFiles = append(jr.TopFiles, jsonDigestFile{Path: f.Path, Commits: f.Commits})
			}
		} else {
			overall.ReposFailed++
		}
		repos = append(repos, jr)
	}

	out := jsonDigest{
		SchemaVersion: DigestSchemaVersion,
		Command:       "digest",
		GeneratedAt:   art.GeneratedAt.UTC().Format(time.RFC3339),
		Days:          art.Days,
		Since:         art.Since.UTC().Format(time.RFC3339),
		Until:         art.Until.UTC().Format(time.RFC3339),
		Repos:         repos,
		Overall:       overall,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("render digest json: %w", err)
	}
	return nil
}
