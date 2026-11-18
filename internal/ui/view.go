package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"dbreader/internal/sqlsafe"
	tk "github.com/Gandalfalex/tuikit"
	"github.com/Gandalfalex/vim-kit/visual"
)

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "dbreader"
	return v
}

func (m *model) render() string {
	if m.W == 0 {
		return ""
	}
	var body string
	h := m.H - tk.Chrome
	switch {
	case m.guarding():
		body = m.renderGuard(h)
	case m.list() != nil:
		body = m.renderList()
	case m.Stack.Top() == scrData:
		body = m.renderGrid()
	case m.Stack.Top() == scrScript:
		body = m.script.render(m.W, h)
	default:
		body = m.vp.View()
	}
	var popup []string
	atTop := false
	switch {
	case m.prompt.Kind == inputCommand:
		popup = tk.Popup(theme, &m.cmd.Line, m.W, maxPopup)
	case m.prompt.Kind == inputSQL:
		popup = tk.Popup(theme, &m.sqlHist, m.W, maxPopup)
	case m.prompt.Kind == inputNone && m.Stack.Top() == scrScript && m.script.insert && !m.guarding():
		popup = tk.Popup(theme, &m.edCompl, m.W, maxPopup)
		// keep the line being typed visible
		atTop = m.script.Cursor-m.script.Top >= h-len(popup)
	}
	return tk.Frame(m.titleBar(), tk.Overlay(body, popup, atTop), m.statusBar(), m.hints())
}

// titleBar shows where we are: connection › schema › table.
func (m *model) titleBar() string {
	var parts []string
	if m.conn != nil {
		p := m.conn.Profile
		name := sCrumb.Render(p.Name)
		switch {
		case p.ReadOnly:
			name += sOK.Render(" [read-only]")
		case p.Protect:
			name += sErr.Render(" [protected]")
		}
		parts = append(parts, name)
		if m.schema != "" && m.Stack.Top() != scrSchemas && m.Stack.Top() != scrConns {
			parts = append(parts, sCrumb.Render(m.schema))
		}
	}
	switch m.Stack.Top() {
	case scrData, scrRow:
		if b := m.grid.browse; b != nil {
			parts = append(parts, sDir.Render(b.Table))
			if b.Where != "" {
				parts = append(parts, sWarn.Render("where "+tk.CleanLine(b.Where, maxCellBytes)))
			}
			if b.Search != "" {
				parts = append(parts, sWarn.Render("rows containing "+strconv.Quote(b.Search)))
			}
		} else {
			parts = append(parts, sWarn.Render(tk.CleanLine(strings.Join(strings.Fields(m.grid.sql), " "), maxCellBytes)))
		}
		if m.Stack.Top() == scrRow {
			parts = append(parts, sMuted.Render("row "+strconv.Itoa(m.grid.Cursor+1)))
		}
	case scrGrep:
		parts = append(parts, sWarn.Render("tables with rows containing "+strconv.Quote(m.grepText)))
	case scrScripts:
		parts = append(parts, sMuted.Render("scripts"))
	case scrScript:
		name := sDir.Render(m.scriptFile.Name)
		if m.script.dirty {
			name += sWarn.Render(" [+]")
		}
		parts = append(parts, sMuted.Render("scripts"), name)
	case scrStruct:
		parts = append(parts, sDir.Render(m.structOf), sMuted.Render("structure"))
	case scrHelp:
		parts = append(parts, sMuted.Render("help"))
	case scrErrors:
		parts = append(parts, sErr.Render("error log"))
	}
	return tk.TitleBar(theme, m.W, "dbreader", parts...)
}

