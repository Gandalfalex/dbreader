package ui

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"dbreader/internal/sqlsafe"
	"github.com/Gandalfalex/vim-kit/cmdline"
	"github.com/Gandalfalex/vim-kit/keys"
	"github.com/Gandalfalex/vim-kit/motion"
)

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit // Run rolls back an open transaction
	}
	if m.pending != nil {
		return m.handlePendingKey(k)
	}
	switch m.mode {
	case inputCommand:
		return m.handleCommandKey(msg)
	case inputConfirm:
		return m.handleConfirmKey(msg)
	case inputFilter, inputSQL:
		return m.handleInput(msg)
	case inputFind, inputSearch:
		return m.handlePromptKey(msg)
	}
	m.setStatus("", false) // messages last until the next key
	if m.top() == scrScript && m.script.insert {
		return m.handleInsertKey(msg)
	}

	// esc cancels in-flight work first
	if k == "esc" && m.loading && m.cancel != nil {
		m.cancelOp()
		m.keys.Reset()
		m.setStatus("cancelled", false)
		return m, nil
	}

	km, actions := pagerKeymap, pagerActions
	switch {
	case m.list() != nil:
		km, actions = listKeymap, listActions
	case m.top() == scrData:
		km, actions = gridKeymap, gridActions
	case m.top() == scrScript && m.script.sel.Active():
		km, actions = visualKeymap, visualActions
	case m.top() == scrScript:
		km, actions = scriptKeymap, editActions
	}
	ev, ok := m.keys.Feed(km, k)
	if !ok {
		return m, nil // count digit or pending g/z
	}
	if ev.Action != actQuit {
		m.quitArmed = false
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
		defer l.scroll(h)
		return l.Apply(ev, len(l.vis), h)
	}
	if m.top() == scrData {
		g := &m.grid
		defer g.Scroll(len(g.vis), h)
		return g.Apply(ev, len(g.vis), h) || g.applyCol(ev)
	}
	if m.top() == scrScript {
		return m.script.apply(ev, m.h-3)
	}
	return motion.ApplyPager(pager{&m.vp}, ev)
}

func (m *model) handleInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	sql := m.mode == inputSQL
	switch msg.String() {
	case "esc", "ctrl+[":
		m.sqlHist.Close()
		if !sql {
			m.applyFilter(m.prevFilter)
		}
		m.mode = inputNone
		m.input.Blur()
		return m, nil
	case "enter":
		m.mode = inputNone
		m.input.Blur()
		if sql {
			m.sqlHist.Close()
			return m, m.runTyped(m.input.Value())
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
				m.input.SetValue(val)
				m.input.SetCursor(len([]rune(val)) - len([]rune(m.complTail)))
			}
			return m, nil
		case "up", "down":
			dir := 1
			if k == "up" {
				dir = -1
			}
			if val, ok := m.sqlHist.Recall(dir); ok {
				m.input.SetValue(val)
				m.input.CursorEnd()
				m.sqlHist.SetItems(nil)
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if sql {
		var items []cmdline.Item
		items, m.complTail = completeSQL(m.input.Value(), m.input.Position(), m.sqlCatalog())
		m.sqlHist.SetItems(items)
	} else {
		m.applyFilter(m.input.Value())
	}
	return m, cmd
}

// sqlCatalog is what the sql prompt completes from: the tables of the
// schema being browsed and their columns, once those have loaded.
func (m *model) sqlCatalog() sqlCatalog {
	cat := sqlCatalog{quote: m.conn.Quote}
	switch m.conn.Dialect() {
	case sqlsafe.Postgres:
		cat.fold = foldLower
	case sqlsafe.Oracle:
		cat.fold = foldUpper
	}
	for _, e := range m.tables.all {
		cat.tables = append(cat.tables, e.Name)
	}
	for _, e := range m.schemas.all {
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
		l.setFilter(f)
	} else if m.top() == scrData {
		m.grid.setFilter(f)
	}
}

// pager adapts the bubbles viewport to motion.Pager.
type pager struct{ *viewport.Model }

func (p pager) Top()    { p.GotoTop() }
func (p pager) Bottom() { p.GotoBottom() }
