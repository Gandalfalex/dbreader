package ui

import (
	"encoding/json"
	"strconv"
	"strings"

	"dbreader/internal/db"

	tea "charm.land/bubbletea/v2"

	tk "github.com/Gandalfalex/tuikit"
)

func (m *model) open() tea.Cmd {
	e, ok := m.list().Current()
	if !ok {
		return nil
	}
	switch m.Stack.Top() {
	case scrConns:
		i, _ := strconv.Atoi(e.Key)
		return m.connect(i)
	case scrSchemas:
		return m.loadTables(e.Key)
	case scrTables:
		return m.browse(db.Browse{Schema: m.schema, Table: e.Key})
	case scrScripts:
		if s, ok := m.findScript(e.Key); ok {
			m.openScript(s)
			// found by its content: go to the text that matched
			if f := m.scripts.Filter; f != "" && m.Stack.Top() == scrScript && !tk.SmartMatch(e.Name, f) {
				m.script.pattern = f
				m.script.jump(1, true, m.H-3)
			}
		}
	case scrGrep:
		return m.openGrepHit(e.Key)
	}
	return nil
}

func (m *model) back() tea.Cmd {
	if m.Stack.Len() == 1 {
		m.SetStatus("at the connection list (q quits)", false)
		return nil
	}
	m.Stack.Pop()
	return nil
}

// tableHere is the table under the cursor or shown in the grid.
func (m *model) tableHere() string {
	switch m.Stack.Top() {
	case scrTables:
		if e, ok := m.tables.Current(); ok {
			return e.Key
		}
	case scrData, scrRow:
		if b := m.grid.browse; b != nil {
			return b.Table
		}
	}
	return ""
}

func (m *model) openStruct() tea.Cmd {
	t := m.tableHere()
	if t == "" {
		if m.Stack.Top() == scrData {
			m.SetStatus("structure is available for tables, not for query results", false)
		}
		return nil
	}
	return m.loadStruct(t)
}

// sortColumn sorts by the column under the cursor: a table is re-read with
// ORDER BY, so the order holds beyond the loaded rows; a query result is
// sorted in place.
func (m *model) sortColumn() tea.Cmd {
	g := &m.grid
	if g.res == nil || g.col >= len(g.res.Columns) {
		return nil
	}
	name := g.res.Columns[g.col]
	if g.browse == nil {
		g.sortBy(g.col)
		g.sorted()
		return nil
	}
	b := *g.browse
	switch {
	case b.Order != name:
		b.Order, b.Desc = name, false
	case !b.Desc:
		b.Desc = true
	default:
		b.Order = ""
	}
	return m.browse(b)
}

func (m *model) cmdYank() tea.Cmd {
	switch m.Stack.Top() {
	case scrData:
		if m.grid.selecting {
			return m.yankRows("tsv")
		}
		if c, ok := m.grid.cell(); ok {
			return tk.Yank(c.S, m.grid.res.Columns[m.grid.col])
		}
	case scrRow:
		if row, ok := m.grid.row(); ok {
			return tk.Yank(rowJSON(m.grid.res.Columns, row), "row as JSON")
		}
	case scrScript:
		if f := &m.script; f.sel.Active() {
			return tk.Yank(f.takeSelection(false), "selection")
		}
		return tk.Yank(m.script.yankLines(1), "line")
	case scrStruct:
		return tk.Yank(m.structOf, "table name")
	}
	if l := m.list(); l != nil {
		if e, ok := l.Current(); ok {
			return tk.Yank(e.Name, "name")
		}
	}
	return nil
}

// rowJSON renders a row as a JSON object, keeping the column order.
func rowJSON(cols []string, row []db.Cell) string {
	var b strings.Builder
	b.WriteString("{\n")
	for i, c := range row {
		k, _ := json.Marshal(cols[i])
		v := []byte("null")
		if !c.Null {
			v, _ = json.Marshal(c.S)
		}
		b.WriteString("  " + string(k) + ": " + string(v))
		if i < len(row)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("}")
	return b.String()
}
