// Package render turns a computed review artifact into output.
//
// It supports three formats (§7.4): "md" (the model's Markdown with a short
// risk header prepended), "json" (the versioned, documented external contract —
// field names matter, schema_version bumps only on breaking changes), and
// "text" (the same content as Markdown with #/**/backtick markup crudely
// stripped — intentionally rough, no real Markdown parser).
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// Format is an output format.
type Format string

const (
	FormatMarkdown Format = "md"
	FormatText     Format = "text"
	FormatJSON     Format = "json"
)

// SchemaVersion is the JSON output schema version (§7.4). Bumped only on a
// breaking change (renaming/removing a field or changing a type); adding an
// optional field does not change it.
const SchemaVersion = 1

// Artifact is the fully-computed review, carrying everything the renderers need
// (§7.4). RiskLevel/RiskSummary are always populated (offline and the heuristic
// fallback guarantee a value).
type Artifact struct {
	GeneratedAt time.Time
	Range       string
	Offline     bool
	Provider    string
	Model       string
	RiskLevel   string
	RiskSummary string
	// RiskHeuristic is true when RiskLevel/RiskSummary came from the deterministic
	// heuristic rather than the model's own risk block (offline mode, or the model
	// omitted a valid risk block). Surfaced in all three output formats.
	RiskHeuristic bool
	Stats         Stats
	Commits       []Commit
	// ReviewMarkdown is the model's review body with the trailing risk block
	// already stripped.
	ReviewMarkdown string
}

// Stats are the aggregate diff counts for a review.
type Stats struct {
	Commits      int
	FilesChanged int
	LinesAdded   int
	LinesRemoved int
}

// Commit is the per-commit metadata included in the JSON output.
type Commit struct {
	Hash    string
	Author  string
	Date    time.Time
	Subject string
}

// Render writes the artifact in the requested format to w.
func Render(w io.Writer, art Artifact, format Format) error {
	switch format {
	case FormatMarkdown, "":
		return renderMarkdown(w, art)
	case FormatText:
		return renderText(w, art)
	case FormatJSON:
		return renderJSON(w, art)
	default:
		return fmt.Errorf("render: unknown output format %q (supported: md, text, json)", format)
	}
}

// tmplFuncs are the helpers exposed to output templates (Item 3). Custom
// templates use the same helpers the embedded default relies on.
var tmplFuncs = template.FuncMap{
	"upper":                strings.ToUpper,
	"trimTrailingNewlines": func(s string) string { return strings.TrimRight(s, "\n") },
}

// TemplateFuncs returns the FuncMap available to output and system templates.
// Callers (config validation, prompt building) use this to register the same
// functions so template parse errors surface at load time, not at render time.
func TemplateFuncs() template.FuncMap { return tmplFuncs }

// RenderWithTemplate behaves like Render, but uses a custom text/template file
// for the md format when outputTemplateFile is non-empty. For text/json,
// outputTemplateFile is ignored (a warning is logged via slog.Warn when a path
// was set, since it has no effect on those formats).
func RenderWithTemplate(w io.Writer, art Artifact, format Format, outputTemplateFile string) error {
	if format != FormatMarkdown && format != "" {
		if outputTemplateFile != "" {
			slog.Warn("output.template_file is ignored for non-markdown format", "format", string(format))
		}
		return Render(w, art, format)
	}
	if outputTemplateFile == "" {
		return Render(w, art, format)
	}
	name := filepath.Base(outputTemplateFile)
	tmpl, err := template.New(name).Funcs(tmplFuncs).ParseFiles(outputTemplateFile)
	if err != nil {
		return fmt.Errorf("parse output template %q: %w", outputTemplateFile, err)
	}
	// Buffer the output so that a mid-template error never leaves partial
	// content on the caller's writer (e.g. stdout). Also catches the silent
	// empty-output case produced by {{define}}-only templates.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, art); err != nil {
		return fmt.Errorf("execute output template %q: %w", outputTemplateFile, err)
	}
	if buf.Len() == 0 {
		return fmt.Errorf("output template %q produced empty output — ensure the template has content outside {{define}} blocks", outputTemplateFile)
	}
	if _, err := io.Copy(w, &buf); err != nil {
		return fmt.Errorf("write rendered output: %w", err)
	}
	return nil
}