func (m *model) renderList() string {
	l := m.list()
	h := m.listHeight()
	var header string
	nameW := m.W - 2
	switch m.Stack.Top() {
	case scrConns:
		header = tk.Pad("CONNECTION", 26) + tk.Pad("DRIVER", 10) + "TARGET"
	case scrSchemas:
		nameW = max(20, m.W-2-10-8)
		header = tk.Pad("SCHEMA", nameW) + tk.PadLeft("OBJECTS", 8)
	case scrTables:
		nameW = max(20, m.W-2-10-14)
		header = tk.Pad("TABLE", nameW) + tk.Pad("TYPE", 10) + tk.PadLeft("ROWS", 13)
	case scrGrep:
		nameW = max(20, m.W-2-14)
		header = tk.Pad("TABLE", nameW) + tk.PadLeft("MATCHING ROWS", 13)
	case scrScripts:
		nameW = max(20, m.W-2-24)
		header = tk.Pad("SCRIPT", nameW) + "LINKED TO"
	}
	empty := ""
	if m.Ops.Loading() {
		empty = m.Ops.Spinner()
	}
	return l.View(theme, m.W, h, tk.ListView[entry]{
		Header: header,
		Row:    func(e entry, _ int) string { return m.renderRow(e, nameW) },
		Empty:  empty,
	})
}

func (m *model) renderRow(e entry, nameW int) string {
	name := tk.CleanLine(e.Name, maxCellBytes)
	switch m.Stack.Top() {
	case scrConns:
		i, _ := strconv.Atoi(e.Key)
		p := m.settings.Profiles[i]
		// tags first, so a long target is cut instead of them
		tags := ""
		if p.Source != "" {
			tags += sMuted.Render("  " + p.Source)
		}
		if n := len(p.Scripts); n > 0 {
			tags += sMuted.Render(fmt.Sprintf(" · %d scripts", n))
		}
		switch {
		case p.Blocked != "":
			tags += sWarn.Render("  unsupported")
		case p.ReadOnly:
			tags += sOK.Render("  read-only")
		case p.Protect:
			tags += sErr.Render("  protected")
		}
		target := tk.TruncLeft(p.Target(), max(12, m.W-2-36-lipgloss.Width(tags)))
		return tk.Pad(name, 26) + sMuted.Render(tk.Pad(p.Driver, 10)+target) + tags
	case scrGrep:
		return tk.Pad(name, nameW) + sWarn.Render(tk.PadLeft(e.Cols[0], 13))
	case scrScripts:
		row := tk.Pad(name, nameW)
		if e.Dim {
			return sMuted.Render(row + e.Cols[0])
		}
		return row + sCrumb.Render(e.Cols[0])
	case scrSchemas:
		row := tk.Pad(name, nameW) + tk.PadLeft(e.Cols[0], 8)
		if e.Dim {
			row = sMuted.Render(row)
		}
		return row + sOK.Render("  "+e.Cols[1])
	case scrTables:
		row := tk.Pad(name, nameW)
		if e.Dim {
			row = sMuted.Render(row)
		}
		return row + sMuted.Render(tk.Pad(e.Cols[0], 10)+tk.PadLeft(e.Cols[1], 13))
	}
	if e.Dim {
		return sMuted.Render(name)
	}
	return name
}

