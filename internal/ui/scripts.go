package ui

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/Gandalfalex/vim-kit/motion"

	"dbreader/internal/config"
	"dbreader/internal/sqlsafe"
)

const maxScriptSize = 4 << 20

// connScripts are the scripts linked to the open connection, followed by
// the ones linked to none.
func (m *model) connScripts() (linked, loose []config.Script) {
	if m.conn == nil {
		return nil, nil
	}
	return m.conn.Profile.Scripts, m.settings.Scripts
}

// openScripts shows the script list on top of the schema or table list.
func (m *model) openScripts() tea.Cmd {
	if !m.needConn() {
		return nil
	}
	linked, loose := m.connScripts()
	// the filter searches the files' text too
	text := func(path string) string {
		if info, err := os.Stat(path); err != nil || info.Size() > maxScriptSize {
			return ""
		}
		data, _ := os.ReadFile(path)
		return string(data)
	}
	var rows []entry
	for _, s := range linked {
		rows = append(rows, entry{Key: s.Path, Name: s.Name, Cols: []string{m.conn.Profile.Name}, Text: text(s.Path)})
	}
	for _, s := range loose {
		rows = append(rows, entry{Key: s.Path, Name: s.Name, Cols: []string{"any connection"}, Dim: true, Text: text(s.Path)})
	}
	m.leaveData()
	if m.Stack.Top() == scrScript {
		m.Stack.Pop() // the list replaces the file; Update saves what was edited
	}
	if !m.Stack.PopTo(scrScripts) {
		m.scripts = newList()
		m.Stack.Push(scrScripts)
	}
	m.scripts.Set(rows)
	if len(rows) == 0 {
		m.SetStatus("no scripts yet: :new <name> starts one", false)
	}
	return nil
}

// leaveData unwinds pagers and the grid, so there is only ever one grid on
// the stack.
func (m *model) leaveData() {
	m.popPages()
	if m.Stack.Top() == scrData {
		m.Stack.Pop()
	}
}

func (m *model) findScript(name string) (config.Script, bool) {
	linked, loose := m.connScripts()
	for _, s := range append(append([]config.Script(nil), linked...), loose...) {
		if s.Name == name || s.Path == name {
			return s, true
		}
	}
	return config.Script{}, false
}

// openScript shows a script file as text. Re-reading the file on screen
// keeps the cursor where it was.
func (m *model) openScript(s config.Script) {
	info, err := os.Stat(s.Path)
	if err == nil && info.Size() > maxScriptSize {
		err = fmt.Errorf("%s is larger than %d MiB", s.Path, maxScriptSize>>20)
	}
	var data []byte
	if err == nil {
		data, err = os.ReadFile(s.Path)
	}
	if err != nil {
		m.fail("open script", err)
		return
	}
	view := newFileView(string(data), m.conn.Dialect())
	view.mtime = info.ModTime()
	m.leaveData()
	if m.scriptFile.Path != s.Path {
		m.keepScript() // the buffer is about to be replaced
	}
	if m.Stack.Top() == scrScript && m.scriptFile.Path == s.Path {
		view.Cursor, view.Top = m.script.Cursor, m.script.Top
		view.Clamp(len(view.lines))
		view.col = motion.ClampCol(view.line(), m.script.col)
		view.reg, view.regLine = m.script.reg, m.script.regLine
	} else {
		m.Stack.PopTo(scrScripts)
		if m.Stack.Top() == scrScript {
			m.Stack.Pop()
		}
		m.Stack.Push(scrScript)
	}
	m.scriptFile, m.script = s, view
	m.SetStatus(fmt.Sprintf("%d lines, %d statements", len(view.lines), len(view.spans)), false)
}

// runScript executes what :run points at in the script on screen: the
// visual selection, or the statement under the cursor.
func (m *model) runScript() tea.Cmd {
	if m.Stack.Top() != scrScript {
		m.SetStatus(":run works in a script (S lists them); use s or :sql to type a statement", true)
		return nil
	}
	text, _ := m.script.target()
	m.script.sel.Clear()
	stmts := sqlsafe.Split(text, m.conn.Dialect())
	if len(stmts) == 0 {
		m.SetStatus("nothing to run here", true)
		return nil
	}
	m.queue, m.queueN = stmts, len(stmts)
	return m.runNext()
}

// runNext starts the next statement of a :run. Each goes through runSQL, so
// every write is confirmed and committed on its own; anything refused,
// cancelled or failed drops the statements after it.
func (m *model) runNext() tea.Cmd {
	if len(m.queue) == 0 {
		return nil
	}
	stmt := m.queue[0]
	m.queue = m.queue[1:]
	cmd := m.runSQL(stmt)
	if cmd == nil && m.confirm == nil {
		m.queue = nil
	}
	return cmd
}

// queuePos describes progress through a multi-statement :run, "" otherwise.
func (m *model) queuePos() string {
	if m.queueN < 2 {
		return ""
	}
	return fmt.Sprintf("statement %d of %d", m.queueN-len(m.queue), m.queueN)
}
