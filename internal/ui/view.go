package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"dbreader/internal/sqlsafe"
	"github.com/Gandalfalex/vim-kit/visual"
)

func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "dbreader"
	return v
}

func (m *model) render() string {
	if m.w == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.titleBar())
	b.WriteByte('\n')

	switch {
	case m.guarding():
		b.WriteString(m.renderGuard(m.h - 3))
	case m.list() != nil:
		b.WriteString(m.renderList())
	case m.top() == scrData:
		b.WriteString(m.renderGrid())
	case m.top() == scrScript:
		b.WriteString(m.script.render(m.w, m.h-3))
	default:
		b.WriteString(m.vp.View())
		b.WriteByte('\n')
	}
	body := b.String()
	var popup []string
	atTop := false
	switch {
	case m.mode == inputCommand:
		popup = m.renderPopup(&m.cmd)
	case m.mode == inputSQL:
		popup = m.renderPopup(&m.sqlHist)
	case m.mode == inputNone && m.top() == scrScript && m.script.insert && !m.guarding():
		popup = m.renderPopup(&m.edCompl)
		// keep the line being typed visible
		atTop = m.script.Cursor-m.script.Top >= m.h-3-len(popup)
	}
	if len(popup) > 0 {
		// draw the completion menu over the bottom of the body, like vim's pum
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		k := min(len(popup), len(lines)-1) // keep the title bar
		if atTop {
			copy(lines[1:], popup[:k])
		} else {
			copy(lines[len(lines)-k:], popup[len(popup)-k:])
		}
		body = strings.Join(lines, "\n") + "\n"
	}
	return body + m.statusBar() + "\n" + m.hints()
}

