package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/Gandalfalex/vim-kit/cmdline"
	"github.com/Gandalfalex/vim-kit/keys"
)

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit // Run rolls back an open transaction
	}
	if m.pending != nil {
		return m.handlePendingKey(k)
	}
	switch m.prompt.Kind {
	case inputCommand:
		return m.handleCommandKey(msg)
	case inputConfirm:
		return m.handleConfirmKey(msg)
	case inputFilter, inputSQL:
		return m.handleInput(msg)
	case inputFind, inputSearch:
		return m.handlePromptKey(msg)
	}
	m.SetStatus("", false) // messages last until the next key
	if m.Stack.Top() == scrScript && m.script.insert {
		return m.handleInsertKey(msg)
	}

	// esc cancels in-flight work first
	if k == "esc" && m.cancelOp() {
		m.Keys.Reset()
		m.SetStatus("cancelled", false)
		return m, nil
	}

	km, actions := pagerKeymap, pagerActions
	switch {
	case m.list() != nil:
		km, actions = listKeymap, listActions
	case m.Stack.Top() == scrData:
		km, actions = gridKeymap, gridActions
	case m.Stack.Top() == scrScript && m.script.sel.Active():
		km, actions = visualKeymap, visualActions
	case m.Stack.Top() == scrScript:
		km, actions = scriptKeymap, editActions
	}
	ev, ok := m.Keys.Feed(km, k)
	if !ok {
		return m, nil // count digit or pending g/z
	}
	if ev.Action != actQuit {
		m.Disarm()
	}
	if ev.Seq == ":" {
		return m, m.openCommandLine()
	}
	if !m.move(ev) {
		if fn, ok := actions[ev.Action]; ok {
			return fn(m, ev)
		}
		if fn, ok := shared[ev.Action]; ok {
			return fn(m, ev)
		}
	}
	return m, nil
}

// move applies ev as a motion on the current screen.
func (m *model) move(ev keys.Event) bool {
	h := m.listHeight()
	if l := m.list(); l != nil {
		return l.Apply(ev, h)
	}
	if m.Stack.Top() == scrData {
		g := &m.grid
		defer g.Scroll(len(g.vis), h)
		return g.Apply(ev, len(g.vis), h) || g.applyCol(ev)
	}
	if m.Stack.Top() == scrScript {
		return m.script.apply(ev, m.H-3)
	}
	return m.vp.Apply(ev)
}

func (m *model) handleInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	sql := m.prompt.Kind == inputSQL
	switch msg.String() {
	case "esc", "ctrl+[":
		m.sqlHist.Close()
		if !sql {
			m.applyFilter(m.prevFilter)
		}
		m.prompt.Close()
		return m, nil
	case "enter":
		m.prompt.Close()
		if sql {
			m.sqlHist.Close()
			return m, m.runTyped(m.prompt.Input.Value())
		}
		return m, nil
	}
	if sql {
		switch k := msg.String(); k {
		case "tab", "ctrl+n", "shift+tab", "ctrl+p":
			dir := 1
			if k == "shift+tab" || k == "ctrl+p" {
				dir = -1
			}
			if val, ok := m.sqlHist.Cycle(dir); ok {
				m.prompt.Input.SetValue(val)
				m.prompt.Input.SetCursor(len([]rune(val)) - len([]rune(m.complTail)))
			}
			return m, nil
		case "up", "down":
			dir := 1
			if k == "up" {
				dir = -1
			}
			if val, ok := m.sqlHist.Recall(dir); ok {
				m.prompt.Input.SetValue(val)
				m.prompt.Input.CursorEnd()
				m.sqlHist.SetItems(nil)
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.prompt.Input, cmd = m.prompt.Input.Update(msg)
	if sql {
		var items []cmdline.Item
		items, m.complTail = completeSQL(m.prompt.Input.Value(), m.prompt.Input.Position(), m.sqlCatalog())
		m.sqlHist.SetItems(items)
	} else {
		m.applyFilter(m.prompt.Input.Value())
	}
	return m, cmd
}

// sqlCatalog is what the sql prompt completes from: the tables of the
// schema being browsed and their columns, once those have loaded.
func (m *model) sqlCatalog() sqlCatalog {
	cat := sqlCatalog{quote: m.conn.Quote, fold: m.conn.Dialect().Fold}
	for _, e := range m.tables.All {
		cat.tables = append(cat.tables, e.Name)
	}
	for _, e := range m.schemas.All {
		cat.schemas = append(cat.schemas, e.Name)
	}
	if m.colSchema == m.schema {
		cat.columns = m.colNames
	}
	return cat
}

// applyFilter filters the list or grid on screen.
func (m *model) applyFilter(f string) {
	if l := m.list(); l != nil {
		l.SetFilter(f)
	} else if m.Stack.Top() == scrData {
		m.grid.setFilter(f)
	}
}
