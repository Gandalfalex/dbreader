package ui

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"dbreader/internal/config"
)

// harness drives a model the way the Bubble Tea runtime would, minus the
// terminal: keys go in, commands are run and their messages fed back.
type harness struct {
	t *testing.T
	m *model
}

func newHarness(t *testing.T, mod func(*config.Profile)) *harness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, age INT);
		INSERT INTO users (name, age) VALUES ('ada', 36), ('bob', NULL), ('cy', 9);
		CREATE TABLE logs (line TEXT);
	`); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	p := config.Profile{Name: "testdb", Driver: config.SQLite, Path: path, Limit: 500}
	if mod != nil {
		mod(&p)
	}
	// a second profile keeps the model from auto-connecting in Init
	s := &config.Settings{Profiles: []config.Profile{p, {Name: "other", Driver: config.SQLite, Path: path}}}
	h := &harness{t: t, m: newModel(s, "")}
	t.Cleanup(h.m.shutdown)
	h.m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return h
}

// pump runs cmd and everything it leads to. Timers (spinner, cursor blink)
// are abandoned after a moment instead of being waited for.
func (h *harness) pump(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(3 * time.Second):
		return
	}
	if msg == nil {
		return
	}
	// batches and sequences are slices of commands
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		for i := range v.Len() {
			h.pump(v.Index(i).Interface().(tea.Cmd))
		}
		return
	}
	switch reflect.TypeOf(msg).String() {
	case "spinner.TickMsg", "cursor.BlinkMsg", "cursor.initialBlinkMsg":
		return
	}
	_, next := h.m.Update(msg)
	h.pump(next)
}

var named = map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "backspace": tea.KeyBackspace, "tab": tea.KeyTab}

func teaPaste(s string) tea.Msg { return tea.PasteMsg{Content: s} }

// press sends keys; multi-character names are special keys. Only commands
// that do work are pumped, so opening a prompt does not wait on its cursor.
func (h *harness) press(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		msg := tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
		if code, ok := named[k]; ok {
			msg = tea.KeyPressMsg{Code: code}
		} else if rest, ok := strings.CutPrefix(k, "ctrl+"); ok {
			msg = tea.KeyPressMsg{Code: []rune(rest)[0], Mod: tea.ModCtrl}
		}
		_, cmd := h.m.Update(msg)
		if h.m.loading {
			h.pump(cmd)
		}
	}
}

func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		h.press(string(r))
	}
}

func (h *harness) sql(stmt string) {
	h.t.Helper()
	h.press("s")
	if h.m.mode != inputSQL {
		h.t.Fatalf("sql prompt did not open (status %q)", h.m.status)
	}
	h.typeText(stmt)
	h.press("enter")
}

func (h *harness) count() string {
	h.t.Helper()
	res, err := h.m.conn.Query(h.t.Context(), "SELECT count(*) FROM users", 1)
	if err != nil {
		h.t.Fatal(err)
	}
	return res.Rows[0][0].S
}

func (h *harness) screen() string { return ansi.Strip(h.m.render()) }

func (h *harness) connect() {
	h.t.Helper()
	h.press("enter")
	if h.m.conn == nil || h.m.top() != scrTables {
		h.t.Fatalf("not on the table list: top = %v, status = %q", h.m.top(), h.m.status)
	}
}

func TestBrowse(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	if got := len(h.m.tables.all); got != 2 {
		t.Fatalf("tables = %d, want 2", got)
	}
	h.press("j", "enter") // logs, users
	if h.m.top() != scrData || h.m.grid.browse == nil || h.m.grid.browse.Table != "users" {
		t.Fatalf("top = %v, grid = %+v", h.m.top(), h.m.grid.browse)
	}
	out := h.screen()
	for _, want := range []string{"ada", "bob", "NULL", "testdb", "users"} {
		if !strings.Contains(out, want) {
			t.Errorf("screen lacks %q:\n%s", want, out)
		}
	}

	h.press("l", "l", "o", "o") // sort by age, descending
	if b := h.m.grid.browse; b.Order != "age" || !b.Desc {
		t.Errorf("browse = %+v, want order by age desc", b)
	}
	if first := h.m.grid.res.Rows[0][1].S; first != "ada" {
		t.Errorf("first row = %s, want ada (age 36)", first)
	}

	h.press("enter")
	if h.m.top() != scrRow || !strings.Contains(h.screen(), "ada") {
		t.Errorf("record view missing:\n%s", h.screen())
	}
	h.press("q", "q", "h")
	if h.m.top() != scrConns {
		t.Errorf("top = %v after going back, want the connection list", h.m.top())
	}
}

func TestReadRunsWithoutAsking(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	h.sql("select name from users where age > 10")
	if h.m.guarding() {
		t.Fatal("a SELECT was held for confirmation")
	}
	if h.m.top() != scrData || len(h.m.grid.res.Rows) != 1 {
		t.Fatalf("top = %v, status = %q", h.m.top(), h.m.status)
	}
}

func TestWriteNeedsConfirmAndCommit(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()

	// nothing but the exact word runs the statement
	for _, typed := range []string{"", "y", "Yes", "yes please", "testdb"} {
		h.sql("delete from users where id = 1")
		if h.m.confirm == nil || h.m.confirm.want != "yes" {
			t.Fatalf("no confirmation asked: %+v", h.m.confirm)
		}
		if !strings.Contains(h.screen(), "delete from users where id = 1") {
			t.Errorf("confirmation does not show the statement:\n%s", h.screen())
		}
		h.typeText(typed)
		h.press("enter")
		if h.m.guarding() || h.count() != "3" {
			t.Fatalf("typed %q: guarding = %v, count = %s", typed, h.guarding(), h.count())
		}
	}

	h.sql("delete from users where id = 1")
	h.press("esc")
	if h.m.guarding() || h.count() != "3" {
		t.Fatalf("esc did not cancel: count = %s", h.count())
	}

	// confirmed: executed, but not committed
	h.sql("delete from users where id = 1")
	h.typeText("yes")
	h.press("enter")
	if h.m.pending == nil || h.m.pending.Affected != 1 {
		t.Fatalf("pending = %+v, status = %q", h.m.pending, h.m.status)
	}
	if out := h.screen(); !strings.Contains(out, "NOT COMMITTED") || !strings.Contains(out, "1 row affected") {
		t.Errorf("commit screen:\n%s", out)
	}
	// other keys do nothing while the transaction is open
	h.press("q", "j", "enter", "esc", ":", "s", "y")
	if h.m.pending == nil || h.m.mode != inputNone {
		t.Fatalf("a key escaped the open transaction: pending = %v, mode = %v", h.m.pending, h.m.mode)
	}
	h.press("r")
	if h.m.pending != nil || h.count() != "3" {
		t.Fatalf("rollback: pending = %v, count = %s", h.m.pending, h.count())
	}

	h.sql("delete from users where id = 1")
	h.typeText("yes")
	h.press("enter", "c")
	if h.m.pending != nil || h.count() != "2" {
		t.Fatalf("commit: pending = %v, count = %s, status = %q", h.m.pending, h.count(), h.m.status)
	}
}

func (h *harness) guarding() bool { return h.m.guarding() }

func TestDestructiveNeedsConnectionName(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	for _, stmt := range []string{"delete from users", "update users set age = 1", "drop table users", "alter table users add column x int"} {
		h.sql(stmt)
		if h.m.confirm == nil || h.m.confirm.want != "testdb" {
			t.Fatalf("%s: confirm = %+v", stmt, h.m.confirm)
		}
		h.typeText("yes") // the ordinary word is not enough
		h.press("enter")
		if h.m.guarding() || h.count() != "3" {
			t.Fatalf("%s ran on \"yes\"", stmt)
		}
	}
	h.sql("delete from users")
	h.typeText("testdb")
	h.press("enter")
	if h.m.pending == nil || h.m.pending.Affected != 3 {
		t.Fatalf("pending = %+v", h.m.pending)
	}
	h.press("r")
	if h.count() != "3" {
		t.Fatalf("count = %s after rollback", h.count())
	}
}

func TestProtectedProfile(t *testing.T) {
	h := newHarness(t, func(p *config.Profile) { p.Protect = true })
	h.connect()
	h.sql("insert into logs values ('x')")
	if h.m.confirm == nil || h.m.confirm.want != "testdb" {
		t.Fatalf("confirm = %+v, want the connection name", h.m.confirm)
	}
}

func TestReadOnlyProfile(t *testing.T) {
	h := newHarness(t, func(p *config.Profile) { p.ReadOnly = true })
	h.connect()
	h.sql("delete from users where id = 1")
	if h.m.guarding() || !h.m.statusErr || !strings.Contains(h.m.status, "read-only") {
		t.Fatalf("guarding = %v, status = %q", h.guarding(), h.m.status)
	}
	if h.count() != "3" {
		t.Fatalf("count = %s", h.count())
	}
}

func TestRefusals(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	for _, stmt := range []string{"commit", "begin", "select 1; delete from users"} {
		h.sql(stmt)
		if h.m.guarding() || !h.m.statusErr {
			t.Errorf("%q: guarding = %v, status = %q", stmt, h.guarding(), h.m.status)
		}
	}
	// a write disguised well enough to fool the classifier would still hit
	// the read-only connection; simulate the misjudgement directly
	h.pump(h.m.query("delete from users"))
	if h.count() != "3" || len(h.m.errs) == 0 {
		t.Fatalf("write ran on the read path: count = %s", h.count())
	}
}

func TestQuitRollsBack(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	h.sql("delete from users where id = 1")
	h.typeText("yes")
	h.press("enter")
	if h.m.pending == nil {
		t.Fatal("no pending write")
	}
	path := h.m.conn.Profile.Path
	h.m.shutdown()
	h.m.pending, h.m.conn = nil, nil

	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var n int
	if err := check.QueryRow("SELECT count(*) FROM users").Scan(&n); err != nil || n != 3 {
		t.Fatalf("count = %d, err = %v; want 3", n, err)
	}
}

// TestServerNavigation walks connection → schema → table on a real server:
// DBREADER_TEST_POSTGRES=postgres://... go test ./internal/ui
func TestServerNavigation(t *testing.T) {
	url := os.Getenv("DBREADER_TEST_POSTGRES")
	if url == "" {
		t.Skip("DBREADER_TEST_POSTGRES not set")
	}
	p := config.Profile{Name: "pg", Driver: config.Postgres, URL: url, Limit: 5}
	h := &harness{t: t, m: newModel(&config.Settings{Profiles: []config.Profile{p, p}}, "")}
	t.Cleanup(h.m.shutdown)
	h.m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	h.press("enter")
	if h.m.top() != scrSchemas {
		t.Fatalf("top = %v, status = %q", h.m.top(), h.m.status)
	}
	if e, _ := h.m.schemas.current(); e.Name != "public" {
		t.Errorf("cursor on schema %q, want public", e.Name)
	}
	h.typeText(":schema pg_catalog")
	h.press("enter")
	if h.m.top() != scrTables || h.m.schema != "pg_catalog" || len(h.m.tables.all) < 50 {
		t.Fatalf("top = %v, schema = %q, tables = %d", h.m.top(), h.m.schema, len(h.m.tables.all))
	}
	h.typeText(":table pg_class")
	h.press("enter")
	g := h.m.grid
	if h.m.top() != scrData || len(g.res.Rows) != 5 || !g.res.Truncated {
		t.Fatalf("top = %v, status = %q", h.m.top(), h.m.status)
	}
	h.typeText(":where relname = 'pg_class'")
	h.press("enter")
	if n := len(h.m.grid.res.Rows); n != 1 {
		t.Fatalf("rows = %d after :where, status = %q", n, h.m.status)
	}
	h.press("i")
	if h.m.top() != scrStruct || !strings.Contains(h.screen(), "relname") {
		t.Errorf("structure page:\n%s", h.screen())
	}
}

const testScript = `-- who is grown up
select name from users
  where age > 10;

