package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/akomyagin/gitl/internal/render"
)

// Model is the bubbletea model for the interactive digest viewer. It holds the
// pre-rendered digest as a slice of lines and a scroll offset; all data is
// computed before the TUI starts, so Update only handles navigation.
type Model struct {
	art    render.DigestArtifact
	lines  []string
	offset int
	height int
	width  int
	ready  bool
}

// New pre-renders the digest artifact to text lines for scrolling.
func New(art render.DigestArtifact) Model {
	// Pre-render to text lines. RenderDigest writes to a strings.Builder.
	var buf strings.Builder
	_ = render.RenderDigest(&buf, art, render.FormatText)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	return Model{art: art, lines: lines}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height - 2 // reserve 1 line for footer hint
		m.width = msg.Width
		m.ready = true
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.offset > 0 {
				m.offset--
			}
		case "down", "j":
			if m.offset+m.height < len(m.lines) {
				m.offset++
			}
		case "pgup":
			m.offset -= m.height
			if m.offset < 0 {
				m.offset = 0
			}
		case "pgdn":
			m.offset += m.height
			if m.offset+m.height > len(m.lines) {
				m.offset = len(m.lines) - m.height
				if m.offset < 0 {
					m.offset = 0
				}
			}
		}
	}
	return m, nil
}

func (m Model) View() string {
	if !m.ready {
		return "Loading..."
	}
	end := m.offset + m.height
	if end > len(m.lines) {
		end = len(m.lines)
	}
	visible := m.lines[m.offset:end]
	body := strings.Join(visible, "\n")
	footer := footerStyle.Render("↑/↓ j/k scroll · PgUp/PgDn · q quit")
	return body + "\n" + footer
}
