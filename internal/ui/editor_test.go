package ui

import (
	"dbreader/internal/driver/sqlite"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dbreader/internal/config"

	tk "github.com/Gandalfalex/tuikit"
)

// editorHarness is connected and has a script directory to write to.
func editorHarness(t *testing.T) (*harness, string) {
	t.Helper()
	t.Setenv("VISUAL", "") // :new uses the built-in editor without one
	t.Setenv("EDITOR", "")
	dir := filepath.Join(t.TempDir(), "scripts")
	h := newHarness(t, func(p *config.Profile) { p.ScriptDir = dir })
	h.connect()
	return h, dir
}

func (h *harness) buffer() string { return strings.Join(h.m.script.lines, "\n") }

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWriteNewScript(t *testing.T) {
	h, dir := editorHarness(t)
	h.command("new report")
	path := filepath.Join(dir, "report.sql")
	if h.m.Stack.Top() != scrScript || !h.m.script.insert || h.m.scriptFile.Path != path {
		t.Fatalf("top = %v, insert = %v, status = %q", h.m.Stack.Top(), h.m.script.insert, h.m.Status)
	}

	// typing with completion: table after FROM, column through the alias
	h.typeText("select u.na")
	if len(h.m.edCompl.Items) != 0 {
		t.Errorf("candidates before the alias is known: %v", h.m.edCompl.Items)
	}
	h.typeText(" from us")
	if !strings.Contains(h.screen(), "users") {
		t.Errorf("no popup for the table:\n%s", h.screen())
	}
	h.press("tab")
	h.typeText(" u")
	h.press("enter")
	h.typeText("  where u.a")
	h.press("tab")
	h.typeText(" > 10;")
	h.press("esc")
	if got, want := h.buffer(), "select u.na from users u\n  where u.age > 10;"; got != want {
		t.Fatalf("buffer = %q, want %q", got, want)
	}
	if !h.m.script.dirty || !strings.Contains(h.screen(), "report.sql [+]") {
		t.Errorf("not marked as modified:\n%s", h.screen())
	}

	// fix the column with normal-mode edits: back to line 1, onto "na"
	h.press("g", "g", "0", "w", "w", "w", "l", "a")
	h.typeText("me")
	h.press("esc")
	if got := h.m.script.lines[0]; got != "select u.name from users u" {
		t.Fatalf("line 1 = %q", got)
	}

	h.command("run")
	if h.m.Stack.Top() != scrData || len(h.m.grid.res.Rows) != 1 || h.m.grid.res.Rows[0][0].S != "ada" {
		t.Fatalf("run: top = %v, status = %q", h.m.Stack.Top(), h.m.Status)
	}
	if got := readFile(t, path); got != "" {
		t.Errorf("file written before :w: %q", got)
	}
	h.press("q")
	h.command("w")
	if got, want := readFile(t, path), "select u.name from users u\n  where u.age > 10;\n"; got != want || h.m.script.dirty {
		t.Fatalf("file = %q, dirty = %v, status = %q", got, h.m.script.dirty, h.m.Status)
	}

	// the script is listed for the connection now and next time
	h.press("S")
	if len(h.m.scripts.All) != 1 || h.m.scripts.All[0].Name != "report.sql" {
		t.Errorf("scripts = %+v", h.m.scripts.All)
	}
	p := config.Profile{Name: "testdb"}
	p.UseScriptDir(dir)
	if len(p.Scripts) != 1 || p.Scripts[0].Path != path {
		t.Errorf("scripts found in the directory = %+v", p.Scripts)
	}
}

