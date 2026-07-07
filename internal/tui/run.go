package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/akomyagin/gitl/internal/render"
)

// Run launches the TUI digest viewer. It blocks until the user quits.
func Run(ctx context.Context, art render.DigestArtifact) error {
	p := tea.NewProgram(New(art), tea.WithContext(ctx), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
