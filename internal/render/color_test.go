package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestColorForLevel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		level string
		want  string
	}{
		{"high", ansiRed},
		{"HIGH", ansiRed},
		{" High ", ansiRed},
		{"medium", ansiYellow},
		{"MEDIUM", ansiYellow},
		{"low", ansiGreen},
		{"Low", ansiGreen},
		{"", ""},
		{"critical", ""},
		{"unknown", ""},
	}
	for _, tc := range cases {
		if got := colorForLevel(tc.level); got != tc.want {
			t.Errorf("colorForLevel(%q) = %q, want %q", tc.level, got, tc.want)
		}
	}
}

// stripOwnColorCodes removes the SGR sequences this package itself emits, so a
// test can assert that no OTHER (injected) escape bytes survived.
func stripOwnColorCodes(s string) string {
	for _, code := range []string{ansiRed, ansiYellow, ansiGreen, ansiReset} {
		s = strings.ReplaceAll(s, code, "")
	}
	return s
}

func TestRiskHeaderColoredWrapsLevelTokenOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		level string
		code  string
	}{
		{"high", ansiRed},
		{"medium", ansiYellow},
		{"low", ansiGreen},
	}
	for _, tc := range cases {
		art := sampleArtifact()
		art.RiskLevel = tc.level
		got := riskHeaderColored(art, true)
		wrapped := tc.code + strings.ToUpper(tc.level) + ansiReset
		if !strings.Contains(got, wrapped) {
			t.Errorf("level %q: header %q missing wrapped token %q", tc.level, got, wrapped)
		}
		if n := strings.Count(got, tc.code); n != 1 {
			t.Errorf("level %q: color code appears %d times, want exactly 1:\n%q", tc.level, n, got)
		}
		if n := strings.Count(got, ansiReset); n != 1 {
			t.Errorf("level %q: reset appears %d times, want exactly 1:\n%q", tc.level, n, got)
		}
	}
}

// TestRiskHeaderColoredInjectionSafety: an attacker-controlled summary with
// embedded escape sequences is still sanitized; the ONLY ESC bytes in the
// colored header are our own SGR codes around the trusted level token.
func TestRiskHeaderColoredInjectionSafety(t *testing.T) {
	t.Parallel()
	art := sampleArtifact()
	art.RiskLevel = "high"
	art.RiskSummary = "summary\x1b[8mHIDDEN\x1b[0m"
	got := riskHeaderColored(art, true)
	residual := stripOwnColorCodes(got)
	if strings.ContainsRune(residual, 0x1b) {
		t.Errorf("injected ESC byte survived outside our own color codes:\n%q", got)
	}
	if !strings.Contains(got, "HIDDEN") {
		t.Errorf("visible summary content lost:\n%q", got)
	}
}

func TestRiskHeaderColoredHeuristicAnnotation(t *testing.T) {
	t.Parallel()
	art := sampleArtifact()
	art.RiskHeuristic = true
	got := riskHeaderColored(art, true)
	if !strings.Contains(got, "*(heuristic)*") {
		t.Errorf("colored header lost the heuristic annotation:\n%q", got)
	}
}

// TestRiskHeaderColoredNoColorMatchesPlain: the color=false path is
// byte-identical to the pre-color pipeline (regression guard tying into the
// golden files, which all render via the plain path).
func TestRiskHeaderColoredNoColorMatchesPlain(t *testing.T) {
	t.Parallel()
	art := sampleArtifact()
	want := sanitizeTerminal(riskHeader(art))
	if got := riskHeaderColored(art, false); got != want {
		t.Errorf("riskHeaderColored(art, false) = %q, want %q", got, want)
	}
	if strings.ContainsRune(riskHeaderColored(art, false), 0x1b) {
		t.Error("no-color header must not contain ESC")
	}
}

// TestRiskHeaderColoredUnknownLevelNoEscape: an out-of-enum level yields no
// color at all (defense in depth), never a stray escape.
func TestRiskHeaderColoredUnknownLevelNoEscape(t *testing.T) {
	t.Parallel()
	art := sampleArtifact()
	art.RiskLevel = "critical"
	got := riskHeaderColored(art, true)
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("unknown level must not emit ANSI:\n%q", got)
	}
}