func TestScriptSavedOnLeaving(t *testing.T) {
	for name, leave := range map[string]func(h *harness){
		"back":       func(h *harness) { h.press("q") },
		"scripts":    func(h *harness) { h.press("S") },
		"reconnect":  func(h *harness) { h.command("connect testdb") },
		"other file": func(h *harness) { h.command("new second") },
		"quit":       func(h *harness) { h.m.shutdown(); h.m.conn = nil },
	} {
		h, dir := editorHarness(t)
		h.command("new keep")
		h.typeText("select 1;")
		h.press("esc")
		leave(h)
		if got := readFile(t, filepath.Join(dir, "keep.sql")); got != "select 1;\n" {
			t.Errorf("%s: file = %q, status = %q", name, got, h.m.Status)
		}
	}
}

func TestScriptChangedOnDisk(t *testing.T) {
	h, dir := editorHarness(t)
	h.command("new shared")
	h.typeText("select 1;")
	h.press("esc")
	path := filepath.Join(dir, "shared.sql")
	// someone else (DBeaver) writes the file meanwhile
	if err := os.WriteFile(path, []byte("select 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour))

	h.command("w")
	if !h.m.StatusErr || readFile(t, path) != "select 2;\n" || !h.m.script.dirty {
		t.Fatalf(":w overwrote a changed file: status = %q", h.m.Status)
	}
	h.press("q") // leaving must not overwrite it either, nor drop the text
	if readFile(t, path) != "select 2;\n" || readFile(t, path+".unsaved") != "select 1;\n" {
		t.Fatalf("file = %q, rescue = %q", readFile(t, path), readFile(t, path+".unsaved"))
	}

	h.command("script shared.sql")
	h.press("x")
	os.Chtimes(path, time.Now().Add(2*time.Hour), time.Now().Add(2*time.Hour))
	h.command("w")
	if !h.m.StatusErr {
		t.Fatalf("second outside change not noticed: %q", h.m.Status)
	}
	h.command("w!")
	if got := readFile(t, path); got != "elect 2;\n" {
		t.Fatalf("after :w!: %q", got)
	}
	h.press("x")
	h.command("reload")
	if !h.m.StatusErr || h.buffer() != "lect 2;" {
		t.Errorf(":reload dropped unsaved edits: %q, status = %q", h.buffer(), h.m.Status)
	}
	h.command("e!")
	if h.buffer() != "elect 2;" || h.m.script.dirty {
		t.Errorf("after :e!: %q", h.buffer())
	}
}

// keysOn feeds normal-mode keys to a buffer through the real key handling.
func keysOn(t *testing.T, text string, keys ...string) *harness {
	t.Helper()
	h, _ := editorHarness(t)
	h.command("new scratch")
	h.press("esc")
	h.m.script = newFileView(text, sqlite.SQL)
	h.m.script.mtime = time.Time{}
	h.press(keys...)
	return h
}

func TestEditing(t *testing.T) {
	const text = "select 1;\nselect 2;\n  select 3;\n"
	tests := []struct {
		name string
		keys string // space separated; "_" is a space, <...> a named key
		want string
	}{
		{"x", "x x", "lect 1;\nselect 2;\n  select 3;"},
		{"count x", "3 x", "ect 1;\nselect 2;\n  select 3;"},
		{"dd", "j d d", "select 1;\n  select 3;"},
		{"count dd", "2 d d", "  select 3;"},
		{"dd all", "3 d d", ""},
		{"D", "w D", "select \nselect 2;\n  select 3;"},
		{"C", "w C x <esc>", "select x\nselect 2;\n  select 3;"},
		{"cc keeps indent", "G c c y <esc>", "select 1;\nselect 2;\n  y"},
		{"yy p", "y y j p", "select 1;\nselect 2;\nselect 1;\n  select 3;"},
		{"yy P", "j y y g g P", "select 2;\nselect 1;\nselect 2;\n  select 3;"},
		{"x p swaps", "x p", "eslect 1;\nselect 2;\n  select 3;"},
		{"o keeps indent", "G o x <esc>", "select 1;\nselect 2;\n  select 3;\n  x"},
		{"O", "O x <esc>", "x\nselect 1;\nselect 2;\n  select 3;"},
		{"i", "w i a _ <esc>", "select a 1;\nselect 2;\n  select 3;"},
		{"a", "$ a x <esc>", "select 1;x\nselect 2;\n  select 3;"},
		{"A", "A x <esc>", "select 1;x\nselect 2;\n  select 3;"},
		{"I", "G $ I x <esc>", "select 1;\nselect 2;\n  xselect 3;"},
		{"enter splits", "w i <enter> <esc>", "select \n1;\nselect 2;\n  select 3;"},
		{"backspace joins", "j i <backspace> <esc>", "select 1;select 2;\n  select 3;"},
		{"J", "J", "select 1; select 2;\n  select 3;"},
		{"u", "x d d u", "elect 1;\nselect 2;\n  select 3;"},
		{"u u", "x d d u u", text[:len(text)-1]},
		{"redo", "x u <ctrl+r>", "elect 1;\nselect 2;\n  select 3;"},
		{"insert is one undo", "i a b c <esc> u", text[:len(text)-1]},
		{"v d", "w v l d", "select \nselect 2;\n  select 3;"},
		{"v d across lines", "w v j d", "select ;\n  select 3;"},
		{"V d", "V j d", "  select 3;"},
		{"v y p", "v e y $ p", "select 1;select\nselect 2;\n  select 3;"},
		{"v c", "v e c x <esc>", "x 1;\nselect 2;\n  select 3;"},
		{"V c", "j V c x <esc>", "select 1;\nx\n  select 3;"},
		{"tab indents", "i <tab> <esc>", "  select 1;\nselect 2;\n  select 3;"},
	}
	for _, tt := range tests {
		var keys []string
		for _, k := range strings.Fields(tt.keys) {
			switch {
			case k == "_":
				k = " "
			case strings.HasPrefix(k, "<"):
				k = strings.Trim(k, "<>")
			}
			keys = append(keys, k)
		}
		h := keysOn(t, text, keys...)
		if got := h.buffer(); got != tt.want {
			t.Errorf("%s (%s): buffer = %q, want %q", tt.name, tt.keys, got, tt.want)
		}
		f := &h.m.script
		if f.insert || f.Cursor >= len(f.lines) || f.col > f.lineLen() {
			t.Errorf("%s: left in a bad state: insert = %v, cursor = %d:%d", tt.name, f.insert, f.Cursor, f.col)
		}
	}
}

func TestPasteIntoScript(t *testing.T) {
	h := keysOn(t, "a\n", "A")
	h.m.Update(teaPaste(" b\r\nc"))
	h.press("esc")
	if got := h.buffer(); got != "a b\nc" {
		t.Errorf("buffer = %q", got)
	}
}

func TestExternalEditor(t *testing.T) {
	h, dir := editorHarness(t)
	h.command("new ext")
	path := filepath.Join(dir, "ext.sql")
	h.typeText("select 1;")
	h.press("esc")

	// e saves the buffer before handing the file to the editor
	h.press("e")
	if got := readFile(t, path); got != "select 1;\n" || h.m.script.dirty {
		t.Fatalf("file = %q, dirty = %v", got, h.m.script.dirty)
	}

	// what the editor left: the file is read again, the cursor follows vim
	if err := os.WriteFile(path, []byte("select 1;\n\nselect name\n  from users;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.m.update(tk.EditedMsg{Path: path, Line: 4, Col: 5})
	if h.m.Stack.Top() != scrScript || len(h.m.script.lines) != 4 || h.m.script.Cursor != 3 || h.m.script.col != 4 {
		t.Fatalf("top = %v, lines = %q, cursor = %d:%d", h.m.Stack.Top(), h.m.script.lines, h.m.script.Cursor, h.m.script.col)
	}

	// enter runs the statement under the cursor
	h.press("enter")
	if h.m.Stack.Top() != scrData || h.m.grid.res == nil || h.m.grid.res.Columns[0] != "name" {
		t.Fatalf("enter: top = %v, status = %q", h.m.Stack.Top(), h.m.Status)
	}
}
