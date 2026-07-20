// Changelog rendering (§9.4/§9.6 of docs/TECHNICAL_PLAN.md). Lives in the
// same render package as review's Artifact/Render — a second artifact type
// and a second render function, not a second package: the md/text/json
// mechanics (stripMarkdown, indented JSON encoding) are already
// format-agnostic and are reused as-is.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/akomyagin/gitl/internal/gitlog"
)

// ChangelogSchemaVersion is the changelog JSON schema version — a separate
// counter from review's SchemaVersion (§9.4): the two commands have
// structurally unrelated JSON contracts and unrelated consumers, so a shared
// numbering space would bump one command's version on a change that touches
// only the other.
const ChangelogSchemaVersion = 1

// ChangelogEntry is one changelog line, ready for rendering.
type ChangelogEntry struct {
	Hash     string
	Subject  string
	Breaking bool
}

// ChangelogArtifact is the fully-computed changelog, carrying everything the
// renderers need (§9.4).
type ChangelogArtifact struct {
	GeneratedAt time.Time
	Range       string
	// Categories holds every gitlog.CategoryOrder name, always present
	// (possibly with a nil/empty slice) — §9.4 JSON contract.
	Categories map[string][]ChangelogEntry
	// Breaking is the ordered "⚠ BREAKING CHANGES" list, using each entry's
	// BreakingText as Subject (§9.2).
	Breaking []ChangelogEntry
	// MissingRequiredCategories are the policy.required_changelog_categories
	// names with zero entries (§9.3) — always a (possibly empty) slice.
	MissingRequiredCategories []string
}

// NewChangelogArtifact builds a ChangelogArtifact from a categorized
// gitlog.Changelog (§9.5) for the given range and required-category check.
func NewChangelogArtifact(generatedAt time.Time, revRange string, cl gitlog.Changelog, missingRequired []string) ChangelogArtifact {
	categories := make(map[string][]ChangelogEntry, len(gitlog.CategoryOrder))
	for _, name := range gitlog.CategoryOrder {
		for _, e := range cl.Categories[name] {
			categories[name] = append(categories[name], ChangelogEntry{
				Hash:     e.Hash,
				Subject:  e.Subject,
				Breaking: e.Breaking,
			})
		}
	}

	breaking := make([]ChangelogEntry, 0, len(cl.Breaking))
	for _, e := range cl.Breaking {
		breaking = append(breaking, ChangelogEntry{
			Hash:     e.Hash,
			Subject:  e.BreakingText,
			Breaking: true,
		})
	}

	if missingRequired == nil {
		missingRequired = []string{}
	}

	return ChangelogArtifact{
		GeneratedAt:               generatedAt,
		Range:                     revRange,
		Categories:                categories,
		Breaking:                  breaking,
		MissingRequiredCategories: missingRequired,
	}
}

// RenderChangelog writes the changelog artifact in the requested format to w.
// A separate function from Render (not an overload/shared interface) — there
// are exactly two format-agnostic renderers so far, review's and this one;
// per the project's "interface at the second implementation" rule, no shared
// abstraction is introduced until a third, genuinely interchangeable
// consumer appears.
func RenderChangelog(w io.Writer, art ChangelogArtifact, format Format) error {
	switch format {
	case FormatMarkdown, "":
		return renderChangelogMarkdown(w, art)
	case FormatText:
		return renderChangelogText(w, art)
	case FormatJSON:
		return renderChangelogJSON(w, art)
	default:
		return fmt.Errorf("render: unknown output format %q (supported: md, text, json)", format)
	}
}

