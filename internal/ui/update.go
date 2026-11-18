package ui

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"dbreader/internal/config"
	"dbreader/internal/db"

	tea "charm.land/bubbletea/v2"

	tk "github.com/Gandalfalex/tuikit"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.Ops.Stale(msg) {
		return m, discard(msg) // result of a superseded or cancelled operation
	}
	editing := m.script.dirty && m.Stack.Has(scrScript)
	cmd := m.update(msg)
	if editing && !m.Stack.Has(scrScript) {
		m.keepScript() // whatever closed the script, its text is not lost
	}
	return m, tea.Batch(cmd, m.Ops.Tick())
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
	if cmd, ok := m.Shell.Update(msg); ok {
		return cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.prompt.Input.SetWidth(max(10, m.W-16))
		m.vp.Resize(m.W, m.H)
		m.refreshViewport()
		return nil

	case connMsg:
		m.Ops.Done()
		if msg.err != nil {
			m.fail("connect "+msg.name, msg.err)
			return nil
		}
		return m.connected(msg)

	case tablesMsg:
		m.Ops.Done()
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
		if m.colsLoading == msg.schema {
			m.colsLoading = ""
		}
		if msg.err != nil {
			slog.Warn("column names for completion", "schema", msg.schema, "err", msg.err)
			return nil
		}
		m.colNames, m.searchCols, m.colSchema = msg.names, msg.search, msg.schema
		if msg.schema == m.schema { // let the table filter find columns too
			for i := range m.tables.All {
				e := &m.tables.All[i]
				e.Text = strings.Join(msg.names[e.Key], " ")
			}
			sel, _ := m.tables.Current()
			m.tables.Refilter()
			m.tables.Select(sel.Key)
		}
		return nil

	case grepMsg:
		return m.grepResult(msg)

	case dataMsg:
		m.Ops.Done()
		if msg.err != nil {
			m.fail("query", msg.err)
			return nil
		}
		m.showData(msg)
		return m.runNext()

	case structMsg:
		m.Ops.Done()
		if msg.err != nil {
			m.fail("structure of "+msg.table, msg.err)
			return nil
		}
		m.structOf, m.cols = msg.table, msg.cols
		m.openPage(scrStruct, false)
		return nil

	case execMsg:
		m.Ops.Done()
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
		m.Ops.Done()
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

	case tk.EditedMsg:
		m.scriptEdited(msg)
		return nil

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
		if m.prompt.Kind == inputNone && m.Stack.Top() == scrScript && m.script.insert && !m.guarding() {
			m.script.insertText(msg.Content)
			m.edCompl.SetItems(nil)
			return nil
		}
	}

	if m.prompt.Kind != inputNone {
		var cmd tea.Cmd
		m.prompt.Input, cmd = m.prompt.Input.Update(msg)
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
	m.Stack.Reset()
	m.tables, m.grid, m.colNames, m.searchCols, m.colsLoading = newList(), grid{}, nil, nil, ""

	m.schemas = newList()
	m.schemaInfo, m.currentSchema = msg.schemas, msg.current
	m.showSchemas()
	if !msg.conn.HasSchemas() {
		return m.loadTables("")
	}
	m.Stack.Push(scrSchemas)
	if p.Schema != "" {
		m.schemas.Select(p.Schema)
		return m.loadTables(p.Schema)
	}
	m.schemas.Select(msg.current)
	m.schemas.Scroll(m.listHeight())
	m.SetStatus(fmt.Sprintf("%d schemas%s", len(m.schemas.All), m.hiddenNote()), false)
	return nil
}

// showSchemas lists the schemas: pinned ones first, then the rest in server
// order. System and empty schemas are hidden unless pinned, opened by the
// profile or the one unqualified names resolve to.
func (m *model) showSchemas() {
	conn := m.conn.Profile.Name
	rows := make([]entry, len(m.schemaInfo))
	for i, s := range m.schemaInfo {
		pin := ""
		if m.state.IsPinned(conn, s.Name) {
			pin = "pinned"
		}
		boring := s.System || s.Objects == 0
		rows[i] = entry{Key: s.Name, Name: s.Name, Cols: []string{tk.FmtInt(int64(s.Objects)), pin}, Dim: boring,
			Hidden: boring && pin == "" && s.Name != m.currentSchema && s.Name != m.conn.Profile.Schema}
	}
	slices.SortStableFunc(rows, func(a, b entry) int { return strings.Compare(b.Cols[1], a.Cols[1]) })
	sel, _ := m.schemas.Current()
	m.schemas.Set(rows)
	if sel.Key != "" {
		m.schemas.Select(sel.Key)
	}
}

