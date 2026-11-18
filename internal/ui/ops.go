package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"dbreader/internal/db"
	"dbreader/internal/sqlsafe"
)

const connectTimeout = 15 * time.Second

func (m *model) connect(i int) tea.Cmd {
	p := m.settings.Profiles[i]
	if p.Blocked != "" {
		m.setStatus(p.Name+" cannot be opened: "+p.Blocked, true)
		return nil
	}
	ctx := m.newOp("connecting to " + p.Name)
	gen := op(m.gen)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		msg := connMsg{op: gen, name: p.Name}
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
	ctx := m.newOp("loading tables")
	gen, conn := op(m.gen), m.conn
	return func() tea.Msg {
		tables, err := conn.Tables(ctx, schema)
		return tablesMsg{op: gen, schema: schema, tables: tables, err: err}
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
	ctx := m.newOp("loading " + b.Table)
	gen, conn := op(m.gen), m.conn
	return func() tea.Msg {
		sql := conn.SelectSQL(b)
		res, err := conn.Query(ctx, sql, b.Limit)
		return dataMsg{op: gen, res: res, sql: sql, browse: &b, err: err}
	}
}

// query runs a statement the classifier found to be read-only; the
// connection enforces that.
func (m *model) query(sql string) tea.Cmd {
	ctx := m.newOp("running query")
	gen, conn, limit := op(m.gen), m.conn, m.limit
	return func() tea.Msg {
		res, err := conn.Query(ctx, sql, limit)
		return dataMsg{op: gen, res: res, sql: sql, err: err}
	}
}

func (m *model) loadStruct(table string) tea.Cmd {
	ctx := m.newOp("loading structure of " + table)
	gen, conn, schema := op(m.gen), m.conn, m.schema
	return func() tea.Msg {
		cols, err := conn.Columns(ctx, schema, table)
		return structMsg{op: gen, table: table, cols: cols, err: err}
	}
}

// exec runs a confirmed write and leaves its transaction open.
func (m *model) exec(stmt sqlsafe.Stmt) tea.Cmd {
	ctx := m.newOp("executing " + stmt.Verb)
	gen, conn := op(m.gen), m.conn
	return func() tea.Msg {
		p, err := conn.Exec(ctx, stmt)
		return execMsg{op: gen, pending: p, err: err}
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
	switch m.top() {
	case scrTables:
		return m.loadTables(m.schema)
	case scrData:
		if m.grid.browse != nil {
			return m.browse(*m.grid.browse)
		}
		return m.query(m.grid.sql)
	case scrStruct:
		m.pop()
		return m.loadStruct(m.structOf)
	case scrScript:
		if m.script.dirty {
			m.setStatus("unsaved changes: :w saves, :e! drops them and re-reads the file", true)
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
		m.setStatus("not connected: pick a connection first", true)
		return nil
	}
	d := m.conn.Dialect()
	stmts := sqlsafe.Split(text, d)
	switch len(stmts) {
	case 0:
		return nil
	case 1:
	default:
		m.setStatus(strconv.Itoa(len(stmts))+" statements: run one at a time", true)
		return nil
	}
	m.lastSQL = stmts[0]
	m.sqlHist.Commit(stmts[0])
	st := sqlsafe.Classify(stmts[0], d)
	switch st.Kind {
	case sqlsafe.Read:
		return m.query(st.Text)
	case sqlsafe.TxControl:
		m.setStatus(st.Verb+" refused: "+st.Reason+" (writes ask for commit or rollback)", true)
		return nil
	}
	if m.conn.Profile.ReadOnly {
		m.setStatus(st.Verb+" refused: "+db.ErrReadOnly.Error(), true)
		return nil
	}
	return m.askConfirm(st)
}

// editSQL opens $EDITOR on the last statement and runs what was saved.
func (m *model) editSQL() tea.Cmd {
	if m.conn == nil {
		m.setStatus("not connected: pick a connection first", true)
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

	args := append(editorCommand(), f.Name())
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
			return statusMsg("unchanged, nothing run")
		}
		return editorMsg{sql: sql}
	})
}

func yank(s, what string) tea.Cmd {
	cmds := []tea.Cmd{tea.SetClipboard(s)}
	if runtime.GOOS == "darwin" {
		cmds = append(cmds, func() tea.Msg {
			c := exec.Command("pbcopy")
			c.Stdin = strings.NewReader(s)
			_ = c.Run()
			return nil
		})
	}
	cmds = append(cmds, func() tea.Msg { return statusMsg(fmt.Sprintf("yanked %s (%d chars)", what, len([]rune(s)))) })
	return tea.Batch(cmds...)
}