// riskHeader returns the "**Risk:** LEVEL — summary" line (§7.4). Appends
// "*(heuristic)*" when the score was computed by the deterministic fallback
// rather than the model, so users can distinguish the two.
func riskHeader(art Artifact) string {
	level := strings.ToUpper(art.RiskLevel)
	var h string
	if art.RiskSummary != "" {
		h = fmt.Sprintf("**Risk:** %s — %s", level, art.RiskSummary)
	} else {
		h = fmt.Sprintf("**Risk:** %s", level)
	}
	if art.RiskHeuristic {
		h += " *(heuristic)*"
	}
	return h
}

// renderMarkdown prepends the risk header to the review body.
func renderMarkdown(w io.Writer, art Artifact) error {
	body := strings.TrimRight(art.ReviewMarkdown, "\n")
	out := riskHeader(art) + "\n\n" + body + "\n"
	if _, err := io.WriteString(w, out); err != nil {
		return fmt.Errorf("render markdown: %w", err)
	}
	return nil
}

// renderText emits the Markdown content with #/**/backtick markup crudely
// stripped. No real Markdown parser (§7.4) — this is intentionally rough.
func renderText(w io.Writer, art Artifact) error {
	body := strings.TrimRight(art.ReviewMarkdown, "\n")
	combined := riskHeader(art) + "\n\n" + body
	if _, err := io.WriteString(w, stripMarkdown(combined)+"\n"); err != nil {
		return fmt.Errorf("render text: %w", err)
	}
	return nil
}

// stripMarkdown removes the most common Markdown decorations (heading #,
// emphasis **/*, inline-code backticks) line by line. Crude by design.
func stripMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		indent := line[:len(line)-len(trimmed)]
		// Strip leading heading hashes: "## Summary" → "Summary".
		if strings.HasPrefix(trimmed, "#") {
			trimmed = strings.TrimLeft(trimmed, "#")
			trimmed = strings.TrimLeft(trimmed, " ")
		}
		// Drop bold/italic markers and inline-code backticks.
		trimmed = strings.ReplaceAll(trimmed, "**", "")
		trimmed = strings.ReplaceAll(trimmed, "`", "")
		trimmed = strings.ReplaceAll(trimmed, "*", "")
		lines[i] = indent + trimmed
	}
	return strings.Join(lines, "\n")
}

// jsonArtifact is the wire shape of the JSON output (§7.4). Field names are a
// documented external contract — do not rename without bumping SchemaVersion.
type jsonArtifact struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   string       `json:"generated_at"`
	Range         string       `json:"range"`
	Offline       bool         `json:"offline"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	Risk          jsonRisk     `json:"risk"`
	Stats         jsonStats    `json:"stats"`
	Commits       []jsonCommit `json:"commits"`
	ReviewMD      string       `json:"review_markdown"`
}

type jsonRisk struct {
	Level     string `json:"level"`
	Summary   string `json:"summary"`
	Heuristic bool   `json:"heuristic"`
}

type jsonStats struct {
	Commits      int `json:"commits"`
	FilesChanged int `json:"files_changed"`
	LinesAdded   int `json:"lines_added"`
	LinesRemoved int `json:"lines_removed"`
}

type jsonCommit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// renderJSON emits the versioned JSON contract (§7.4). Timestamps are RFC3339
// UTC.
func renderJSON(w io.Writer, art Artifact) error {
	commits := make([]jsonCommit, 0, len(art.Commits))
	for _, c := range art.Commits {
		commits = append(commits, jsonCommit{
			Hash:    c.Hash,
			Author:  c.Author,
			Date:    c.Date.UTC().Format(time.RFC3339),
			Subject: c.Subject,
		})
	}

	out := jsonArtifact{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   art.GeneratedAt.UTC().Format(time.RFC3339),
		Range:         art.Range,
		Offline:       art.Offline,
		Provider:      art.Provider,
		Model:         art.Model,
		Risk:          jsonRisk{Level: art.RiskLevel, Summary: art.RiskSummary, Heuristic: art.RiskHeuristic},
		Stats: jsonStats{
			Commits:      art.Stats.Commits,
			FilesChanged: art.Stats.FilesChanged,
			LinesAdded:   art.Stats.LinesAdded,
			LinesRemoved: art.Stats.LinesRemoved,
		},
		Commits:  commits,
		ReviewMD: strings.TrimRight(art.ReviewMarkdown, "\n"),
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("render json: %w", err)
	}
	return nil
}
