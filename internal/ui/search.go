package ui

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"dbreader/internal/db"

	tea "charm.land/bubbletea/v2"

	tk "github.com/Gandalfalex/tuikit"
	"github.com/Gandalfalex/vim-kit/keys"
	"github.com/Gandalfalex/vim-kit/visual"
)

const (
	actSearch keys.Action = "search"
	actNext   keys.Action = "search_next"
	actPrev   keys.Action = "search_prev"
)

// grepLimit caps the matches counted per table in a schema-wide search.
const grepLimit = 1000

// ---- / in a script ----

// matchCols returns the rune columns where pat occurs in line (smart-case).
func matchCols(line, pat string) []int {
	if pat == "" {
		return nil
	}
	hay, needle := []rune(line), []rune(pat)
	if strings.ToLower(pat) == pat {
		hay = []rune(strings.ToLower(line))
		if len(hay) != len([]rune(line)) { // case folding changed the length
			hay = []rune(line)
		}
	}
	var cols []int
	for i := 0; i+len(needle) <= len(hay); i++ {
		if string(hay[i:i+len(needle)]) == pat {
			cols = append(cols, i)
		}
	}
	return cols
}

// find returns the next match of the pattern from pos in direction dir,
// wrapping around the file. With here, a match at pos itself counts.
func (f *fileView) find(pos visual.Pos, dir int, here bool) (visual.Pos, bool) {
	n := len(f.lines)
	for k := 0; k <= n; k++ {
		l := ((pos.Line+dir*k)%n + n) % n
		cols := matchCols(f.lines[l], f.pattern)
		if dir < 0 {
			for i, j := 0, len(cols)-1; i < j; i, j = i+1, j-1 {
				cols[i], cols[j] = cols[j], cols[i]
			}
		}
		for _, c := range cols {
			switch {
			case k > 0 && k < n:
			case k == n: // wrapped onto the starting line: the part before pos
				if (dir > 0 && c >= pos.Col) || (dir < 0 && c <= pos.Col) {
					continue
				}
			case here && c == pos.Col:
			case (dir > 0 && c <= pos.Col) || (dir < 0 && c >= pos.Col):
				continue
			}
			return visual.Pos{Line: l, Col: c}, true
		}
	}
	return pos, false
}

// jump moves the cursor to the next match and reports whether there is one.
func (f *fileView) jump(dir int, here bool, h int) bool {
	p, ok := f.find(f.pos(), dir, here)
	if ok {
		f.Cursor, f.col = p.Line, p.Col
		f.Scroll(len(f.lines), h)
	}
	return ok
}

func (m *model) openFind() (tea.Model, tea.Cmd) {
	m.findFrom = m.script.pos()
	m.prevFilter = m.script.pattern
	return m, m.prompt.Open(inputFind, tk.ModeSearch, "/", "search in this script (smart-case; n / N for the next and previous match)", "")
}

func (m *model) findNext(dir int) {
	f := &m.script
	switch {
	case f.pattern == "":
		m.SetStatus("no search pattern (/ starts one)", false)
	case !f.jump(dir, false, m.H-3):
		m.SetStatus("pattern not found: "+f.pattern, true)
	}
}

// ---- ? : search on the server ----

func (m *model) openSearch() (tea.Model, tea.Cmd) {
	var placeholder string
	switch {
	case m.Stack.Top() == scrTables || m.Stack.Top() == scrGrep:
		placeholder = "search every table of " + m.schemaLabel() + " for rows containing this text"
	case m.Stack.Top() == scrData && m.grid.browse != nil:
		placeholder = "search all rows of " + m.grid.browse.Table + " on the server, any column (empty clears)"
	default:
		m.SetStatus("? searches a table, or all tables from the table list; query results: use / or put it in the statement", false)
		return m, nil
	}
	return m, m.prompt.Open(inputSearch, tk.ModeSearch, "?", placeholder, "")
}

