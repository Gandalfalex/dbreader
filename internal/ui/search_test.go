package ui

import (
	"dbreader/internal/driver/sqlite"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbreader/internal/config"
	"dbreader/internal/db"
	"dbreader/internal/sqlsafe"
)

func TestTableFilterFindsColumns(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	h.press("/")
	h.typeText("age") // no table is called that; users has the column
	h.press("enter")
	if len(h.m.tables.vis) != 1 {
		t.Fatalf("visible = %d", len(h.m.tables.vis))
	}
	if e, _ := h.m.tables.current(); e.Name != "users" {
		t.Errorf("found %q, want users", e.Name)
	}
}

func TestGrepAcrossTables(t *testing.T) {
	h := newHarness(t, nil)
	h.connect()
	p, err := h.m.conn.Exec(t.Context(), sqlsafe.Classify("insert into logs values ('ada logged in'), ('nothing')", sqlite.SQL))
	if err != nil {
		t.Fatal(err)
	}
	p.Commit()

	h.press("?")
	if h.m.mode != inputSearch {
		t.Fatalf("mode = %v, status = %q", h.m.mode, h.m.status)
	}
	h.typeText("ADA")
	h.press("enter")
	if h.m.top() != scrGrep || h.m.loading {
		t.Fatalf("top = %v, loading = %v, status = %q", h.m.top(), h.m.loading, h.m.status)
	}
	if len(h.m.grep.all) != 2 || !strings.Contains(h.m.status, "2 of 2 tables") {
		t.Fatalf("hits = %+v, status = %q", h.m.grep.all, h.m.status)
	}
	h.press("enter") // logs
	g := h.m.grid
	if h.m.top() != scrData || g.browse.Table != "logs" || len(g.res.Rows) != 1 || g.res.Rows[0][0].S != "ada logged in" {
		t.Fatalf("top = %v, rows = %+v, status = %q", h.m.top(), g.res, h.m.status)
	}
	h.press("q")
	if h.m.top() != scrGrep {
		t.Errorf("top = %v, want the search results again", h.m.top())
	}

	h.command("grep nobody has this")
	if h.m.top() != scrGrep || len(h.m.grep.all) != 0 {
		t.Errorf("hits = %+v", h.m.grep.all)
	}
}

func TestSearchOneTable(t *testing.T) {
	h := newHarness(t, func(p *config.Profile) { p.Limit = 2 })
	h.connect()
	h.press("j", "enter") // users, only 2 of 3 rows loaded
	if !h.m.grid.res.Truncated {
		t.Fatal("expected a truncated page")
	}
	h.press("?")
	h.typeText("cy") // the row that is not loaded
	h.press("enter")
	g := h.m.grid
	if len(g.res.Rows) != 1 || g.res.Rows[0][1].S != "cy" || !strings.Contains(h.screen(), `rows containing "cy"`) {
		t.Fatalf("rows = %+v\n%s", g.res.Rows, h.screen())
	}
	h.press("?", "enter") // empty clears
	if len(h.m.grid.res.Rows) != 2 || h.m.grid.browse.Search != "" {
		t.Errorf("search not cleared: %+v", h.m.grid.browse)
	}
}

func TestScriptSearch(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) config.Script {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return config.Script{Name: name, Path: path}
	}
	a := write("monthly.sql", "select 1;\n\n-- revenue per Customer\nselect customer, sum(total)\nfrom orders;\n")
	b := write("cleanup.sql", "delete from logs;\n")
	h := newHarness(t, func(p *config.Profile) { p.Scripts = []config.Script{a, b} })
	h.connect()
	h.press("S", "/")
	h.typeText("revenue") // in the file, not in its name
	h.press("enter")
	if len(h.m.scripts.vis) != 1 {
		t.Fatalf("visible scripts = %d", len(h.m.scripts.vis))
	}
	h.press("enter")
	f := &h.m.script
	if h.m.top() != scrScript || f.Cursor != 2 || f.col != 3 {
		t.Fatalf("top = %v, cursor = %d:%d; want the match on line 3", h.m.top(), f.Cursor, f.col)
	}

	// / inside the file: smart-case, n and N wrap around
	h.press("g", "g", "/")
	h.typeText("customer")
	if f.Cursor != 2 {
		t.Errorf("incremental search at line %d, want 3", f.Cursor+1)
	}
	h.press("enter", "n")
	if f.Cursor != 3 || f.col != 7 {
		t.Errorf("n: cursor = %d:%d, want 3:7", f.Cursor, f.col)
	}
	h.press("n")
	if f.Cursor != 2 {
		t.Errorf("n did not wrap: line %d", f.Cursor+1)
	}
	h.press("N")
	if f.Cursor != 3 {
		t.Errorf("N: line %d", f.Cursor+1)
	}
	h.press("/")
	h.typeText("Customer") // upper case: exact
	h.press("enter", "n")
	if f.Cursor != 2 {
		t.Errorf("case-sensitive search moved to line %d", f.Cursor+1)
	}
	h.press("g", "g", "/")
	h.typeText("zzz")
	h.press("esc")
	if f.Cursor != 0 || f.pattern != "Customer" {
		t.Errorf("esc: cursor line %d, pattern %q", f.Cursor+1, f.pattern)
	}
}

func TestYankRows(t *testing.T) {
	cols := []string{"id", "note"}
	rows := [][]db.Cell{{{S: "1"}, {S: "a,b \"q\"\tx\ny"}}, {{S: "2"}, {Null: true}}}
	want := map[string]string{
		"tsv":      "id\tnote\n1\ta,b \"q\" x y\n2\t\n",
		"csv":      "id,note\n1,\"a,b \"\"q\"\"\tx\ny\"\n2,\n",
		"markdown": "| id | note |\n| --- | --- |\n| 1 | a,b \"q\"\tx y |\n| 2 | NULL |\n",
		"json":     "[\n  {\n    \"id\": \"1\",\n    \"note\": \"a,b \\\"q\\\"\\tx\\ny\"\n  },\n  {\n    \"id\": \"2\",\n    \"note\": null\n  }\n]\n",
	}
	for format, w := range want {
		if got := formatRows(cols, rows, format); got != w {
			t.Errorf("%s:\n got %q\nwant %q", format, got, w)
		}
	}

	h := newHarness(t, nil)
	h.connect()
	h.press("j", "enter", "j", "V", "j")
	if from, to := h.m.grid.selRange(); !h.m.grid.selecting || from != 1 || to != 2 {
		t.Fatalf("selection = %d-%d", from, to)
	}
	if !strings.Contains(h.screen(), "2 rows selected") {
		t.Errorf("no selection in the status bar:\n%s", h.screen())
	}
	h.press("y")
	if h.m.grid.selecting {
		t.Error("selection survived the yank")
	}
	h.press("V", "esc")
	if h.m.grid.selecting || h.m.top() != scrData {
		t.Errorf("esc: selecting = %v, top = %v", h.m.grid.selecting, h.m.top())
	}
	h.command("yank nope")
	if !h.m.statusErr {
		t.Errorf("unknown format accepted: %q", h.m.status)
	}
}
