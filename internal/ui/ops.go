package ui

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"dbreader/internal/db"
	"dbreader/internal/sqlsafe"

	tea "charm.land/bubbletea/v2"

	tk "github.com/Gandalfalex/tuikit"
)

const connectTimeout = 15 * time.Second

func (m *model) connect(i int) tea.Cmd {
	p := m.settings.Profiles[i]
	if p.Blocked != "" {
		m.SetStatus(p.Name+" cannot be opened: "+p.Blocked, true)
		return nil
	}
	ctx, gen := m.Ops.Start("connecting to " + p.Name)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		msg := connMsg{Op: gen, name: p.Name}
		if msg.conn, msg.err = db.Open(ctx, p); msg.err != nil {
			return msg
		}
		if msg.schemas, msg.err = msg.conn.Schemas(ctx); msg.err != nil {
			msg.conn.Close()
			msg.conn = nil
			return msg
		}
		msg.current = msg.conn.CurrentSchema(ctx)
		return msg
	}
}

func (m *model) loadTables(schema string) tea.Cmd {
	ctx, gen := m.Ops.Start("loading tables")
	conn := m.conn
	return func() tea.Msg {
		tables, err := conn.Tables(ctx, schema)
		return tablesMsg{Op: gen, schema: schema, tables: tables, err: err}
	}
}

// loadColNames fetches the schema's column names for sql completion. An
// external editor waiting for them gets its catalog file rewritten at once:
// dbreader handles no messages until the editor exits.
func (m *model) loadColNames(schema string) tea.Cmd {
	conn, edCatalog := m.conn, &m.edCatalog
	m.colsLoading = schema
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		names, err := conn.ColumnNames(ctx, schema)
		if err != nil {
			return colNamesMsg{conn: conn, schema: schema, err: err}
		}
		if ec := edCatalog.Load(); ec != nil && ec.schema == schema {
			c := ec.base
			c.withColumns(names)
			_ = c.write(ec.path) // gone if the editor has closed meanwhile
		}
		search, err := conn.SearchColumns(ctx, schema)
		return colNamesMsg{conn: conn, schema: schema, names: names, search: search, err: err}
	}
}

// browse loads a page of table data.
func (m *model) browse(b db.Browse) tea.Cmd {
	b.Limit = m.limit
	ctx, gen := m.Ops.Start("loading " + b.Table)
	conn := m.conn
	return func() tea.Msg {
		sql := conn.SelectSQL(b)
		res, err := conn.Query(ctx, sql, b.Limit)
		return dataMsg{Op: gen, res: res, sql: sql, browse: &b, err: err}
	}
}

// query runs a statement the classifier found to be read-only; the
// connection enforces that.
func (m *model) query(sql string) tea.Cmd {
	ctx, gen := m.Ops.Start("running query")
	conn, limit := m.conn, m.limit
	return func() tea.Msg {
		res, err := conn.Query(ctx, sql, limit)
		return dataMsg{Op: gen, res: res, sql: sql, err: err}
	}
}

func (m *model) loadStruct(table string) tea.Cmd {
	ctx, gen := m.Ops.Start("loading structure of " + table)
	conn, schema := m.conn, m.schema
	return func() tea.Msg {
		cols, err := conn.Columns(ctx, schema, table)
		return structMsg{Op: gen, table: table, cols: cols, err: err}
	}
}

// exec runs a confirmed write and leaves its transaction open.
func (m *model) exec(stmt sqlsafe.Stmt) tea.Cmd {
	ctx, gen := m.Ops.Start("executing " + stmt.Verb)
	conn := m.conn
	return func() tea.Msg {
		p, err := conn.Exec(ctx, stmt)
		return execMsg{Op: gen, pending: p, err: err}
	}
}

func finish(p *db.Pending, commit bool) tea.Cmd {
	return func() tea.Msg {
		msg := finishMsg{pending: p, committed: commit}
		if commit {
			msg.err = p.Commit()
		} else {
			msg.err = p.Rollback()
		}
		return msg
	}
}

// reload re-runs what the current screen shows.
func (m *model) reload() tea.Cmd {
	switch m.Stack.Top() {
	case scrTables:
		return m.loadTables(m.schema)
	case scrData:
		if m.grid.browse != nil {
			return m.browse(*m.grid.browse)
		}
		return m.query(m.grid.sql)
	case scrStruct:
		m.Stack.Pop()
		return m.loadStruct(m.structOf)
	case scrScript:
		if m.script.dirty {
			m.SetStatus("unsaved changes: :w saves, :e! drops them and re-reads the file", true)
			return nil
		}
		m.openScript(m.scriptFile)
	}
	return nil
}

// runTyped runs a statement from the prompt, the editor or :sql.
func (m *model) runTyped(text string) tea.Cmd {
	m.queue, m.queueN = nil, 0
	return m.runSQL(text)
}

// runSQL is the single entry point for statements the user typed. Reads run
// at once; anything else goes through the write guard.
func (m *model) runSQL(text string) tea.Cmd {
	if m.conn == nil {
		m.SetStatus("not connected: pick a connection first", true)
		return nil
	}
	d := m.conn.Dialect()
	stmts := sqlsafe.Split(text, d)
	switch len(stmts) {
	case 0:
		return nil
	case 1:
	default:
		m.SetStatus(strconv.Itoa(len(stmts))+" statements: run one at a time", true)
		return nil
	}
	m.lastSQL = stmts[0]
	m.sqlHist.Commit(stmts[0])
	st := sqlsafe.Classify(stmts[0], d)
	switch st.Kind {
	case sqlsafe.Read:
		return m.query(st.Text)
	case sqlsafe.TxControl:
		m.SetStatus(st.Verb+" refused: "+st.Reason+" (writes ask for commit or rollback)", true)
		return nil
	}
	if m.conn.Profile.ReadOnly {
		m.SetStatus(st.Verb+" refused: "+db.ErrReadOnly.Error(), true)
		return nil
	}
	return m.askConfirm(st)
}

// editSQL opens $EDITOR on the last statement and runs what was saved.
func (m *model) editSQL() tea.Cmd {
	if m.conn == nil {
		m.SetStatus("not connected: pick a connection first", true)
		return nil
	}
	f, err := os.CreateTemp("", "dbreader-*.sql")
	if err != nil {
		m.fail("edit", err)
		return nil
	}
	before := m.lastSQL
	_, _ = f.WriteString(before)
	f.Close()

	args := append(tk.EditorCommand(), f.Name())
	return tea.ExecProcess(exec.Command(args[0], args[1:]...), func(err error) tea.Msg {
		defer os.Remove(f.Name())
		if err != nil {
			return editorMsg{err: err}
		}
		data, err := os.ReadFile(f.Name())
		if err != nil {
			return editorMsg{err: err}
		}
		sql := strings.TrimSpace(string(data))
		if sql == strings.TrimSpace(before) {
			return tk.StatusMsg("unchanged, nothing run")
		}
		return editorMsg{sql: sql}
	})
}
