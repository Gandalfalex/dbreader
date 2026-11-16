package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Gandalfalex/vim-kit/keys"
	"github.com/Gandalfalex/vim-kit/motion"
)

type keyFn func(m *model, ev keys.Event) (tea.Model, tea.Cmd)

// do adapts an action that never returns a command.
func do(fn func(m *model, ev keys.Event)) keyFn {
	return func(m *model, ev keys.Event) (tea.Model, tea.Cmd) {
		fn(m, ev)
		return m, nil
	}
}

// cmd adapts methods that only produce a command, like (*model).reload.
func cmd(fn func(*model) tea.Cmd) keyFn {
	return func(m *model, _ keys.Event) (tea.Model, tea.Cmd) { return m, fn(m) }
}

// App actions; motions come from vimkit/motion.
const (
	actQuit       keys.Action = "quit"
	actErrors     keys.Action = "errors"
	actFilter     keys.Action = "filter"
	actEsc        keys.Action = "clear_or_back"
	actOpen       keys.Action = "open"
	actBack       keys.Action = "back"
	actInfo       keys.Action = "info"
	actReload     keys.Action = "reload"
	actYank       keys.Action = "yank"
	actYankRow    keys.Action = "yank_row"
	actSort       keys.Action = "sort"
	actSQL        keys.Action = "sql"
	actEdit       keys.Action = "edit_sql"
	actScripts    keys.Action = "scripts"
	actVisual     keys.Action = "visual"
	actVisualLine keys.Action = "visual_line"
)

// common are the actions every screen shares.
var common = keys.Bindings{
	actErrors:  {"E"},
	actSQL:     {"s"},
	actEdit:    {"e"},
	actScripts: {"S"},
	actReload:  {"r", "ctrl+r"},
	actYank:    {"y"},
}

var listKeymap = keys.MustKeymap(motion.VerticalBindings(), common, keys.Bindings{
	actQuit:   {"q"},
	actFilter: {"/"},
	actEsc:    {"esc"},
	actOpen:   {"enter", "l", "right"},
	actBack:   {"h", "left", "backspace", "-"},
	actInfo:   {"i", "gi"},
	actSearch: {"?"},
})

// In the grid h and l move between columns, so only backspace, - and q go back.
var gridKeymap = keys.MustKeymap(motion.VerticalBindings(), columnMotions, common, keys.Bindings{
	actFilter:     {"/"},
	actEsc:        {"esc"},
	actOpen:       {"enter"},
	actBack:       {"q", "backspace", "-"},
	actInfo:       {"i", "gi"},
	actSort:       {"o"},
	actYankRow:    {"Y"},
	actSearch:     {"?"},
	actVisualLine: {"V"},
})

// A script is edited like a file in vim: every in-line motion works and
// the usual keys insert, delete, yank and paste. r is not bound, so a
// reload cannot discard edits by accident (:e! does that on purpose).
var scriptKeymap = keys.MustKeymap(motion.VerticalBindings(), motion.HorizontalBindings(), editBindings,
	common.Only(actErrors, actSQL, actScripts), keys.Bindings{
		actEsc:        {"esc"},
		actBack:       {"q", "-"},
		actVisual:     {"v"},
		actVisualLine: {"V"},
		actFilter:     {"/"},
		actNext:       {"n"},
		actPrev:       {"N"},
	})

// With a selection, y d c act on it and :run executes it.
var visualKeymap = keys.MustKeymap(motion.VerticalBindings(), motion.HorizontalBindings(), keys.Bindings{
	actEsc:        {"esc"},
	actVisual:     {"v"},
	actVisualLine: {"V"},
	actYank:       {"y"},
	actDelete:     {"d", "x"},
	actChange:     {"c"},
	actFilter:     {"/"},
	actNext:       {"n"},
	actPrev:       {"N"},
})

var pagerKeymap = keys.MustKeymap(pagerMotions(), common, keys.Bindings{
	actBack: {"q", "esc", "h", "left", "backspace", "-"},
})

