package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/Gandalfalex/vim-kit/cmdline"
	"github.com/Gandalfalex/vim-kit/keys"
	"github.com/Gandalfalex/vim-kit/visual"

	"dbreader/internal/config"
)

// Editing actions of the script screen; the text operations themselves are
// on fileView.
const (
	actInsert      keys.Action = "insert"
	actAppend      keys.Action = "append"
	actInsertBOL   keys.Action = "insert_line_start"
	actAppendEOL   keys.Action = "append_line_end"
	actOpenBelow   keys.Action = "open_below"
	actOpenAbove   keys.Action = "open_above"
	actDelChar     keys.Action = "delete_char"
	actDelLine     keys.Action = "delete_line"
	actDelEOL      keys.Action = "delete_to_end"
	actChangeEOL   keys.Action = "change_to_end"
	actChangeLine  keys.Action = "change_line"
	actYankLine    keys.Action = "yank_line"
	actPaste       keys.Action = "paste"
	actPasteBefore keys.Action = "paste_before"
	actUndo        keys.Action = "undo"
	actRedo        keys.Action = "redo"
	actJoin        keys.Action = "join"
	actDelete      keys.Action = "delete"
	actChange      keys.Action = "change"
)

var editBindings = keys.Bindings{
	actInsert:      {"i"},
	actAppend:      {"a"},
	actInsertBOL:   {"I"},
	actAppendEOL:   {"A"},
	actOpenBelow:   {"o"},
	actOpenAbove:   {"O"},
	actDelChar:     {"x"},
	actDelLine:     {"dd"},
	actDelEOL:      {"D"},
	actChangeEOL:   {"C"},
	actChangeLine:  {"cc"},
	actYankLine:    {"yy"},
	actPaste:       {"p"},
	actPasteBefore: {"P"},
	actUndo:        {"u"},
	actRedo:        {"ctrl+r"},
	actJoin:        {"J"},
}

// edit adapts a text operation; the screen scrolls to the cursor after it.
func edit(fn func(f *fileView, ev keys.Event)) keyFn {
	return func(m *model, ev keys.Event) (tea.Model, tea.Cmd) {
		fn(&m.script, ev)
		return m, nil
	}
}

var editActions = map[keys.Action]keyFn{
	actInsert:    edit(func(f *fileView, _ keys.Event) { f.startInsert(f.col) }),
	actAppend:    edit(func(f *fileView, _ keys.Event) { f.startInsert(min(f.col+1, f.lineLen())) }),
	actInsertBOL: edit(func(f *fileView, _ keys.Event) { f.startInsert(utf8.RuneCountInString(f.indent())) }),
	actAppendEOL: edit(func(f *fileView, _ keys.Event) { f.startInsert(f.lineLen()) }),
	actOpenBelow: edit(func(f *fileView, _ keys.Event) { f.openLine(false) }),
	actOpenAbove: edit(func(f *fileView, _ keys.Event) { f.openLine(true) }),
	actDelChar:   edit(func(f *fileView, ev keys.Event) { f.deleteChars(ev.Count) }),
	actDelEOL:    edit(func(f *fileView, _ keys.Event) { f.deleteChars(-1) }),
	actDelLine:   edit(func(f *fileView, ev keys.Event) { f.deleteLines(f.Cursor, f.Cursor+ev.Count-1) }),
	actChangeEOL: edit(func(f *fileView, _ keys.Event) {
		f.deleteChars(-1)
		f.startInsert(f.lineLen())
		f.fresh = false
	}),
	actChangeLine: edit(func(f *fileView, ev keys.Event) { f.changeLines(f.Cursor, f.Cursor+ev.Count-1) }),
	actYankLine: func(m *model, ev keys.Event) (tea.Model, tea.Cmd) {
		return m, yank(m.script.yankLines(ev.Count), "line")
	},
	actPaste:       edit(func(f *fileView, _ keys.Event) { f.paste(false) }),
	actPasteBefore: edit(func(f *fileView, _ keys.Event) { f.paste(true) }),
	actJoin:        edit(func(f *fileView, _ keys.Event) { f.join() }),
	actUndo: do(func(m *model, _ keys.Event) {
		if !m.script.undoStep() {
			m.setStatus("already at oldest change", false)
		}
	}),
	actRedo: do(func(m *model, _ keys.Event) {
		if !m.script.redoStep() {
			m.setStatus("already at newest change", false)
		}
	}),
	actEsc: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		return m, nil // nothing to leave: q or - go back
	},
	actBack:       cmd((*model).back),
	actFilter:     func(m *model, _ keys.Event) (tea.Model, tea.Cmd) { return m.openFind() },
	actNext:       do(func(m *model, _ keys.Event) { m.findNext(1) }),
	actPrev:       do(func(m *model, _ keys.Event) { m.findNext(-1) }),
	actVisual:     do(func(m *model, _ keys.Event) { m.script.sel.Toggle(visual.Char, m.script.pos()) }),
	actVisualLine: do(func(m *model, _ keys.Event) { m.script.sel.Toggle(visual.Line, m.script.pos()) }),
}

// visualActions apply to the selection.
var visualActions = map[keys.Action]keyFn{
	actEsc:        do(func(m *model, _ keys.Event) { m.script.sel.Clear() }),
	actVisual:     editActions[actVisual],
	actVisualLine: editActions[actVisualLine],
	actFilter:     editActions[actFilter],
	actNext:       editActions[actNext],
	actPrev:       editActions[actPrev],
	actYank: func(m *model, _ keys.Event) (tea.Model, tea.Cmd) {
		return m, yank(m.script.takeSelection(false), "selection")
	},
	actDelete: edit(func(f *fileView, _ keys.Event) { f.takeSelection(true) }),
	actChange: edit(func(f *fileView, _ keys.Event) {
		if f.sel.Mode == visual.Line {
			from, to := f.sel.Bounds(f.pos(), f.lines)
			f.sel.Clear()
			f.changeLines(from.Line, to.Line)
			return
		}
		f.takeSelection(true)
		f.startInsert(f.col)
		f.fresh = false
	}),
}