// togglePin pins or unpins the schema under the cursor.
func (m *model) togglePin() {
	if m.Stack.Top() != scrSchemas {
		m.SetStatus("* pins schemas: use it in the schema list", true)
		return
	}
	e, ok := m.schemas.Current()
	if !ok {
		return
	}
	pinned, err := m.state.TogglePin(m.conn.Profile.Name, e.Key)
	m.showSchemas()
	m.schemas.Scroll(m.listHeight())
	if err != nil {
		m.fail("save pin", err)
		return
	}
	if pinned {
		m.SetStatus("pinned "+e.Key+": listed first, never hidden", false)
	} else {
		m.SetStatus("unpinned "+e.Key, false)
	}
}

// toggleHidden shows or hides the system and empty schemas.
func (m *model) toggleHidden() {
	if m.Stack.Top() != scrSchemas {
		m.SetStatus("zh shows hidden schemas: use it in the schema list", true)
		return
	}
	l := &m.schemas
	if l.HiddenCount() == 0 {
		m.SetStatus("nothing hidden here", false)
		return
	}
	sel, _ := l.Current()
	l.ShowHidden = !l.ShowHidden
	l.Refilter()
	l.Select(sel.Key)
	l.Scroll(m.listHeight())
	m.SetStatus(strings.TrimPrefix(m.hiddenNote(), ", "), false)
}

// hiddenNote tells how many schemas zh would show or hide, "" for none.
func (m *model) hiddenNote() string {
	switch n := m.schemas.HiddenCount(); {
	case n == 0:
		return ""
	case m.schemas.ShowHidden:
		return fmt.Sprintf(", showing %d system and empty ones (zh hides them)", n)
	default:
		return fmt.Sprintf(", %d system or empty ones hidden (zh shows them)", n)
	}
}

func (m *model) showTables(schema string, tables []db.Table) {
	rows := make([]entry, len(tables))
	for i, t := range tables {
		est := ""
		if t.Rows >= 0 {
			est = "~" + tk.FmtInt(t.Rows)
		}
		rows[i] = entry{Key: t.Name, Name: t.Name, Cols: []string{t.Kind, est}, Dim: t.Kind != "table"}
	}
	sel := ""
	if e, ok := m.tables.Current(); ok && schema == m.schema {
		sel = e.Key // reload: stay on the same table
	}
	filter := m.tables.Filter
	m.schema = schema
	m.tables = newList()
	m.tables.Set(rows)
	if !m.Stack.PopTo(scrTables) {
		m.Stack.PopTo(scrSchemas)
		m.Stack.Keep(2)
		m.Stack.Push(scrTables)
	} else if sel != "" {
		m.tables.SetFilter(filter)
		m.tables.Select(sel)
	}
	m.SetStatus(fmt.Sprintf("%d tables", len(rows)), false)
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
	if m.popPages(); m.Stack.Top() != scrData {
		m.Stack.Push(scrData)
	}
	n := tk.FmtInt(int64(len(msg.res.Rows)))
	if msg.res.Truncated {
		n += "+"
	}
	m.SetStatus(fmt.Sprintf("%s rows in %s", n, tk.FmtDur(msg.res.Took)), false)
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
		m.SetStatus("rolled back, nothing changed", false)
		return nil
	}
	slog.Info("committed", "sql", p.Stmt.Text, "affected", p.Affected)
	note := "committed"
	if p.Stmt.NoTx {
		note = "executed"
	}
	status := fmt.Sprintf("%s: %s, %s rows affected", note, p.Stmt.Verb, tk.FmtInt(p.Affected))
	if len(m.queue) > 0 {
		m.SetStatus(status, false)
		return m.runNext()
	}
	var cmd tea.Cmd
	if m.Stack.Top() == scrData || m.Stack.Top() == scrTables {
		cmd = m.reload()
	}
	m.SetStatus(status, false)
	if cmd != nil {
		// the reload's own status would hide the outcome of the write
		return tea.Sequence(cmd, func() tea.Msg { return tk.StatusMsg(status) })
	}
	return nil
}
