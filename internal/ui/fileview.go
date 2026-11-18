package ui

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Gandalfalex/vim-kit/keys"
	"github.com/Gandalfalex/vim-kit/motion"
	"github.com/Gandalfalex/vim-kit/visual"
	"github.com/charmbracelet/x/ansi"

	"dbreader/internal/sqlsafe"
)

// fileView is a small modal editor for a SQL script: vim motions, insert
// mode, visual selection, undo. :run executes the selection, or without one
// the statement the cursor is in.
type fileView struct {
	lines  []string
	starts []int          // byte offset of each line in the text
	spans  []sqlsafe.Span // statements, as byte ranges of the text
	d      *sqlsafe.Dialect
	motion.Window
	col, left int // cursor column and first visible column, in runes
	sel       visual.Selection

	insert bool
	fresh  bool // insert mode entered, nothing typed yet (no undo step taken)
	dirty  bool
	mtime  time.Time // of the file when it was read or last saved
	undo   []snapshot
	redo   []snapshot

	pattern string // last / search, highlighted

	reg     string // what x, d and y took
	regLine bool   // reg holds whole lines
}

type snapshot struct {
	lines       []string
	cursor, col int
}

const maxUndo = 200

func newFileView(text string, d *sqlsafe.Dialect) fileView {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	f := fileView{lines: strings.Split(strings.TrimSuffix(text, "\n"), "\n"), d: d}
	f.reindex()
	return f
}

// reindex recomputes line offsets and statements after the text changed.
func (f *fileView) reindex() {
	f.starts = f.starts[:0]
	off := 0
	for _, l := range f.lines {
		f.starts = append(f.starts, off)
		off += len(l) + 1
	}
	f.spans = sqlsafe.Spans(f.text(), f.d)
}

func (f *fileView) text() string { return strings.Join(f.lines, "\n") + "\n" }

func (f *fileView) pos() visual.Pos { return visual.Pos{Line: f.Cursor, Col: f.col} }

func (f *fileView) line() string { return f.lines[f.Cursor] }

func (f *fileView) lineLen() int { return utf8.RuneCountInString(f.line()) }

// clampCol keeps the cursor on a character; in insert mode it may also sit
// after the last one.
func (f *fileView) clampCol() {
	if f.insert {
		f.col = max(0, min(f.col, f.lineLen()))
		return
	}
	f.col = motion.ClampCol(f.line(), f.col)
}

// apply runs ev as a vertical or in-line motion.
func (f *fileView) apply(ev keys.Event, h int) bool {
	if f.Apply(ev, len(f.lines), h) {
		f.Scroll(len(f.lines), h)
		f.clampCol()
		return true
	}
	col, ok := motion.ApplyText(ev, f.line(), f.col)
	if ok {
		f.col = col
	}
	return ok
}

// offset is the cursor's byte offset in the text.
func (f *fileView) offset() int {
	off, col := f.starts[f.Cursor], f.col
	for _, r := range f.line() {
		if col == 0 {
			break
		}
		off += utf8.RuneLen(r)
		col--
	}
	return off
}

// span is the statement the cursor is in: the one that ends at the next
// semicolon, or the last one below the final semicolon.
func (f *fileView) span() (sqlsafe.Span, bool) {
	if len(f.spans) == 0 {
		return sqlsafe.Span{}, false
	}
	off := f.offset()
	i := sort.Search(len(f.spans), func(i int) bool { return f.spans[i].End >= off })
	return f.spans[min(i, len(f.spans)-1)], true
}

// statement returns the lines the cursor's statement covers.
func (f *fileView) statement() (from, to int, ok bool) {
	sp, ok := f.span()
	if !ok {
		return 0, 0, false
	}
	lineOf := func(off int) int {
		return sort.Search(len(f.starts), func(l int) bool { return f.starts[l] > off }) - 1
	}
	from, to = lineOf(sp.Start), lineOf(sp.End)
	// the span starts right after the previous semicolon: skip to its text
	for from < to && strings.TrimSpace(f.lineFrom(from, sp.Start)) == "" {
		from++
	}
	return from, to, true
}