// titleBar shows where we are: connection › schema › table.
func (m *model) titleBar() string {
	parts := []string{sTitle.Render("dbreader")}
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
		if m.schema != "" && m.top() != scrSchemas && m.top() != scrConns {
			parts = append(parts, sCrumb.Render(m.schema))
		}
	}
	switch m.top() {
	case scrData, scrRow:
		if b := m.grid.browse; b != nil {
			parts = append(parts, sDir.Render(b.Table))
			if b.Where != "" {
				parts = append(parts, sWarn.Render("where "+clean(b.Where, true)))
			}
			if b.Search != "" {
				parts = append(parts, sWarn.Render("rows containing "+strconv.Quote(b.Search)))
			}
		} else {
			parts = append(parts, sWarn.Render(clean(strings.Join(strings.Fields(m.grid.sql), " "), true)))
		}
		if m.top() == scrRow {
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
	return ansi.Truncate(strings.Join(parts, sMuted.Render(" › ")), m.w, "…")
}

func (m *model) renderList() string {
	l := m.list()
	h := m.listHeight()
	l.scroll(h)

	var b strings.Builder
	var header string
	nameW := m.w - 2
	switch m.top() {
	case scrConns:
		header = pad("CONNECTION", 26) + pad("DRIVER", 10) + "TARGET"
	case scrSchemas:
		nameW = max(20, m.w-2-10-8)
		header = pad("SCHEMA", nameW) + padLeft("OBJECTS", 8)
	case scrTables:
		nameW = max(20, m.w-2-10-14)
		header = pad("TABLE", nameW) + pad("TYPE", 10) + padLeft("ROWS", 13)
	case scrGrep:
		nameW = max(20, m.w-2-14)
		header = pad("TABLE", nameW) + padLeft("MATCHING ROWS", 13)
	case scrScripts:
		nameW = max(20, m.w-2-24)
		header = pad("SCRIPT", nameW) + "LINKED TO"
	}
	b.WriteString(sHeader.Render(ansi.Truncate("  "+header, m.w, "")))
	b.WriteByte('\n')

	lines := 0
	if len(l.vis) == 0 {
		msg := "  (empty)"
		if l.filter != "" {
			msg = "  no matches for filter: " + l.filter
		}
		if m.loading {
			msg = "  " + m.spin.View() + " " + m.what
		}
		b.WriteString(sMuted.Render(msg))
		b.WriteByte('\n')
		lines++
	}
	for i := l.Top; i < len(l.vis) && i < l.Top+h; i++ {
		row := m.renderRow(l.all[l.vis[i]], nameW)
		if i == l.Cursor {
			row = sSel.Render(pad("▌ "+ansi.Strip(row), m.w))
		} else {
			row = "  " + row
		}
		b.WriteString(ansi.Truncate(row, m.w, ""))
		b.WriteByte('\n')
		lines++
	}
	for ; lines < h; lines++ {
		b.WriteString(sMuted.Render("~"))
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *model) renderRow(e entry, nameW int) string {
	name := clean(e.Name, true)
	switch m.top() {
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
		target := truncLeft(p.Target(), max(12, m.w-2-36-lipgloss.Width(tags)))
		return pad(name, 26) + sMuted.Render(pad(p.Driver, 10)+target) + tags
	case scrGrep:
		return pad(name, nameW) + sWarn.Render(padLeft(e.Cols[0], 13))
	case scrScripts:
		row := pad(name, nameW)
		if e.Dim {
			return sMuted.Render(row + e.Cols[0])
		}
		return row + sCrumb.Render(e.Cols[0])
	case scrSchemas:
		row := pad(name, nameW) + padLeft(e.Cols[0], 8)
		if e.Dim {
			row = sMuted.Render(row)
		}
		return row + sOK.Render("  "+e.Cols[1])
	case scrTables:
		row := pad(name, nameW)
		if e.Dim {
			row = sMuted.Render(row)
		}
		return row + sMuted.Render(pad(e.Cols[0], 10)+padLeft(e.Cols[1], 13))
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
	avail := m.w - 2
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
			return padLeft(s, c.w)
		}
		return pad(s, c.w)
	}

	b.WriteString("  ")
	for _, c := range cols {
		name := clean(g.res.Columns[c.i], true)
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
			text := fit(clean(cell.S, true), c)
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
		if (cur || picked) && used < m.w {
			line.WriteString(sSel.Render(strings.Repeat(" ", m.w-used)))
		}
		b.WriteString(ansi.Truncate(line.String(), m.w, ""))
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
	modeName, modeColor := "NORMAL", cAccent
	switch m.mode {
	case inputFilter:
		modeName, modeColor = "FILTER", cOK
	case inputSQL:
		modeName, modeColor = "SQL", cWarn
	case inputCommand:
		modeName, modeColor = "COMMAND", cDir
	case inputConfirm:
		modeName, modeColor = "CONFIRM", cErr
	case inputFind, inputSearch:
		modeName, modeColor = "SEARCH", cWarn
	}
	if m.mode == inputNone && m.top() == scrData && m.grid.selecting && !m.guarding() {
		modeName, modeColor = "V-LINE", cWarn
	}
	if m.pending != nil {
		modeName, modeColor = "TXN OPEN", cErr
	}
	if m.mode == inputNone && m.top() == scrScript && !m.guarding() {
		switch {
		case m.script.insert:
			modeName, modeColor = "INSERT", cOK
		case m.script.sel.Mode == visual.Char:
			modeName, modeColor = "VISUAL", cWarn
		case m.script.sel.Mode == visual.Line:
			modeName, modeColor = "V-LINE", cWarn
		}
	}
	if m.mode != inputNone {
		left := sMode.Background(modeColor).Render(modeName) + " "
		return left + ansi.Truncate(m.input.View(), m.w-lipgloss.Width(left), "")
	}

	left := sMode.Background(modeColor).Render(modeName)
	var mid []string
	if m.loading {
		mid = append(mid, m.spin.View()+" "+m.what)
	}
	switch l := m.list(); {
	case m.guarding():
	case l != nil:
		pos := 0
		if len(l.vis) > 0 {
			pos = l.Cursor + 1
		}
		mid = append(mid, fmt.Sprintf("%d/%d", pos, len(l.vis)))
		if len(l.vis) != len(l.all) {
			mid = append(mid, fmt.Sprintf("(of %d)", len(l.all)))
		}
		if l.filter != "" {
			mid = append(mid, sOK.Render("/"+l.filter))
		}
	case m.top() == scrData && m.grid.res != nil:
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
			col := fmt.Sprintf("col %d/%d %s", g.col+1, len(g.res.Columns), clean(g.res.Columns[g.col], true))
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
	case m.top() == scrScript:
		f := &m.script
		mid = append(mid, fmt.Sprintf("%d/%d", f.Cursor+1, len(f.lines)), fmt.Sprintf("col %d", f.col+1))
		if f.pattern != "" {
			mid = append(mid, sOK.Render("/"+f.pattern))
		}
		if text, what := f.target(); what != "" {
			mid = append(mid, ":run → "+what+": "+m.runKinds(text))
		}
	default:
		mid = append(mid, fmt.Sprintf("%3.0f%%", m.vp.ScrollPercent()*100))
	}
	if n := len(m.errs); n > 0 && m.top() != scrErrors {
		mid = append(mid, sErr.Render(fmt.Sprintf("E:%d", n)))
	}
	if p := m.keys.Partial(); p != "" {
		mid = append(mid, sWarn.Render(p))
	}
	if m.status != "" {
		st := m.status
		if m.statusErr {
			st = sErr.Render(st)
		}
		mid = append(mid, st)
	}
	line := left + " " + strings.Join(mid, sMuted.Render(" │ "))
	line = ansi.Truncate(line, m.w, "…")
	return sBar.Render(pad(line, m.w))
}

func (m *model) hints() string {
	var h string
	switch {
	case m.mode == inputCommand:
		h = "tab/S-tab complete · ↑↓ history · enter run · esc cancel"
	case m.mode == inputConfirm:
		h = "enter execute · esc cancel"
	case m.mode == inputFind:
		h = "enter keep · esc back to where you were · then n / N next and previous match"
	case m.mode == inputSearch:
		h = "enter search on the server · esc cancel"
	case m.mode == inputSQL:
		h = "tab/S-tab complete · ↑↓ history · enter run · esc cancel · reads run at once, writes ask first"
	case m.pending != nil:
		h = "c commit · r roll back · ctrl+c roll back and quit"
	default:
		switch m.top() {
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
	return sMuted.Render(ansi.Truncate(" "+h, m.w, "…"))
}