// handleInsertKey is insert mode: text goes into the script, tab cycles the
// completion popup, esc returns to normal mode.
func (m *model) handleInsertKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := &m.script
	complete := false // recompute candidates after this key
	switch k := msg.String(); k {
	case "esc", "ctrl+[":
		f.stopInsert()
	case "enter":
		f.newline()
	case "backspace":
		f.backspace()
		complete = true
	case "delete":
		if f.col < f.lineLen() {
			f.touch()
			f.splice(f.col, f.col+1, "")
			f.reindex()
		}
	case "left":
		f.col = max(0, f.col-1)
	case "right":
		f.col = min(f.lineLen(), f.col+1)
	case "up", "down":
		d := 1
		if k == "up" {
			d = -1
		}
		f.Move(d, len(f.lines))
		f.clampCol()
	case "home":
		f.col = 0
	case "end":
		f.col = f.lineLen()
	case "tab", "ctrl+n", "shift+tab", "ctrl+p":
		dir := 1
		if k == "shift+tab" || k == "ctrl+p" {
			dir = -1
		}
		if word, ok := m.edCompl.Cycle(dir); ok {
			f.touch()
			f.splice(m.edStart, f.col, word)
			f.col = m.edStart + utf8.RuneCountInString(word)
			f.reindex()
			return m, nil // keep the popup while cycling
		}
		if k == "tab" {
			f.insertText("  ")
		}
	default:
		if msg.Text == "" || msg.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
			return m, nil
		}
		f.insertText(msg.Text)
		complete = true
	}
	m.edCompl.SetItems(nil)
	if complete {
		m.completeScript()
	}
	return m, nil
}

// completeScript fills the popup for the word being typed in the script.
func (m *model) completeScript() {
	f := &m.script
	text, cursor := f.context()
	words, start := completeWord(text, cursor, m.sqlCatalog())
	items := make([]cmdline.Item, len(words))
	copy(items, words)
	m.edCompl.SetItems(items)
	m.edStart = f.col - (cursor - start)
}

// saveScript writes the script on screen to its file. It refuses to
// overwrite a file that changed on disk since it was read, unless forced.
func (m *model) saveScript(force bool) bool {
	f, path := &m.script, m.scriptFile.Path
	info, err := os.Stat(path)
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		m.fail("save", err)
		return false
	case err == nil && !force && !info.ModTime().Equal(f.mtime):
		m.setStatus(m.scriptFile.Name+" changed on disk since it was opened: :w! overwrites, :e! reloads", true)
		return false
	}
	if err := writeFile(path, f.text()); err != nil {
		m.fail("save", err)
		return false
	}
	if info, err := os.Stat(path); err == nil {
		f.mtime = info.ModTime()
	}
	f.dirty = false
	m.setStatus(fmt.Sprintf("%s written, %d lines", m.scriptFile.Name, len(f.lines)), false)
	return true
}

// keepScript saves unsaved work when the script is being left, so closing
// or quitting never loses text. If the file changed on disk meanwhile, the
// work goes to a file beside it instead of over it.
func (m *model) keepScript() {
	f := &m.script
	if !f.dirty {
		return
	}
	if m.saveScript(false) {
		slog.Info("script saved on leaving", "path", m.scriptFile.Path)
		return
	}
	rescue := m.scriptFile.Path + ".unsaved"
	if err := writeFile(rescue, f.text()); err != nil {
		m.fail("save "+m.scriptFile.Name, err)
		return
	}
	f.dirty = false
	m.fail("save "+m.scriptFile.Name, fmt.Errorf("the file changed on disk; your version was written to %s", rescue))
}

// writeFile replaces path atomically, keeping the mode of an existing file.
func writeFile(path, text string) error {
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dbreader-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// newScript creates an empty script for the open connection and opens it
// in insert mode. An existing script of that name is opened instead.
func (m *model) newScript(name string) tea.Cmd {
	if !m.needConn() {
		return nil
	}
	dir := m.conn.Profile.ScriptDir
	switch {
	case name == "":
		m.setStatus("usage: :new <name>", true)
		return nil
	case dir == "":
		m.setStatus("this connection has no script directory", true)
		return nil
	case name != filepath.Base(name) || strings.HasPrefix(name, "."):
		m.setStatus("script name must be a plain file name", true)
		return nil
	}
	if filepath.Ext(name) == "" {
		name += ".sql"
	}
	s := config.Script{Name: name, Path: filepath.Join(dir, name)}
	if old, ok := m.findScript(s.Path); ok {
		m.openScript(old)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.fail("new script", err)
		return nil
	}
	file, err := os.OpenFile(s.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		m.fail("new script", err)
		return nil
	}
	file.Close()

	// known to this session and, through the directory, to the next
	m.conn.Profile.Scripts = append(m.conn.Profile.Scripts, s)
	for i := range m.settings.Profiles {
		if p := &m.settings.Profiles[i]; p.Name == m.conn.Profile.Name {
			p.Scripts = append(p.Scripts, s)
		}
	}
	m.openScript(s)
	if m.top() == scrScript && m.scriptFile.Path == s.Path {
		m.script.startInsert(0)
		m.setStatus("new script "+s.Path, false)
	}
	return nil
}