// lineFrom is line l, without what precedes byte offset off of the text.
func (f *fileView) lineFrom(l, off int) string {
	if cut := off - f.starts[l]; cut > 0 && cut <= len(f.lines[l]) {
		return f.lines[l][cut:]
	}
	return f.lines[l]
}

// target is the text :run would execute and a description of it.
func (f *fileView) target() (text, what string) {
	if f.sel.Active() {
		from, to := f.sel.Bounds(f.pos(), f.lines)
		return f.sel.Text(f.pos(), f.lines), "selection, lines " + strconv.Itoa(from.Line+1) + "-" + strconv.Itoa(to.Line+1)
	}
	sp, ok := f.span()
	if !ok {
		return "", ""
	}
	from, _, _ := f.statement()
	return f.text()[sp.Start:sp.End], "statement at line " + strconv.Itoa(from+1)
}

// context returns the statement being typed and the cursor's rune index in
// it, for completion. Outside any statement it is the current line.
func (f *fileView) context() (text string, cursor int) {
	off := f.offset()
	if sp, ok := f.span(); ok && off >= sp.Start && off <= sp.End {
		all := f.text()
		return all[sp.Start:sp.End], utf8.RuneCountInString(all[sp.Start:off])
	}
	return f.line(), f.col
}

// ---- editing ----

func (f *fileView) snap() snapshot {
	return snapshot{lines: append([]string(nil), f.lines...), cursor: f.Cursor, col: f.col}
}

func (f *fileView) restore(s snapshot) {
	f.lines, f.Cursor, f.col = s.lines, s.cursor, s.col
	f.sel.Clear()
	f.dirty = true
	f.reindex()
}

// change records an undo step; call it before modifying the text.
func (f *fileView) change() {
	f.undo = append(f.undo, f.snap())
	if len(f.undo) > maxUndo {
		f.undo = f.undo[1:]
	}
	f.redo = nil
	f.dirty = true
}

// touch is change for insert mode, where everything typed until esc is one
// undo step.
func (f *fileView) touch() {
	if f.fresh || !f.insert {
		f.change()
		f.fresh = false
	}
}

func (f *fileView) undoStep() bool {
	if len(f.undo) == 0 {
		return false
	}
	f.redo = append(f.redo, f.snap())
	f.restore(f.undo[len(f.undo)-1])
	f.undo = f.undo[:len(f.undo)-1]
	return true
}

func (f *fileView) redoStep() bool {
	if len(f.redo) == 0 {
		return false
	}
	f.undo = append(f.undo, f.snap())
	f.restore(f.redo[len(f.redo)-1])
	f.redo = f.redo[:len(f.redo)-1]
	return true
}

// startInsert enters insert mode with the cursor at col.
func (f *fileView) startInsert(col int) {
	f.sel.Clear()
	f.insert, f.fresh = true, true
	f.col = col
	f.clampCol()
}

// stopInsert returns to normal mode; like vim the cursor steps back onto
// the last character typed.
func (f *fileView) stopInsert() {
	f.insert = false
	f.col--
	f.clampCol()
}

// splice replaces runes [from, to) of the current line with ins and
// returns what was there.
func (f *fileView) splice(from, to int, ins string) string {
	r := []rune(f.line())
	from = max(0, min(from, len(r)))
	to = max(from, min(to, len(r)))
	f.lines[f.Cursor] = string(r[:from]) + ins + string(r[to:])
	return string(r[from:to])
}

// insertText puts s, which may span lines, at the cursor and moves the
// cursor behind it.
func (f *fileView) insertText(s string) {
	f.touch()
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	r := []rune(f.line())
	f.col = max(0, min(f.col, len(r)))
	before, after := string(r[:f.col]), string(r[f.col:])
	parts := strings.Split(s, "\n")
	last := len(parts) - 1
	f.col = utf8.RuneCountInString(parts[last])
	if last == 0 {
		f.col += utf8.RuneCountInString(before)
	}
	parts[0] = before + parts[0]
	parts[last] += after
	f.lines = append(f.lines[:f.Cursor], append(parts, f.lines[f.Cursor+1:]...)...)
	f.Cursor += last
	f.reindex()
}

// indent is the leading white space of the current line.
func (f *fileView) indent() string {
	return f.line()[:len(f.line())-len(strings.TrimLeftFunc(f.line(), unicode.IsSpace))]
}

