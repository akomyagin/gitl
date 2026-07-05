package render

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, "# Title\n\nbody\n\n\n", FormatMarkdown); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := b.String()
	if got != "# Title\n\nbody\n" {
		t.Errorf("Render normalized output = %q", got)
	}
}

func TestRenderEmptyFormatDefaultsToMarkdown(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, "hello", ""); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if b.String() != "hello\n" {
		t.Errorf("got %q", b.String())
	}
}

func TestRenderUnimplementedFormats(t *testing.T) {
	for _, f := range []Format{FormatText, FormatJSON} {
		var b strings.Builder
		if err := Render(&b, "x", f); err == nil {
			t.Errorf("Render(%q) expected not-implemented error, got nil", f)
		}
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	var b strings.Builder
	if err := Render(&b, "x", Format("xml")); err == nil {
		t.Error("expected error for unknown format")
	}
}