func (m *model) renderGrid() string {
	g := &m.grid
	h := m.listHeight()
	var b strings.Builder
	if g.res == nil {
		b.WriteString("\n")
		for range h {
			b.WriteString("\n")
		}
		return b.String()
	}
	g.Scroll(len(g.vis), h)
	avail := m.W - 2
	g.scrollCols(avail)

	// columns that fit, the last one possibly cut short
	type column struct{ i, w int }
	var cols []column
	for i, used := g.left, 0; i < len(g.widths) && used < avail; i++ {
		w := min(g.widths[i], avail-used)
		cols = append(cols, column{i, w})
		used += w + colGap
	}
	gap := strings.Repeat(" ", colGap)
	fit := func(s string, c column) string {
		if g.numeric[c.i] {
			return tk.PadLeft(s, c.w)
		}
		return tk.Pad(s, c.w)
	}

	b.WriteString("  ")
	for _, c := range cols {
		name := tk.CleanLine(g.res.Columns[c.i], maxCellBytes)
		switch {
		case g.browse != nil && g.browse.Order == g.res.Columns[c.i]:
			name += sortArrow(g.browse.Desc)
		case g.browse == nil && g.sortCol == c.i:
			name += sortArrow(g.sortDesc)
		}
		st := sHeader
		if c.i == g.col {
			st = sCrumb.Bold(true)
		}
		b.WriteString(st.Render(fit(name, c)) + gap)
	}
	b.WriteByte('\n')

	lines := 0
	if len(g.vis) == 0 {
		msg := "  (no rows)"
		if g.filter != "" {
			msg = "  no rows match filter: " + g.filter
		}
		b.WriteString(sMuted.Render(msg) + "\n")
		lines++
	}
	for r := g.Top; r < len(g.vis) && r < g.Top+h; r++ {
		row := g.res.Rows[g.vis[r]]
		cur := r == g.Cursor
		picked := g.selected(r) && !cur
		var line strings.Builder
		used := 2
		if cur {
			line.WriteString(sSel.Render("▌ "))
		} else if picked {
			line.WriteString(sSel.Render("  "))
		} else {
			line.WriteString("  ")
		}
		for _, c := range cols {
			cell := row[c.i]
			text := fit(tk.CleanLine(cell.S, maxCellBytes), c)
			if cell.Null {
				text = fit("NULL", c)
			}
			switch {
			case cur && c.i == g.col:
				line.WriteString(sCell.Render(text) + sSel.Render(gap))
			case (cur || picked) && cell.Null:
				line.WriteString(sSelNull.Render(text + gap))
			case cur, picked:
				line.WriteString(sSel.Render(text + gap))
			case cell.Null:
				line.WriteString(sMuted.Render(text) + gap)
			default:
				line.WriteString(text + gap)
			}
			used += c.w + colGap
		}
		if (cur || picked) && used < m.W {
			line.WriteString(sSel.Render(strings.Repeat(" ", m.W-used)))
		}
		b.WriteString(ansi.Truncate(line.String(), m.W, ""))
		b.WriteByte('\n')
		lines++
	}
	for ; lines < h; lines++ {
		b.WriteString(sMuted.Render("~"))
		b.WriteByte('\n')
	}
	return b.String()
}

// runKinds says what running text would do: the kind of its statement, or
// a count per kind when it holds several.
func (m *model) runKinds(text string) string {
	d := m.conn.Dialect()
	var n [4]int
	stmts := sqlsafe.Split(text, d)
	for _, st := range stmts {
		n[sqlsafe.Classify(st, d).Kind]++
	}
	styles := [4]lipgloss.Style{sOK, sWarn, sErr, sErr}
	var parts []string
	for k, c := range n {
		switch {
		case c == 0:
		case len(stmts) == 1:
			parts = append(parts, styles[k].Render(sqlsafe.Kind(k).String()))
		default:
			parts = append(parts, styles[k].Render(fmt.Sprintf("%d %s", c, sqlsafe.Kind(k))))
		}
	}
	if len(parts) == 0 {
		return sMuted.Render("(nothing)")
	}
	return strings.Join(parts, sMuted.Render(", "))
}

func sortArrow(desc bool) string {
	if desc {
		return "↓"
	}
	return "↑"
}