// newline breaks the line at the cursor, keeping its indentation.
func (f *fileView) newline() {
	indent := f.indent()
	if utf8.RuneCountInString(indent) > f.col {
		indent = ""
	}
	f.insertText("\n" + indent)
}

// backspace deletes the character before the cursor, joining lines at the
// start of one.
func (f *fileView) backspace() {
	switch {
	case f.col > 0:
		f.touch()
		f.splice(f.col-1, f.col, "")
		f.col--
	case f.Cursor > 0:
		f.touch()
		f.Cursor--
		f.col = f.lineLen()
		f.lines[f.Cursor] += f.lines[f.Cursor+1]
		f.lines = append(f.lines[:f.Cursor+1], f.lines[f.Cursor+2:]...)
	}
	f.reindex()
}

// deleteChars removes n characters from the cursor on (x), or up to the end
// of the line with n < 0 (D).
func (f *fileView) deleteChars(n int) {
	if f.lineLen() == 0 {
		return
	}
	if n < 0 {
		n = f.lineLen()
	}
	f.touch()
	f.reg, f.regLine = f.splice(f.col, f.col+n, ""), false
	f.clampCol()
	f.reindex()
}

// deleteLines removes lines [from, to] into the register.
func (f *fileView) deleteLines(from, to int) {
	f.touch()
	to = min(to, len(f.lines)-1)
	f.reg, f.regLine = strings.Join(f.lines[from:to+1], "\n"), true
	f.lines = append(f.lines[:from], f.lines[to+1:]...)
	if len(f.lines) == 0 {
		f.lines = []string{""}
	}
	f.Cursor = from
	f.Clamp(len(f.lines))
	f.clampCol()
	f.reindex()
}

// yankLines copies n lines from the cursor into the register.
func (f *fileView) yankLines(n int) string {
	to := min(f.Cursor+n, len(f.lines))
	f.reg, f.regLine = strings.Join(f.lines[f.Cursor:to], "\n"), true
	return f.reg
}

// takeSelection copies the selection into the register and, with del,
// removes it from the text.
func (f *fileView) takeSelection(del bool) string {
	from, to := f.sel.Bounds(f.pos(), f.lines)
	text, lineMode := f.sel.Text(f.pos(), f.lines), f.sel.Mode == visual.Line
	f.sel.Clear()
	switch {
	case !del:
		f.Cursor, f.col = from.Line, from.Col
		if lineMode {
			f.col = 0
		}
		f.clampCol()
	case lineMode:
		f.deleteLines(from.Line, to.Line)
	default:
		f.touch()
		head := []rune(f.lines[from.Line])
		tail := []rune(f.lines[to.Line])
		f.lines[from.Line] = string(head[:min(from.Col, len(head))]) + string(tail[min(to.Col+1, len(tail)):])
		f.lines = append(f.lines[:from.Line+1], f.lines[to.Line+1:]...)
		f.Cursor, f.col = from.Line, from.Col
		f.clampCol()
		f.reindex()
	}
	f.reg, f.regLine = text, lineMode
	return text
}

// paste inserts the register after the cursor (p) or before it (P).
func (f *fileView) paste(before bool) {
	if f.reg == "" {
		return
	}
	if f.regLine {
		f.touch()
		at := f.Cursor + 1
		if before {
			at = f.Cursor
		}
		ins := strings.Split(f.reg, "\n")
		f.lines = append(f.lines[:at], append(ins, f.lines[at:]...)...)
		f.Cursor, f.col = at, 0
		f.reindex()
		return
	}
	if !before && f.lineLen() > 0 {
		f.col++
	}
	f.insertText(f.reg)
	f.col--
	f.clampCol()
}

// join appends the next line to the current one with a single space (J).
func (f *fileView) join() {
	if f.Cursor >= len(f.lines)-1 {
		return
	}
	f.touch()
	head := strings.TrimRightFunc(f.line(), unicode.IsSpace)
	f.col = utf8.RuneCountInString(head)
	f.lines[f.Cursor] = head + " " + strings.TrimLeftFunc(f.lines[f.Cursor+1], unicode.IsSpace)
	f.lines = append(f.lines[:f.Cursor+1], f.lines[f.Cursor+2:]...)
	f.reindex()
}

