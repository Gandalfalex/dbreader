package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// refreshViewport rebuilds content for pager screens.
func (m *model) refreshViewport() {
	switch m.top() {
	case scrRow:
		m.vp.SoftWrap = true
		m.vp.SetContent(m.renderRecord())
	case scrStruct:
		m.vp.SoftWrap = false
		m.vp.SetContent(m.renderStruct())
	case scrHelp:
		m.vp.SoftWrap = false
		m.vp.SetContent(helpPage())
	case scrErrors:
		m.vp.SoftWrap = true
		m.vp.SetContent(m.renderErrors())
	}
}

// openPage pushes a pager screen and renders its content.
func (m *model) openPage(s screen, atBottom bool) {
	if m.top() != s {
		m.push(s)
	}
	m.refreshViewport()
	if atBottom {
		m.vp.GotoBottom()
	} else {
		m.vp.GotoTop()
	}
}

func (m *model) renderErrors() string {
	if len(m.errs) == 0 {
		return "\n" + sMuted.Render("  no errors this session") + "\n"
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, e := range m.errs {
		b.WriteString("  " + sMuted.Render(e.at.Format("15:04:05")) + "  " + sWarn.Render(e.what) + "\n")
		b.WriteString("    " + sErr.Render(clean(e.err.Error(), false)) + "\n\n")
	}
	return b.String()
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
		nameW = max(nameW, ansi.StringWidth(clean(c, true)))
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
		val := clean(c.S, false)
		if c.Null {
			val = sMuted.Render("NULL")
		}
		// continuation lines of multi-line values line up under the first
		val = strings.ReplaceAll(val, "\n", "\n"+strings.Repeat(" ", 2+nameW+typeW))
		b.WriteString("  " + sCrumb.Render(pad(clean(res.Columns[i], true), nameW)) +
			sMuted.Render(pad(strings.ToLower(res.Types[i]), typeW)) + val + "\n")
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
	b.WriteString("  " + sHeader.Render(pad("NAME", nameW)+pad("TYPE", typeW)+pad("NULL", 6)+pad("KEY", 5)+"DEFAULT") + "\n")
	for _, c := range m.cols {
		null, key := "yes", ""
		if !c.Nullable {
			null = sWarn.Render("no")
		}
		if c.PK {
			key = sOK.Render("PK")
		}
		b.WriteString("  " + pad(clean(c.Name, true), nameW) + sCrumb.Render(pad(c.Type, typeW)) +
			pad(null, 6) + pad(key, 5) + sMuted.Render(clean(c.Default, true)) + "\n")
	}
	if len(m.cols) == 0 {
		b.WriteString(sMuted.Render("  (no columns)") + "\n")
	}
	return b.String()
}
