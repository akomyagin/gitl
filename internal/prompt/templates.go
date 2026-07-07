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
	"strings"
	"text/template"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// Review is the input for building a review prompt.
type Review struct {
	Range   string
	Commits []gitlog.Commit
	Diff    string
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
	tmpl, err := template.ParseFiles(systemTemplateFile)
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

	fmt.Fprintf(&b, "Review the git range `%s` (%d commit(s)).\n\n", r.Range, len(r.Commits))

	b.WriteString("# Commits\n\n")
	if len(r.Commits) == 0 {
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
