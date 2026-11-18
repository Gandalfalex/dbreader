package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	tk "github.com/Gandalfalex/tuikit"
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
	actPin        keys.Action = "pin"
	actHidden     keys.Action = "toggle_hidden"
	actRun        keys.Action = "run"
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
	actPin:    {"*"},
	actHidden: {"zh"},
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

// A script is edited like a file in vim: the in-line motions work and the
// usual keys insert, delete, yank and paste. e opens the file in $EDITOR
// instead of moving to the word end; enter runs the statement under the
// cursor. r is not bound, so a reload cannot discard edits by accident (:e!
// does that on purpose).
var scriptKeymap = keys.MustKeymap(motion.VerticalBindings(), scriptMotions(), editBindings,
	common.Only(actErrors, actSQL, actScripts, actEdit), keys.Bindings{
		actRun:        {"enter"},
		actEsc:        {"esc"},
		actBack:       {"q", "-"},
		actVisual:     {"v"},
		actVisualLine: {"V"},
		actFilter:     {"/"},
		actNext:       {"n"},
		actPrev:       {"N"},
	})

// With a selection, y d c act on it and enter or :run executes it.
var visualKeymap = keys.MustKeymap(motion.VerticalBindings(), motion.HorizontalBindings(), keys.Bindings{
	actRun:        {"enter"},
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

var pagerKeymap = keys.MustKeymap(tk.PagerMotions(), common, keys.Bindings{
	actBack: {"q", "esc", "h", "left", "backspace", "-"},
})

// columnMotions are the in-line motions that make sense across columns;
// e stays free for the editor.
var columnMotions = motion.HorizontalBindings().Only(
	motion.Left, motion.Right, motion.WordForward, motion.WordBackward, motion.LineStart, motion.LineEnd)

func scriptMotions() keys.Bindings {
	b := motion.HorizontalBindings()
	delete(b, motion.WordEnd)
	return b
}

// quitList quits at the top level; deeper in q must be pressed twice so a
// stray q does not drop the connection.
func quitList(m *model, _ keys.Event) (tea.Model, tea.Cmd) { return m, m.Quit(false) }

func (m *model) openFilter(current string) (tea.Model, tea.Cmd) {
	m.prevFilter = current
	return m, m.prompt.Open(inputFilter, tk.ModeFilter, "/", "filter (smart-case; matches any column in a result)", current)
}

func (m *model) openSQL() tea.Cmd {
	if m.conn == nil {
		m.SetStatus("not connected: pick a connection first", true)
		return nil
	}
	m.sqlHist.Open(nil)
	m.complTail = ""
	return m.prompt.Open(inputSQL, modeSQL, "sql> ", "one statement (tab completes tables and columns · ↑↓ history · writes ask before running)", "")
}

// shared are the handlers for the common bindings.
var shared = map[keys.Action]keyFn{
	actErrors: do(func(m *model, _ keys.Event) {
		if m.Stack.Top() != scrErrors {
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
		return m.openFilter(m.list().Filter)
	},
	actEsc: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		if l := m.list(); l.Filter != "" {
			l.SetFilter("")
			return m, nil
		}
		return m, m.back()
	},
	actOpen:   cmd((*model).open),
	actBack:   cmd((*model).back),
	actInfo:   cmd((*model).openStruct),
	actSearch: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) { return m.openSearch() },
	actPin:    do(func(m *model, _ keys.Event) { m.togglePin() }),
	// in the script list e opens the script under the cursor in $EDITOR
	actEdit: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		if m.Stack.Top() != scrScripts {
			return m, m.editSQL()
		}
		if m.open(); m.Stack.Top() != scrScript {
			return m, nil
		}
		return m, m.editScript()
	},
	actHidden: do(func(m *model, _ keys.Event) { m.toggleHidden() }),
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
		return m, tk.Yank(strings.Join(vals, "\t"), "row")
	},
}

var pagerActions = map[keys.Action]keyFn{
	actBack: do(func(m *model, _ keys.Event) { m.Stack.Pop() }),
}