func (m *model) schemaLabel() string {
	if m.schema == "" {
		return m.conn.Profile.Name
	}
	return m.schema
}

// handlePromptKey serves the / prompt of a script and the ? prompt.
func (m *model) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	find := m.prompt.Kind == inputFind
	f := &m.script
	switch msg.String() {
	case "esc", "ctrl+[":
		m.prompt.Close()
		if find {
			f.pattern = m.prevFilter
			f.Cursor, f.col = m.findFrom.Line, m.findFrom.Col
		}
		return m, nil
	case "enter":
		m.prompt.Close()
		if !find {
			return m, m.search(strings.TrimSpace(m.prompt.Input.Value()))
		}
		if f.pattern != "" && len(matchCols(f.lines[f.Cursor], f.pattern)) == 0 {
			m.SetStatus("pattern not found: "+f.pattern, true)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.prompt.Input, cmd = m.prompt.Input.Update(msg)
	if find { // search as you type, from where the search started
		f.pattern = m.prompt.Input.Value()
		f.Cursor, f.col = m.findFrom.Line, m.findFrom.Col
		f.jump(1, true, m.H-3)
	}
	return m, cmd
}

// search runs the ? prompt: one table's rows when a table is open, else
// every table of the schema.
func (m *model) search(text string) tea.Cmd {
	if m.Stack.Top() == scrData {
		b := *m.grid.browse
		b.Search, b.SearchCols = text, m.searchColumns(b.Table, m.grid.res.Columns)
		return m.browse(b)
	}
	if text == "" {
		return nil
	}
	return m.startGrep(text)
}

// startGrep counts, table by table, the rows containing text, and lists
// the tables that have any.
func (m *model) startGrep(text string) tea.Cmd {
	if !m.needConn() {
		return nil
	}
	if m.colSchema != m.schema || m.colNames == nil {
		m.SetStatus("column names are still loading, try again in a moment", true)
		return nil
	}
	if !m.Stack.PopTo(scrTables) {
		m.SetStatus("pick a schema first", true)
		return nil
	}
	var tables []string
	for _, e := range m.tables.All {
		tables = append(tables, e.Key)
	}
	ctx, gen := m.Ops.Start("searching " + strconv.Itoa(len(tables)) + " tables")
	conn, schema, cols := m.conn, m.schema, m.searchCols
	m.grep, m.grepText, m.grepSeen, m.grepFailed = newList(), text, 0, 0
	m.Stack.Push(scrGrep)

	ch := make(chan tea.Msg, 8)
	go func() {
		defer close(ch)
		send := func(msg grepMsg) bool {
			msg.Op, msg.ch = gen, ch
			select {
			case ch <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for _, t := range tables {
			n, err := conn.CountMatches(ctx, schema, t, cols[t], text, grepLimit)
			if ctx.Err() != nil || !send(grepMsg{table: t, n: n, err: err}) {
				return
			}
		}
		send(grepMsg{done: true})
	}()
	return waitFor(ch)
}

func waitFor(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *model) grepResult(msg grepMsg) tea.Cmd {
	if msg.done {
		m.Ops.Done()
		status := fmt.Sprintf("%q found in %d of %d tables", m.grepText, len(m.grep.All), m.grepSeen)
		if m.grepFailed > 0 {
			status += fmt.Sprintf(" (%d could not be searched, see log)", m.grepFailed)
		}
		m.SetStatus(status, false)
		return nil
	}
	m.grepSeen++
	switch {
	case msg.err != nil:
		m.grepFailed++
		slog.Warn("search table", "table", msg.table, "err", msg.err)
	case msg.n > 0:
		count := tk.FmtInt(int64(msg.n))
		if msg.n >= grepLimit {
			count += "+"
		}
		m.grep.All = append(m.grep.All, entry{Key: msg.table, Name: msg.table, Cols: []string{count}})
		m.grep.Refilter()
	}
	return waitFor(msg.ch)
}

// openGrepHit shows the matching rows of a table from the search results.
func (m *model) openGrepHit(table string) tea.Cmd {
	return m.browse(db.Browse{Schema: m.schema, Table: table, Search: m.grepText, SearchCols: m.searchColumns(table, nil)})
}

// searchColumns returns the columns of table that text can be searched in,
// falling back to all the columns shown when the catalog has not loaded.
func (m *model) searchColumns(table string, shown []string) []string {
	if m.colSchema == m.schema && m.searchCols != nil {
		if cols, ok := m.searchCols[table]; ok {
			return cols
		}
	}
	return shown
}

// ---- yanking results ----

var yankFormats = []string{"tsv", "csv", "json", "markdown"}

// yankRows copies the selected rows of the grid, or all loaded ones, in
// the given format.
func (m *model) yankRows(format string) tea.Cmd {
	g := &m.grid
	if m.Stack.Top() != scrData && m.Stack.Top() != scrRow || g.res == nil {
		m.SetStatus("no result on screen to yank", true)
		return nil
	}
	if format == "" {
		format = "tsv"
	}
	ok := false
	for _, f := range yankFormats {
		if strings.HasPrefix(f, format) {
			format, ok = f, true
			break
		}
	}
	if !ok {
		m.SetStatus("usage: :yank "+strings.Join(yankFormats, "|"), true)
		return nil
	}
	from, to := 0, len(g.vis)-1
	if g.selecting {
		from, to = g.selRange()
		g.selecting = false
	}
	var rows [][]db.Cell
	for i := from; i <= to && i < len(g.vis); i++ {
		rows = append(rows, g.res.Rows[g.vis[i]])
	}
	if len(rows) == 0 {
		m.SetStatus("no rows to yank", false)
		return nil
	}
	what := fmt.Sprintf("%d rows as %s", len(rows), format)
	if len(rows) == 1 {
		what = "1 row as " + format
	}
	return tk.Yank(formatRows(g.res.Columns, rows, format), what)
}

func formatRows(cols []string, rows [][]db.Cell, format string) string {
	var b strings.Builder
	line := func(vals []string, sep string, quote func(string) string) {
		for i, v := range vals {
			if i > 0 {
				b.WriteString(sep)
			}
			b.WriteString(quote(v))
		}
		b.WriteByte('\n')
	}
	values := func(row []db.Cell, null string) []string {
		out := make([]string, len(row))
		for i, c := range row {
			out[i] = c.S
			if c.Null {
				out[i] = null
			}
		}
		return out
	}
	switch format {
	case "json":
		b.WriteString("[\n")
		for i, row := range rows {
			obj := strings.ReplaceAll(rowJSON(cols, row), "\n", "\n  ")
			b.WriteString("  " + obj)
			if i < len(rows)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString("]\n")
	case "csv":
		quote := func(s string) string {
			if strings.ContainsAny(s, ",\"\n\r") {
				return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
			}
			return s
		}
		line(cols, ",", quote)
		for _, row := range rows {
			line(values(row, ""), ",", quote)
		}
	case "markdown":
		cell := func(s string) string {
			return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
		}
		b.WriteString("| ")
		line(cols, " | ", cell)
		b.WriteString("|" + strings.Repeat(" --- |", len(cols)) + "\n")
		for _, row := range rows {
			b.WriteString("| ")
			line(values(row, "NULL"), " | ", cell)
		}
		return mdClose(b.String())
	default: // tsv: tabs and line breaks inside values become spaces
		flat := func(s string) string { return strings.NewReplacer("\t", " ", "\n", " ", "\r", "").Replace(s) }
		line(cols, "\t", flat)
		for _, row := range rows {
			line(values(row, ""), "\t", flat)
		}
	}
	return b.String()
}

// mdClose ends every table row with its closing bar.
func mdClose(table string) string {
	lines := strings.Split(strings.TrimSuffix(table, "\n"), "\n")
	for i, l := range lines {
		if !strings.HasSuffix(l, "|") {
			lines[i] = l + " |"
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