// openLine starts a new, equally indented line below or above the cursor
// and enters insert mode on it.
func (f *fileView) openLine(above bool) {
	f.change()
	indent := f.indent()
	at := f.Cursor + 1
	if above {
		at = f.Cursor
	}
	f.lines = append(f.lines[:at], append([]string{indent}, f.lines[at:]...)...)
	f.Cursor = at
	f.reindex()
	f.startInsert(utf8.RuneCountInString(indent))
	f.fresh = false // the new line is already this session's undo step
}

// changeLines replaces lines [from, to] with one empty line indented like
// the first of them, and enters insert mode on it (cc, V c).
func (f *fileView) changeLines(from, to int) {
	f.Cursor = from
	indent := f.indent()
	f.deleteLines(from, to) // the undo step
	if len(f.lines) == 1 && f.lines[0] == "" {
		f.lines[0] = indent
	} else {
		f.lines = append(f.lines[:from], append([]string{indent}, f.lines[from:]...)...)
	}
	f.Cursor = from
	f.reindex()
	f.startInsert(utf8.RuneCountInString(indent))
	f.fresh = false
}

// ---- drawing ----

// render draws h rows, w cells wide: marker, line number, text.
func (f *fileView) render(w, h int) string {
	f.Scroll(len(f.lines), h)
	numW := len(strconv.Itoa(len(f.lines)))
	textW := max(1, w-numW-3)
	f.left = min(f.left, f.col)
	if f.col >= f.left+textW {
		f.left = f.col - textW + 1
	}
	from, to, hasStmt := f.statement()
	if f.sel.Active() {
		hasStmt = false // the selection is what runs
	}

	var b strings.Builder
	for i := f.Top; i < f.Top+h; i++ {
		if i >= len(f.lines) {
			b.WriteString(sMuted.Render("~") + "\n")
			continue
		}
		marker := " "
		switch {
		case i == f.Cursor:
			marker = sCrumb.Render("▌")
		case hasStmt && i >= from && i <= to:
			marker = sCrumb.Render("│")
		}
		num := sMuted
		if i == f.Cursor {
			num = sCrumb
		}
		b.WriteString(marker + num.Render(padLeft(strconv.Itoa(i+1), numW)) + "  " + f.renderLine(i, textW))
		b.WriteByte('\n')
	}
	return b.String()
}

// renderLine styles one line: comments muted, selection and cursor marked.
func (f *fileView) renderLine(i, w int) string {
	r := []rune(clean(f.lines[i], false))
	plain := func(s string) string { return s }
	if strings.HasPrefix(strings.TrimSpace(f.lines[i]), "--") {
		plain = func(s string) string { return sMuted.Render(s) }
	}
	lineSel := f.sel.Mode == visual.Line && f.sel.Contains(f.pos(), f.lines, i, 0)
	// room for the cursor after the last character, and on empty lines
	if (i == f.Cursor && f.col >= len(r)) || len(r) == 0 {
		r = append(r, ' ')
	}
	found := map[int]bool{}
	for _, c := range matchCols(f.lines[i], f.pattern) {
		for j := range []rune(f.pattern) {
			found[c+j] = true
		}
	}
	var b strings.Builder
	run, kind := "", 0 // 0 plain, 1 selected, 2 cursor, 3 search match
	flush := func() {
		switch kind {
		case 1:
			b.WriteString(sSel.Render(run))
		case 2:
			b.WriteString(sCell.Render(run))
		case 3:
			b.WriteString(sFound.Render(run))
		default:
			b.WriteString(plain(run))
		}
		run = ""
	}
	end := min(len(r), f.left+w)
	for j := f.left; j < end; j++ {
		k := 0
		switch {
		case i == f.Cursor && j == f.col:
			k = 2
		case lineSel || f.sel.Contains(f.pos(), f.lines, i, j):
			k = 1
		case found[j]:
			k = 3
		}
		if k != kind {
			flush()
			kind = k
		}
		run += string(r[j])
	}
	flush()
	return ansi.Truncate(b.String(), w, "")
}