// changelogMarkdownBody builds the Markdown body shared by md and text
// formats (text further runs it through stripMarkdown).
func changelogMarkdownBody(art ChangelogArtifact) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## [Unreleased] — %s\n", art.Range)

	if !hasAnyEntries(art) {
		b.WriteString("\nNo changes in this range.\n")
		return b.String()
	}

	if len(art.Breaking) > 0 {
		b.WriteString("\n### ⚠ BREAKING CHANGES\n\n")
		for _, e := range art.Breaking {
			fmt.Fprintf(&b, "- **BREAKING**: %s (%s)\n", e.Subject, e.Hash)
		}
	}

	for _, name := range gitlog.CategoryOrder {
		entries := art.Categories[name]
		if len(entries) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", name)
		for _, e := range entries {
			if e.Breaking {
				fmt.Fprintf(&b, "- **BREAKING**: %s (%s)\n", e.Subject, e.Hash)
			} else {
				fmt.Fprintf(&b, "- %s (%s)\n", e.Subject, e.Hash)
			}
		}
	}

	return b.String()
}

// hasAnyEntries reports whether the changelog has anything to print: at
// least one category entry OR one breaking entry. The deterministic
// categorizer always duplicates breaking commits into a category, but the
// --ai path builds Breaking from the model payload independently of
// Categories — a breaking-only artifact with empty categories is a valid
// input here and must not render as "No changes".
func hasAnyEntries(art ChangelogArtifact) bool {
	if len(art.Breaking) > 0 {
		return true
	}
	for _, entries := range art.Categories {
		if len(entries) > 0 {
			return true
		}
	}
	return false
}

func renderChangelogMarkdown(w io.Writer, art ChangelogArtifact) error {
	if _, err := io.WriteString(w, sanitizeTerminal(changelogMarkdownBody(art))); err != nil {
		return fmt.Errorf("render changelog markdown: %w", err)
	}
	return nil
}

func renderChangelogText(w io.Writer, art ChangelogArtifact) error {
	if _, err := io.WriteString(w, sanitizeTerminal(stripMarkdown(changelogMarkdownBody(art)))); err != nil {
		return fmt.Errorf("render changelog text: %w", err)
	}
	return nil
}

// jsonChangelog is the wire shape of changelog --format=json (§9.4). Field
// names are a documented external contract.
type jsonChangelog struct {
	SchemaVersion             int                          `json:"schema_version"`
	Command                   string                       `json:"command"`
	GeneratedAt               string                       `json:"generated_at"`
	Range                     string                       `json:"range"`
	BreakingChanges           []jsonBreakingChange         `json:"breaking_changes"`
	Categories                map[string][]jsonChangeEntry `json:"categories"`
	MissingRequiredCategories []string                     `json:"missing_required_categories"`
}

type jsonBreakingChange struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	// Category is which Keep a Changelog section this same commit landed in.
	Category string `json:"category"`
}

type jsonChangeEntry struct {
	Hash     string `json:"hash"`
	Subject  string `json:"subject"`
	Breaking bool   `json:"breaking"`
}

func renderChangelogJSON(w io.Writer, art ChangelogArtifact) error {
	categories := make(map[string][]jsonChangeEntry, len(gitlog.CategoryOrder))
	categoryOf := make(map[string]string) // hash -> category, for breaking_changes[].category
	for _, name := range gitlog.CategoryOrder {
		entries := art.Categories[name]
		list := make([]jsonChangeEntry, 0, len(entries))
		for _, e := range entries {
			list = append(list, jsonChangeEntry{Hash: e.Hash, Subject: e.Subject, Breaking: e.Breaking})
			if e.Breaking {
				categoryOf[e.Hash] = name
			}
		}
		categories[name] = list
	}

	breaking := make([]jsonBreakingChange, 0, len(art.Breaking))
	for _, e := range art.Breaking {
		breaking = append(breaking, jsonBreakingChange{
			Hash:     e.Hash,
			Subject:  e.Subject,
			Category: categoryOf[e.Hash],
		})
	}

	missing := art.MissingRequiredCategories
	if missing == nil {
		missing = []string{}
	}

	out := jsonChangelog{
		SchemaVersion:             ChangelogSchemaVersion,
		Command:                   "changelog",
		GeneratedAt:               art.GeneratedAt.UTC().Format(time.RFC3339),
		Range:                     art.Range,
		BreakingChanges:           breaking,
		Categories:                categories,
		MissingRequiredCategories: missing,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("render changelog json: %w", err)
	}
	return nil
}