// columnMotions are the in-line motions that make sense across columns;
// e stays free for the editor.
var columnMotions = motion.HorizontalBindings().Only(
	motion.Left, motion.Right, motion.WordForward, motion.WordBackward, motion.LineStart, motion.LineEnd)

// pagerMotions adds less-style keys to the vim defaults.
func pagerMotions() keys.Bindings {
	b := motion.VerticalBindings()
	b[motion.Down] = append(b[motion.Down], "enter")
	b[motion.PageDown] = append(b[motion.PageDown], "space")
	return b
}

// quitList quits at the top level; deeper in q must be pressed twice so a
// stray q does not drop the connection.
func quitList(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
	if m.top() == scrConns || m.quitArmed {
		return m, tea.Quit
	}
	m.quitArmed = true
	m.setStatus("press q again to quit (h goes back)", false)
	return m, nil
}

func (m *model) openFilter(current string) (tea.Model, tea.Cmd) {
	m.mode = inputFilter
	m.prevFilter = current
	m.input.Prompt = "/"
	m.input.Placeholder = "filter (smart-case; matches any column in a result)"
	m.input.SetValue(current)
	m.input.CursorEnd()
	return m, m.input.Focus()
}

func (m *model) openSQL() tea.Cmd {
	if m.conn == nil {
		m.setStatus("not connected: pick a connection first", true)
		return nil
	}
	m.mode = inputSQL
	m.input.Prompt = "sql> "
	m.input.Placeholder = "one statement (tab completes tables and columns · ↑↓ history · writes ask before running)"
	m.input.SetValue("")
	m.sqlHist.Open(nil)
	m.complTail = ""
	return m.input.Focus()
}

// shared are the handlers for the common bindings.
var shared = map[keys.Action]keyFn{
	actErrors: do(func(m *model, _ keys.Event) {
		if m.top() != scrErrors {
			m.openPage(scrErrors, true)
		}
	}),
	actSQL:     cmd((*model).openSQL),
	actEdit:    cmd((*model).editSQL),
	actScripts: cmd((*model).openScripts),
	actReload:  cmd((*model).reload),
	actYank:    cmd((*model).cmdYank),
}

var listActions = map[keys.Action]keyFn{
	actQuit: quitList,
	actFilter: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		return m.openFilter(m.list().filter)
	},
	actEsc: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		if l := m.list(); l.filter != "" {
			l.setFilter("")
			return m, nil
		}
		return m, m.back()
	},
	actOpen:   cmd((*model).open),
	actBack:   cmd((*model).back),
	actInfo:   cmd((*model).openStruct),
	actSearch: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) { return m.openSearch() },
}

var gridActions = map[keys.Action]keyFn{
	actFilter: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		return m.openFilter(m.grid.filter)
	},
	actSearch: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) { return m.openSearch() },
	actVisualLine: do(func(m *model, _ keys.Event) {
		g := &m.grid
		g.selecting, g.selAnchor = !g.selecting, g.Cursor
	}),
	actEsc: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		if m.grid.selecting {
			m.grid.selecting = false
			return m, nil
		}
		if m.grid.filter != "" {
			m.grid.setFilter("")
			return m, nil
		}
		return m, m.back()
	},
	actOpen: do(func(m *model, _ keys.Event) {
		if _, ok := m.grid.row(); ok {
			m.openPage(scrRow, false)
		}
	}),
	actBack: cmd((*model).back),
	actInfo: cmd((*model).openStruct),
	actSort: cmd((*model).sortColumn),
	actYankRow: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		row, ok := m.grid.row()
		if !ok {
			return m, nil
		}
		vals := make([]string, len(row))
		for i, c := range row {
			vals[i] = c.S
		}
		return m, yank(strings.Join(vals, "\t"), "row")
	},
}

var pagerActions = map[keys.Action]keyFn{
	actBack: do(func(m *model, _ keys.Event) { m.pop() }),
}
