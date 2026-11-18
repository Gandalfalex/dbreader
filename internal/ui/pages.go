package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	tk "github.com/Gandalfalex/tuikit"
)

// refreshViewport rebuilds content for pager screens.
func (m *model) refreshViewport() {
	content, wrap := m.pageContent()
	m.vp.VP.SoftWrap = wrap
	m.vp.Refresh(content)
}

// pageContent is the text of the pager screen on top, and whether it is
// soft-wrapped.
func (m *model) pageContent() (string, bool) {
	switch m.Stack.Top() {
	case scrRow:
		return m.renderRecord(), true
	case scrStruct:
		return m.renderStruct(), false
	case scrHelp:
		return helpPage(), false
	case scrErrors:
		return m.Errors.Render(theme), true
	}
	return "", false
}

// openPage pushes a pager screen and renders its content.
func (m *model) openPage(s screen, atBottom bool) {
	if m.Stack.Top() != s {
		m.Stack.Push(s)
	}
	content, wrap := m.pageContent()
	m.vp.Show(content, wrap, atBottom)
}

// renderRecord shows the row under the cursor, one column per line.
func (m *model) renderRecord() string {
	row, ok := m.grid.row()
	if !ok {
		return ""
	}
	res := m.grid.res
	nameW := 0
	for _, c := range res.Columns {
		nameW = max(nameW, ansi.StringWidth(tk.CleanLine(c, maxCellBytes)))
	}
	nameW = min(nameW, 32) + 2
	typeW := 0
	for _, t := range res.Types {
		typeW = max(typeW, len(t))
	}
	typeW = min(typeW, 20) + 2
	var b strings.Builder
	b.WriteString("\n")
	for i, c := range row {
		val := tk.Clean(c.S)
		if c.Null {
			val = sMuted.Render("NULL")
		}
		// continuation lines of multi-line values line up under the first
		val = strings.ReplaceAll(val, "\n", "\n"+strings.Repeat(" ", 2+nameW+typeW))
		b.WriteString("  " + sCrumb.Render(tk.Pad(tk.CleanLine(res.Columns[i], maxCellBytes), nameW)) +
			sMuted.Render(tk.Pad(strings.ToLower(res.Types[i]), typeW)) + val + "\n")
	}
	return b.String()
}

func (m *model) renderStruct() string {
	nameW, typeW := 6, 4
	for _, c := range m.cols {
		nameW = max(nameW, ansi.StringWidth(c.Name))
		typeW = max(typeW, ansi.StringWidth(c.Type))
	}
	nameW, typeW = min(nameW, 40)+2, min(typeW, 40)+2
	var b strings.Builder
	b.WriteString("\n  " + sSection.Render("COLUMNS") + sMuted.Render("  "+m.conn.Qualified(m.schema, m.structOf)) + "\n\n")
	b.WriteString("  " + sHeader.Render(tk.Pad("NAME", nameW)+tk.Pad("TYPE", typeW)+tk.Pad("NULL", 6)+tk.Pad("KEY", 5)+"DEFAULT") + "\n")
	for _, c := range m.cols {
		null, key := "yes", ""
		if !c.Nullable {
			null = sWarn.Render("no")
		}
		if c.PK {
			key = sOK.Render("PK")
		}
		b.WriteString("  " + tk.Pad(tk.CleanLine(c.Name, maxCellBytes), nameW) + sCrumb.Render(tk.Pad(c.Type, typeW)) +
			tk.Pad(null, 6) + tk.Pad(key, 5) + sMuted.Render(tk.CleanLine(c.Default, maxCellBytes)) + "\n")
	}
	if len(m.cols) == 0 {
		b.WriteString(sMuted.Render("  (no columns)") + "\n")
	}
	return b.String()
}