func (m *model) statusBar() string {
	mode := tk.ModeNormal
	if m.prompt.Active() {
		mode = m.prompt.Mode
	}
	if m.prompt.Kind == inputNone && m.Stack.Top() == scrData && m.grid.selecting && !m.guarding() {
		mode = tk.ModeVLine
	}
	if m.pending != nil {
		mode = modeTxn
	}
	if m.prompt.Kind == inputNone && m.Stack.Top() == scrScript && !m.guarding() {
		switch {
		case m.script.insert:
			mode = tk.ModeInsert
		case m.script.sel.Mode == visual.Char:
			mode = tk.ModeVisual
		case m.script.sel.Mode == visual.Line:
			mode = tk.ModeVLine
		}
	}
	if m.prompt.Active() {
		return tk.PromptBar(theme, m.W, mode, m.prompt.Input)
	}

	var mid []string
	switch l := m.list(); {
	case m.guarding():
	case l != nil:
		mid = append(mid, l.Position()...)
		if l.Filter != "" {
			mid = append(mid, sOK.Render("/"+l.Filter))
		}
	case m.Stack.Top() == scrData && m.grid.res != nil:
		g := &m.grid
		pos := 0
		if len(g.vis) > 0 {
			pos = g.Cursor + 1
		}
		total := strconv.Itoa(len(g.vis))
		if g.res.Truncated {
			total += "+"
		}
		mid = append(mid, fmt.Sprintf("%d/%s", pos, total))
		if len(g.vis) != len(g.res.Rows) {
			mid = append(mid, fmt.Sprintf("(of %d)", len(g.res.Rows)))
		}
		if g.col < len(g.res.Columns) {
			col := fmt.Sprintf("col %d/%d %s", g.col+1, len(g.res.Columns), tk.CleanLine(g.res.Columns[g.col], maxCellBytes))
			if t := g.res.Types[g.col]; t != "" {
				col += " " + sMuted.Render(strings.ToLower(t))
			}
			mid = append(mid, col)
		}
		if g.res.Truncated {
			mid = append(mid, sWarn.Render(fmt.Sprintf("first %d rows (:limit N)", len(g.res.Rows))))
		}
		if g.filter != "" {
			mid = append(mid, sOK.Render("/"+g.filter))
		}
		if g.selecting {
			from, to := g.selRange()
			mid = append(mid, sWarn.Render(fmt.Sprintf("%d rows selected", to-from+1)))
		}
	case m.Stack.Top() == scrScript:
		f := &m.script
		mid = append(mid, fmt.Sprintf("%d/%d", f.Cursor+1, len(f.lines)), fmt.Sprintf("col %d", f.col+1))
		if f.pattern != "" {
			mid = append(mid, sOK.Render("/"+f.pattern))
		}
		if text, what := f.target(); what != "" {
			mid = append(mid, ":run → "+what+": "+m.runKinds(text))
		}
	default:
		mid = append(mid, m.vp.Percent())
	}
	return tk.StatusBar(theme, m.W, mode, m.StatusParts(mid, m.Stack.Top() == scrErrors))
}

func (m *model) hints() string {
	var h string
	switch {
	case m.prompt.Kind == inputCommand:
		h = "tab/S-tab complete · ↑↓ history · enter run · esc cancel"
	case m.prompt.Kind == inputConfirm:
		h = "enter execute · esc cancel"
	case m.prompt.Kind == inputFind:
		h = "enter keep · esc back to where you were · then n / N next and previous match"
	case m.prompt.Kind == inputSearch:
		h = "enter search on the server · esc cancel"
	case m.prompt.Kind == inputSQL:
		h = "tab/S-tab complete · ↑↓ history · enter run · esc cancel · reads run at once, writes ask first"
	case m.pending != nil:
		h = "c commit · r roll back · ctrl+c roll back and quit"
	default:
		switch m.Stack.Top() {
		case scrConns:
			h = "j/k move · l/enter connect · / filter · : command · :help · q quit"
		case scrSchemas:
			h = "j/k move · l open · h back · / filter · * pin · zh show hidden · s sql · S scripts · : command · :help"
		case scrTables:
			h = "l open · / filter by table or column name · ? search rows in all tables · i structure · s sql · S scripts · h back · :help"
		case scrGrep:
			h = "l/enter show the matching rows · / filter · ? new search · esc cancel · h back"
		case scrScripts:
			h = "l/enter open · e open in $EDITOR · / filter by name or content · :new <name> create · h back"
		case scrScript:
			switch {
			case m.script.insert:
				h = "type · tab/S-tab complete · esc normal mode"
			case m.script.sel.Active():
				h = "move to extend · enter run selection · y yank · d delete · c change · esc clear"
			default:
				h = "enter run statement under cursor · e edit in $EDITOR · v/V select · / n N search · i a o x dd p u edit here · q back"
			}
		case scrData:
			h = "h/l columns · enter record · / filter loaded · ? search table · o sort · y cell · Y row · V rows then y · :yank csv|json · - back"
		case scrRow:
			h = "j/k ^d/^u scroll · y yank as JSON · h back"
		case scrStruct:
			h = "j/k scroll · y yank name · r reload · h back"
		case scrHelp:
			h = "j/k scroll · q back"
		case scrErrors:
			h = "j/k scroll · G newest · q back · full log: " + m.logPath
		}
	}
	return tk.HintBar(theme, m.W, h)
}
