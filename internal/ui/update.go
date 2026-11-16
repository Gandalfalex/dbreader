package ui

import (
	"fmt"
	"log/slog"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"dbreader/internal/config"
	"dbreader/internal/db"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if r, ok := msg.(opResult); ok && r.opGen() != m.gen {
		return m, discard(msg) // result of a superseded or cancelled operation
	}
	editing := m.script.dirty && m.onStack(scrScript)
	cmd := m.update(msg)
	if editing && !m.onStack(scrScript) {
		m.keepScript() // whatever closed the script, its text is not lost
	}
	if m.loading && !m.spinning {
		m.spinning = true
		cmd = tea.Batch(cmd, m.spin.Tick)
	}
	return m, cmd
}

// discard releases what a dropped result holds: a write nobody will get to
// confirm is rolled back, an unused connection closed.
func discard(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case execMsg:
		if p := msg.pending; p != nil && p.Open() {
			slog.Warn("write finished after it was cancelled, rolling back", "sql", p.Stmt.Text)
			return func() tea.Msg { _ = p.Rollback(); return nil }
		}
	case connMsg:
		if c := msg.conn; c != nil {
			return func() tea.Msg { _ = c.Close(); return nil }
		}
	}
	return nil
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.SetWidth(max(10, m.w-16))
		m.vp.SetWidth(m.w)
		m.vp.SetHeight(max(1, m.h-3))
		m.refreshViewport()
		return nil

	case spinner.TickMsg:
		if !m.loading {
			m.spinning = false
			return nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return cmd

	case statusMsg:
		m.setStatus(string(msg), false)
		return nil

	case connMsg:
		m.done()
		if msg.err != nil {
			m.fail("connect "+msg.name, msg.err)
			return nil
		}
		return m.connected(msg)

	case tablesMsg:
		m.done()
		if msg.err != nil {
			m.fail("list tables", msg.err)
			return nil
		}
		m.showTables(msg.schema, msg.tables)
		return m.loadColNames(msg.schema)

	case colNamesMsg:
		if msg.conn != m.conn {
			return nil
		}
		if msg.err != nil {
			slog.Warn("column names for completion", "schema", msg.schema, "err", msg.err)
			return nil
		}
		m.colNames, m.colSchema = msg.names, msg.schema
		if msg.schema == m.schema { // let the table filter find columns too
			for i := range m.tables.all {
				e := &m.tables.all[i]
				e.Text = strings.Join(msg.names[e.Key], " ")
			}
			sel, _ := m.tables.current()
			m.tables.refilter()
			m.tables.selectKey(sel.Key)
		}
		return nil

	case grepMsg:
		return m.grepResult(msg)

	case dataMsg:
		m.done()
		if msg.err != nil {
			m.fail("query", msg.err)
			return nil
		}
		m.showData(msg)
		return m.runNext()

	case structMsg:
		m.done()
		if msg.err != nil {
			m.fail("structure of "+msg.table, msg.err)
			return nil
		}
		m.structOf, m.cols = msg.table, msg.cols
		m.openPage(scrStruct, false)
		return nil

	case execMsg:
		m.done()
		if msg.err != nil {
			m.fail("execute", msg.err)
			return nil
		}
		if p := msg.pending; p.Open() {
			m.pending = p
			return nil
		}
		return m.finished(msg.pending, true)

	case finishMsg:
		m.done()
		m.pending = nil
		if msg.err != nil {
			what := "rollback"
			if msg.committed {
				what = "commit"
			}
			m.fail(what, msg.err)
			return nil
		}
		return m.finished(msg.pending, msg.committed)

	case editorMsg:
		if msg.err != nil {
			m.fail("editor", msg.err)
			return nil
		}
		return m.runTyped(msg.sql)

	case tea.KeyPressMsg:
		_, cmd := m.handleKey(msg)
		return cmd

	case tea.PasteMsg:
		if m.mode == inputNone && m.top() == scrScript && m.script.insert && !m.guarding() {
			m.script.insertText(msg.Content)
			m.edCompl.SetItems(nil)
			return nil
		}
	}

	if m.mode != inputNone {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
	return nil
}

