package tui

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/akomyagin/gitl/internal/render"
)

var update = flag.Bool("update", false, "update golden files")

// TestMain disables ANSI color so View() output is deterministic for the
// golden comparison.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

func fixtureArtifact() render.DigestArtifact {
	return render.DigestArtifact{
		GeneratedAt: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		Days:        7,
		Since:       time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
		Until:       time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		Repos: []render.RepoDigest{{
			Path:         "/repo/foo",
			Ok:           true,
			Commits:      3,
			FilesChanged: 5,
			LinesAdded:   120,
			LinesRemoved: 30,
			ByAuthor:     []render.DigestAuthorStat{{Author: "Alice", Commits: 2, LinesAdded: 100, LinesRemoved: 20}},
			ByTopic:      []render.DigestTopicStat{{Topic: "feat", Commits: 2}},
			TopFiles:     []render.DigestFileStat{{Path: "main.go", Commits: 2}},
		}},
	}
}

// TestGoldenView renders the model at a fixed size and compares it against a
// golden file. On first run (or with -update) the golden is generated.
func TestGoldenView(t *testing.T) {
	m := New(fixtureArtifact())
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = m2.(Model)

	got := m.View()

	goldenPath := filepath.Join("testdata", "view_80x24.golden")
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		if !os.IsNotExist(err) && !*update {
			t.Fatalf("read golden: %v", err)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return // first run: golden generated, nothing to compare against
	}

	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
		return
	}

	if got != string(want) {
		t.Errorf("view mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestNavigation(t *testing.T) {
	m := New(fixtureArtifact())
	// Initialize size
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m = m2.(Model)

	// Scroll down multiple times — offset must not exceed len(lines)-height
	for i := 0; i < 100; i++ {
		m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		m = m2.(Model)
	}
	maxOffset := len(m.lines) - m.height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		t.Errorf("offset %d exceeded max %d", m.offset, maxOffset)
	}

	// Scroll up — offset must reach 0
	for i := 0; i < 100; i++ {
		m2, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
		m = m2.(Model)
	}
	if m.offset != 0 {
		t.Errorf("offset %d after scrolling up, want 0", m.offset)
	}

	// q returns tea.Quit
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Error("q key should return tea.Quit cmd")
	}
}