func TestRiskHeaderLineColored(t *testing.T) {
	t.Parallel()
	// false path is byte-identical to the existing color-free RiskHeaderLine.
	plain := RiskHeaderLine("high", "bad\x1b[8mHIDDEN\x1b[0m", true)
	if got := RiskHeaderLineColored("high", "bad\x1b[8mHIDDEN\x1b[0m", true, false); got != plain {
		t.Errorf("RiskHeaderLineColored(color=false) = %q, want %q", got, plain)
	}
	// true path wraps the level token; no stray ESC beyond our codes.
	got := RiskHeaderLineColored("high", "bad\x1b[8mHIDDEN\x1b[0m", false, true)
	if !strings.Contains(got, ansiRed+"HIGH"+ansiReset) {
		t.Errorf("colored streaming header missing wrapped level:\n%q", got)
	}
	if strings.ContainsRune(stripOwnColorCodes(got), 0x1b) {
		t.Errorf("injected ESC byte survived in streaming header:\n%q", got)
	}
}

// TestRenderColorMarkdownAndText: the level token is colored in md and text
// human output; the body stays plain (exactly one color+reset pair total).
func TestRenderColorMarkdownAndText(t *testing.T) {
	t.Parallel()
	for _, format := range []Format{FormatMarkdown, FormatText} {
		var b strings.Builder
		art := sampleArtifact() // medium → yellow
		if err := RenderColor(&b, art, format, true); err != nil {
			t.Fatalf("RenderColor %s: %v", format, err)
		}
		got := b.String()
		if !strings.Contains(got, ansiYellow+"MEDIUM"+ansiReset) {
			t.Errorf("%s output missing colored level token:\n%q", format, got)
		}
		if n := strings.Count(got, "\x1b"); n != 2 {
			t.Errorf("%s output has %d ESC bytes, want exactly 2 (color+reset):\n%q", format, n, got)
		}
	}
}

// TestRenderColorFalseMatchesRender: with color=false every format is
// byte-identical to Render — goldens and existing callers are unaffected.
func TestRenderColorFalseMatchesRender(t *testing.T) {
	t.Parallel()
	for _, format := range []Format{FormatMarkdown, FormatText, FormatJSON} {
		var want, got strings.Builder
		if err := Render(&want, sampleArtifact(), format); err != nil {
			t.Fatalf("Render %s: %v", format, err)
		}
		if err := RenderColor(&got, sampleArtifact(), format, false); err != nil {
			t.Fatalf("RenderColor %s: %v", format, err)
		}
		if got.String() != want.String() {
			t.Errorf("RenderColor(color=false) differs from Render for %s:\ngot:\n%q\nwant:\n%q",
				format, got.String(), want.String())
		}
	}
}

// TestRenderColorJSONNeverColored: the JSON machine contract carries no raw
// ANSI even when color is requested; injected control bytes stay escaped.
func TestRenderColorJSONNeverColored(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderColor(&b, escapeInjectionArtifact(), FormatJSON, true); err != nil {
		t.Fatalf("RenderColor json: %v", err)
	}
	got := b.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("JSON output contains raw ESC byte:\n%q", got)
	}
	if !strings.Contains(got, "\\u001b") {
		t.Errorf("JSON output should keep control bytes escaped as \\u001b:\n%q", got)
	}
}

// TestRenderWithTemplateColorCustomTemplateStaysPlain: custom-template output
// is never colorized — the user owns the template.
func TestRenderWithTemplateColorCustomTemplateStaysPlain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tmpl")
	if err := os.WriteFile(path, []byte("Risk: {{upper .RiskLevel}} — {{.RiskSummary}}\n"), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	var b strings.Builder
	if err := RenderWithTemplateColor(&b, sampleArtifact(), FormatMarkdown, path, true); err != nil {
		t.Fatalf("RenderWithTemplateColor: %v", err)
	}
	if strings.ContainsRune(b.String(), 0x1b) {
		t.Errorf("templated output must stay color-free:\n%q", b.String())
	}
}

// TestRenderWithTemplateColorNoTemplate: without a template file the built-in
// header path is colorized.
func TestRenderWithTemplateColorNoTemplate(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := RenderWithTemplateColor(&b, sampleArtifact(), FormatMarkdown, "", true); err != nil {
		t.Fatalf("RenderWithTemplateColor: %v", err)
	}
	if !strings.Contains(b.String(), ansiYellow+"MEDIUM"+ansiReset) {
		t.Errorf("built-in header not colorized:\n%q", b.String())
	}
}
