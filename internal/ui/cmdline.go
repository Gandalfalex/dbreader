package ui

import (
	tea "charm.land/bubbletea/v2"

	tk "github.com/Gandalfalex/tuikit"
)

func (m *model) openCommandLine() tea.Cmd {
	m.cmd.Open(m)
	return m.prompt.Open(inputCommand, tk.ModeCommand, ":", tk.Placeholder, "")
}

func (m *model) handleCommandKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	cmd, err := tk.HandleCommandKey(&m.cmd, m, &m.prompt, msg)
	if err != nil {
		m.SetStatus(err.Error(), true)
	}
	return m, cmd
}

// gotoLine is :42 on whatever the screen shows.
func gotoLine(m *model, n int) {
	h := m.listHeight()
	switch l := m.list(); {
	case l != nil:
		l.Goto(n, h)
	case m.Stack.Top() == scrData:
		g := &m.grid
		g.Cursor = n - 1
		g.Clamp(len(g.vis))
		g.Center(len(g.vis), h)
	case m.Stack.Top() == scrScript:
		f := &m.script
		f.Cursor = n - 1
		f.Clamp(len(f.lines))
		f.Center(len(f.lines), m.H-3)
		f.col = 0
	default:
		m.vp.Goto(n)
	}
}
