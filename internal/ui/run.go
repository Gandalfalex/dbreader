// Package ui is the Bubble Tea terminal interface.
package ui

import (
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"dbreader/internal/config"
)

// Run starts the TUI and blocks until the user quits.
func Run(s *config.Settings, logPath string) error {
	m := newModel(s, logPath)
	st, err := config.LoadState()
	if err != nil {
		slog.Warn("state file unreadable, starting without pins", "err", err)
	}
	m.state = st
	_, err = tea.NewProgram(m).Run()
	m.shutdown()
	return err
}
