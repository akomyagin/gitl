// Package prompt builds LLM prompts from git history.
//
// It ships a single hardcoded review template as plain Go strings — one
// template does not justify //go:embed or text/template machinery. Custom
// user templates (text/template) are post-MVP. The template requires the model
// to end with a fenced `risk` JSON block (§7.1), parsed back in internal/llm.
package prompt

import (
	"fmt"
	"strings"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// Review is the input for building a review prompt.
type Review struct {
	Range   string
	Commits []gitlog.Commit
	Diff    string
}

// reviewSystem is the hardcoded system prompt for `gitl review`. The trailing
// risk block (item 4) is the machine-readable score parsed by internal/llm
// (§7.1); it must be the very last thing the model emits.
const reviewSystem = "You are an experienced senior software engineer performing a code review of a git commit range.\n" +
	"\n" +
	"Write a concise, structured review in Markdown with these sections:\n" +
	"1. \"## Summary\" — what this range does overall, 2-4 sentences.\n" +
	"2. \"## Notable changes\" — bullet list of the most important changes.\n" +
	"3. \"## Concerns\" — potential bugs, design issues, missing tests, security-sensitive spots. Be specific: reference files and hunks. If there are none, say so explicitly.\n" +
	"4. End your answer with a separate fenced code block using the language `risk`, containing exactly one single-line JSON object, and this must be the very last thing you output:\n" +
	"   ```risk\n" +
	"   {\"level\": \"low|medium|high\", \"summary\": \"<one sentence, up to 140 characters>\"}\n" +
	"   ```\n" +
	"\n" +
	"Ground every statement in the commits and diff you are given. Ignore pure formatting churn (whitespace, generated/lock files). Do not invent changes that are not in the diff."

// BuildReview renders the system and user messages for a review. The user
// message carries the commit metadata (subjects, authors, bodies, file lists)
// followed by the full unified diff in a fenced block.
func BuildReview(r Review) (system, user string) {
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

	return reviewSystem, b.String()
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