delete from users where id = 2;
insert into logs values ('a;b');
drop table logs;
`

func scriptHarness(t *testing.T) *harness {
	t.Helper()
	file := filepath.Join(t.TempDir(), "users.sql")
	if err := os.WriteFile(file, []byte(testScript), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(p *config.Profile) {
		p.Scripts = []config.Script{{Name: "users.sql", Path: file}}
	})
	h.m.settings.Scripts = []config.Script{{Name: "scratch.sql", Path: file}}
	h.connect()
	h.press("S")
	if h.m.top() != scrScripts || len(h.m.scripts.all) != 2 {
		t.Fatalf("top = %v, scripts = %d, status = %q", h.m.top(), len(h.m.scripts.all), h.m.status)
	}
	h.press("enter")
	if h.m.top() != scrScript || len(h.m.script.lines) != 7 || len(h.m.script.spans) != 4 {
		t.Fatalf("top = %v, script = %+v", h.m.top(), h.m.script)
	}
	return h
}

func (h *harness) command(line string) {
	h.t.Helper()
	h.typeText(":" + line)
	h.press("enter")
}

func TestScriptRunStatementUnderCursor(t *testing.T) {
	h := scriptHarness(t)
	out := h.screen()
	for _, want := range []string{"1  -- who is grown up", "2  select name from users", "7  drop table logs;", "statement at line 1: read"} {
		if !strings.Contains(out, want) {
			t.Errorf("script screen lacks %q:\n%s", want, out)
		}
	}
	if h.m.confirm != nil || h.m.loading {
		t.Fatal("opening a script ran something")
	}

	// anywhere in the statement, :run takes all of it and nothing more
	h.press("j", "j", "$")
	h.command("run")
	if h.m.top() != scrData || len(h.m.grid.res.Rows) != 1 || h.m.grid.res.Rows[0][0].S != "ada" {
		t.Fatalf("top = %v, status = %q", h.m.top(), h.m.status)
	}
	h.press("q")
	if h.m.top() != scrScript || h.m.script.Cursor != 2 {
		t.Fatalf("top = %v, cursor = %d; want back in the script on line 3", h.m.top(), h.m.script.Cursor)
	}

	// the blank line belongs to the statement below it
	h.press("j")
	if _, what := h.m.script.target(); what != "statement at line 5" {
		t.Errorf("target on the blank line = %q", what)
	}
	h.command("exec")
	if c := h.m.confirm; c == nil || c.want != "yes" || c.stmt.Text != "delete from users where id = 2" || h.count() != "3" {
		t.Fatalf("confirm = %+v, count = %s", h.m.confirm, h.count())
	}
	h.press("esc")

	h.press("G")
	h.command("run")
	if c := h.m.confirm; c == nil || c.want != "testdb" || c.stmt.Text != "drop table logs" {
		t.Fatalf("drop from a script: confirm = %+v", h.m.confirm)
	}
	h.press("esc")
}

func TestScriptRunSelection(t *testing.T) {
	h := scriptHarness(t)

	// characters: part of a line is a statement of its own
	h.press("j", "v", "$")
	if got, _ := h.m.script.target(); got != "select name from users" {
		t.Fatalf("selection = %q", got)
	}
	if !strings.Contains(h.screen(), "VISUAL") {
		t.Errorf("no VISUAL badge:\n%s", h.screen())
	}
	h.command("run")
	if h.m.top() != scrData || len(h.m.grid.res.Rows) != 3 {
		t.Fatalf("top = %v, rows = %v, status = %q", h.m.top(), h.m.grid.res, h.m.status)
	}
	h.press("q")
	if h.m.script.sel.Active() {
		t.Error("selection survived the run")
	}

	// lines: two writes run in order, each confirmed and committed alone
	h.command("5")
	h.press("V", "j")
	if !strings.Contains(h.screen(), "2 write") {
		t.Errorf("status does not announce two writes:\n%s", h.screen())
	}
	h.command("run")
	if c := h.m.confirm; c == nil || !strings.HasPrefix(c.stmt.Text, "delete") || !strings.Contains(h.screen(), "statement 1 of 2") {
		t.Fatalf("first confirm = %+v\n%s", h.m.confirm, h.screen())
	}
	h.typeText("yes")
	h.press("enter")
	if h.m.pending == nil || h.m.confirm != nil {
		t.Fatalf("pending = %v: the second statement must wait for the first commit", h.m.pending)
	}
	h.press("c")
	if c := h.m.confirm; c == nil || c.stmt.Text != "insert into logs values ('a;b')" || h.count() != "2" {
		t.Fatalf("second confirm = %+v, count = %s, status = %q", h.m.confirm, h.count(), h.m.status)
	}
	h.typeText("yes")
	h.press("enter", "c")
	if h.guarding() || len(h.m.queue) != 0 {
		t.Fatalf("run did not finish: queue = %v", h.m.queue)
	}
	res, err := h.m.conn.Query(t.Context(), "select line from logs", 5)
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0].S != "a;b" {
		t.Fatalf("logs = %+v, err = %v", res, err)
	}
}

func TestScriptRunStopsOnRefusal(t *testing.T) {
	// rollback, a wrong word, esc and an error each end the run
	for name, stop := range map[string]func(h *harness){
		"rollback": func(h *harness) { h.typeText("yes"); h.press("enter", "r") },
		"mistyped": func(h *harness) { h.typeText("y"); h.press("enter") },
		"esc":      func(h *harness) { h.press("esc") },
	} {
		h := scriptHarness(t)
		h.command("5")
		h.press("V", "G")
		h.command("run")
		if h.m.confirm == nil {
			t.Fatalf("%s: no confirmation", name)
		}
		stop(h)
		if h.guarding() || len(h.m.queue) != 0 || h.count() != "3" {
			t.Errorf("%s: guarding = %v, queue = %v, count = %s", name, h.guarding(), h.m.queue, h.count())
		}
	}

	h := scriptHarness(t)
	h.m.script = newFileView("select * from nope;\ndelete from users where id = 1;\n", h.m.conn.Dialect())
	h.press("V", "G")
	h.command("run")
	if h.guarding() || len(h.m.queue) != 0 || len(h.m.errs) != 1 {
		t.Errorf("after a failed read: guarding = %v, queue = %v, errs = %d", h.guarding(), h.m.queue, len(h.m.errs))
	}
}

func TestRunOutsideScript(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	h.command("run")
	if !h.m.statusErr || h.m.loading || h.guarding() {
		t.Fatalf("status = %q", h.m.status)
	}
}

func TestSQLPromptCompletion(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	if len(h.m.colNames["users"]) != 3 {
		t.Fatalf("column names not loaded: %v", h.m.colNames)
	}
	h.press("s")
	h.typeText("select u. from us")
	if n := len(h.m.sqlHist.Items); n != 1 {
		t.Fatalf("candidates = %d, want users", n)
	}
	if !strings.Contains(h.screen(), "users") {
		t.Errorf("popup not drawn:\n%s", h.screen())
	}
	h.press("tab")
	if got := h.m.input.Value(); got != "select u. from users" {
		t.Fatalf("after tab: %q", got)
	}
	h.typeText(" u")
	// back to the dot, where the alias now resolves
	h.m.input.SetCursor(len("select u."))
	h.typeText("a")
	h.press("tab")
	if got := h.m.input.Value(); got != "select u.age from users u" {
		t.Fatalf("column completion: %q", got)
	}
	if pos := h.m.input.Position(); pos != len("select u.age") {
		t.Errorf("cursor at %d, want it after the completed word", pos)
	}
	h.press("enter")
	if h.m.top() != scrData || len(h.m.grid.res.Rows) != 3 {
		t.Fatalf("top = %v, status = %q", h.m.top(), h.m.status)
	}
}

func TestBlockedConnection(t *testing.T) {
	h := newHarness(t, func(p *config.Profile) { p.Blocked = "it connects through an ssh tunnel" })
	h.press("enter")
	if h.m.conn != nil || !h.m.statusErr || !strings.Contains(h.m.status, "ssh tunnel") {
		t.Fatalf("conn = %v, status = %q", h.m.conn, h.m.status)
	}
}
