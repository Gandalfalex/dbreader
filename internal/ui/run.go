// Package ui is the Bubble Tea terminal interface.
package ui

import (
	tea "charm.land/bubbletea/v2"

	"dbreader/internal/config"
)

// Run starts the TUI and blocks until the user quits.
func Run(s *config.Settings, logPath string) error {
	m := newModel(s, logPath)
	_, err := tea.NewProgram(m).Run()
	m.shutdown()
	return err
}