func (m *model) connected(msg connMsg) tea.Cmd {
	if m.conn != nil {
		_ = m.conn.Close()
	}
	p := msg.conn.Profile
	slog.Info("connected", "connection", p.Name, "driver", p.Driver, "target", p.Target())
	m.conn, m.schema = msg.conn, ""
	if m.limit = p.Limit; m.limit <= 0 {
		m.limit = config.DefaultLimit
	}
	m.stack = m.stack[:1]
	m.tables, m.grid, m.colNames = listState{}, grid{}, nil

	var rows []entry
	for _, s := range msg.schemas {
		rows = append(rows, entry{Key: s, Name: s, Dim: systemSchemas[s]})
	}
	m.schemas = listState{}
	m.schemas.set(rows)
	if !msg.conn.HasSchemas() {
		return m.loadTables("")
	}
	m.push(scrSchemas)
	if p.Schema != "" {
		m.schemas.selectKey(p.Schema)
		return m.loadTables(p.Schema)
	}
	m.schemas.selectKey(msg.current)
	m.schemas.scroll(m.listHeight())
	m.setStatus(fmt.Sprintf("%d schemas", len(rows)), false)
	return nil
}

var systemSchemas = map[string]bool{
	"pg_catalog": true, "information_schema": true,
	"mysql": true, "performance_schema": true, "sys": true,
}

func (m *model) showTables(schema string, tables []db.Table) {
	rows := make([]entry, len(tables))
	for i, t := range tables {
		est := ""
		if t.Rows >= 0 {
			est = "~" + fmtInt(t.Rows)
		}
		rows[i] = entry{Key: t.Name, Name: t.Name, Cols: []string{t.Kind, est}, Dim: t.Kind != "table"}
	}
	sel := ""
	if e, ok := m.tables.current(); ok && schema == m.schema {
		sel = e.Key // reload: stay on the same table
	}
	filter := m.tables.filter
	m.schema = schema
	m.tables = listState{}
	m.tables.set(rows)
	if !m.popTo(scrTables) {
		m.popTo(scrSchemas)
		m.stack = m.stack[:min(len(m.stack), 2)]
		m.push(scrTables)
	} else if sel != "" {
		m.tables.setFilter(filter)
		m.tables.selectKey(sel)
	}
	m.setStatus(fmt.Sprintf("%d tables", len(rows)), false)
}

// showData puts a result on the grid screen, replacing the one shown. A
// reload of the same statement keeps the cursor and filter.
func (m *model) showData(msg dataMsg) {
	old := m.grid
	m.grid = newGrid(msg.res, msg.sql, msg.browse)
	g := &m.grid
	if old.res != nil && sameView(old, *g) {
		g.filter, g.sortCol, g.sortDesc = old.filter, old.sortCol, old.sortDesc
		if g.sortCol >= len(g.widths) {
			g.sortCol = -1
		}
		g.sorted()
		g.Cursor, g.Top = old.Cursor, old.Top
		g.col, g.left = min(old.col, max(0, len(g.widths)-1)), old.left
		g.Clamp(len(g.vis))
	}
	// replace the grid on screen; from a script, put the result on top of it
	if m.popPages(); m.top() != scrData {
		m.push(scrData)
	}
	n := fmtInt(int64(len(msg.res.Rows)))
	if msg.res.Truncated {
		n += "+"
	}
	m.setStatus(fmt.Sprintf("%s rows in %s", n, fmtDur(msg.res.Took)), false)
}

// sameView reports whether b shows the same table or query as a, possibly
// re-sorted, so the cursor position still means something.
func sameView(a, b grid) bool {
	if (a.browse == nil) != (b.browse == nil) {
		return false
	}
	if a.browse != nil {
		return a.browse.Schema == b.browse.Schema && a.browse.Table == b.browse.Table
	}
	return a.sql == b.sql
}

// finished reports a completed write and refreshes the data on screen.
func (m *model) finished(p *db.Pending, committed bool) tea.Cmd {
	if !committed {
		slog.Info("rolled back", "sql", p.Stmt.Text)
		m.queue = nil
		m.setStatus("rolled back, nothing changed", false)
		return nil
	}
	slog.Info("committed", "sql", p.Stmt.Text, "affected", p.Affected)
	note := "committed"
	if p.Stmt.NoTx {
		note = "executed"
	}
	status := fmt.Sprintf("%s: %s, %s rows affected", note, p.Stmt.Verb, fmtInt(p.Affected))
	if len(m.queue) > 0 {
		m.setStatus(status, false)
		return m.runNext()
	}
	var cmd tea.Cmd
	if m.top() == scrData || m.top() == scrTables {
		cmd = m.reload()
	}
	m.setStatus(status, false)
	if cmd != nil {
		// the reload's own status would hide the outcome of the write
		return tea.Sequence(cmd, func() tea.Msg { return statusMsg(status) })
	}
	return nil
}
