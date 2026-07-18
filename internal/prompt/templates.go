// Package prompt builds LLM prompts from git history.
//
// The default review system prompt is embedded from templates/review_system.tmpl
// (byte-identical to the historical hardcoded string). Custom user templates
// (text/template) can override it via BuildReviewWithTemplate (Item 3). The
// template requires the model to end with a fenced `risk` JSON block (§7.1),
// parsed back in internal/llm.
package prompt

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/akomyagin/gitl/internal/gitlog"
	"github.com/akomyagin/gitl/internal/render"
)

// Review is the input for building a review prompt.
type Review struct {
	Range   string
	Commits []gitlog.Commit
	Diff    string
	// Staged marks a review of staged (indexed, not yet committed) changes:
	// there is no revision range and no commit metadata, only the diff. The
	// user message then carries an explicit "staged changes" note instead of
	// the range/commit sections. Zero value keeps the historical range-based
	// prompt byte-identical.
	Staged bool
}

// BuildReview renders the system and user messages for a review. The user
// message carries the commit metadata (subjects, authors, bodies, file lists)
// followed by the full unified diff in a fenced block. It uses the embedded
// default system prompt (equivalent to BuildReviewWithTemplate(r, "")).
func BuildReview(r Review) (system, user string) {
	return defaultReviewSystem, buildUserMessage(r)
}

// BuildReviewWithTemplate renders the system prompt from a custom template file.
// If systemTemplateFile is empty, the embedded default is used (identical to
// BuildReview). Otherwise the file is parsed as a text/template and executed
// with the Review as data (fields Range, Commits, Diff). The user message is
// built identically in both cases.
func BuildReviewWithTemplate(r Review, systemTemplateFile string) (system, user string, err error) {
	user = buildUserMessage(r)
	if systemTemplateFile == "" {
		return defaultReviewSystem, user, nil
	}
	tmpl, err := template.New(filepath.Base(systemTemplateFile)).Funcs(render.TemplateFuncs()).ParseFiles(systemTemplateFile)
	if err != nil {
		return "", "", fmt.Errorf("parse system template %q: %w", systemTemplateFile, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, r); err != nil {
		return "", "", fmt.Errorf("execute system template %q: %w", systemTemplateFile, err)
	}
	if buf.Len() == 0 {
		return "", "", fmt.Errorf("system template %q produced empty output — ensure the template has content outside {{define}} blocks", systemTemplateFile)
	}
	return buf.String(), user, nil
}

// buildUserMessage assembles the user message shared by BuildReview and
// BuildReviewWithTemplate: commit metadata followed by the fenced unified diff.
func buildUserMessage(r Review) string {
	var b strings.Builder

	if r.Staged {
		b.WriteString("Review the staged changes (not yet committed).\n\n")
	} else {
		fmt.Fprintf(&b, "Review the git range `%s` (%d commit(s)).\n\n", r.Range, len(r.Commits))
	}

	b.WriteString("# Commits\n\n")
	switch {
	case r.Staged:
		b.WriteString("(staged changes — not yet committed, no commit history)\n\n")
	case len(r.Commits) == 0:
		b.WriteString("(no commits in range)\n\n")
	}
	for _, c := range r.Commits {
		fmt.Fprintf(&b, "## %s — %s (%s, %s)\n", shortHash(c.Hash), c.Subject, c.Author, c.Date.Format("2006-01-02"))
		if c.Body != "" {
			b.WriteString("\n")
			for _, line := range strings.Split(c.Body, "\n") {
				b.WriteString("> " + line + "\n")
			}
		}
		if len(c.Files) > 0 {
			b.WriteString("\nFiles:\n")
			for _, f := range c.Files {
				if f.Old != "" {
					fmt.Fprintf(&b, "- %s %s -> %s\n", f.Status, f.Old, f.Path)
				} else {
					fmt.Fprintf(&b, "- %s %s\n", f.Status, f.Path)
				}
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("# Diff\n\n")
	if strings.TrimSpace(r.Diff) == "" {
		b.WriteString("(empty diff)\n")
	} else {
		b.WriteString("```diff\n")
		b.WriteString(strings.TrimRight(r.Diff, "\n"))
		b.WriteString("\n```\n")
	}

	return b.String()
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// Changelog is the input for building a `changelog --ai` prompt: the raw
// commit list plus the deterministic categorization, which the model receives
// as a starting point (it rewrites prose and may reclassify significant
// non-conventional commits out of Other).
type Changelog struct {
	Range   string
	Commits []gitlog.Commit
	Grouped gitlog.Changelog
}

// BuildChangelog renders the system and user messages for an AI changelog
// using the embedded default system prompt (equivalent to
// BuildChangelogWithTemplate(c, "")).
func BuildChangelog(c Changelog) (system, user string) {
	return defaultChangelogSystem, buildChangelogUserMessage(c)
}

// BuildChangelogWithTemplate renders the system prompt from a custom template
// file, mirroring BuildReviewWithTemplate. If systemTemplateFile is empty the
// embedded default is used. Otherwise the file is parsed as a text/template
// and executed with the Changelog as data (fields Range, Commits, Grouped).
// The user message is built identically in both cases.
func BuildChangelogWithTemplate(c Changelog, systemTemplateFile string) (system, user string, err error) {
	user = buildChangelogUserMessage(c)
	if systemTemplateFile == "" {
		return defaultChangelogSystem, user, nil
	}
	tmpl, err := template.New(filepath.Base(systemTemplateFile)).Funcs(render.TemplateFuncs()).ParseFiles(systemTemplateFile)
	if err != nil {
		return "", "", fmt.Errorf("parse system template %q: %w", systemTemplateFile, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, c); err != nil {
		return "", "", fmt.Errorf("execute system template %q: %w", systemTemplateFile, err)
	}
	if buf.Len() == 0 {
		return "", "", fmt.Errorf("system template %q produced empty output — ensure the template has content outside {{define}} blocks", systemTemplateFile)
	}
	return buf.String(), user, nil
}

// buildChangelogUserMessage assembles the changelog user message: the range,
// the full commit list (short hash + subject + quoted body), and the
// deterministic grouping the model starts from.
func buildChangelogUserMessage(c Changelog) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Rewrite the changelog for the git range `%s` (%d commit(s)).\n\n", c.Range, len(c.Commits))

	b.WriteString("# Commits\n\n")
	if len(c.Commits) == 0 {
		b.WriteString("(no commits in range)\n")
	}
	for _, cm := range c.Commits {
		fmt.Fprintf(&b, "- %s %s\n", shortHash(cm.Hash), cm.Subject)
		if body := strings.TrimSpace(cm.Body); body != "" {
			for _, line := range strings.Split(body, "\n") {
				b.WriteString("  > " + line + "\n")
			}
		}
	}

	b.WriteString("\n# Deterministic grouping (starting point)\n\n")
	for _, name := range gitlog.CategoryOrder {
		entries := c.Grouped.Categories[name]
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n", name)
		for _, e := range entries {
			fmt.Fprintf(&b, "- %s (%s)\n", e.Subject, e.Hash)
		}
		b.WriteString("\n")
	}
	if len(c.Grouped.Breaking) > 0 {
		b.WriteString("### BREAKING CHANGES\n")
		for _, e := range c.Grouped.Breaking {
			fmt.Fprintf(&b, "- %s (%s)\n", e.BreakingText, e.Hash)
		}
	}

	return b.String()
}
