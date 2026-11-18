package db

import (
	"context"
	"database/sql"
	"dbreader/internal/driver/sqlite"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"dbreader/internal/config"
	"dbreader/internal/sqlsafe"
)

func testConn(t *testing.T, mod func(*config.Profile)) *Conn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "has space & more.db")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note TEXT DEFAULT 'x', blob BLOB);
		CREATE VIEW names AS SELECT name FROM users;
		INSERT INTO users (name, note, blob) VALUES ('ada', NULL, x'ff00'), ('bob', 'hi', NULL), ('cy', 'yo', NULL);
	`); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	p := config.Profile{Name: "test", Driver: "sqlite", Path: path}
	if mod != nil {
		mod(&p)
	}
	c, err := Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func count(t *testing.T, c *Conn) string {
	t.Helper()
	res, err := c.Query(context.Background(), "SELECT count(*) FROM users", 10)
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows[0][0].S
}

func TestQuery(t *testing.T) {
	c := testConn(t, nil)
	res, err := c.Query(context.Background(), "SELECT id, name, note, blob FROM users ORDER BY id", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || !res.Truncated {
		t.Fatalf("rows = %d, truncated = %v; want 2, true", len(res.Rows), res.Truncated)
	}
	row := res.Rows[0]
	if row[0].S != "1" || row[1].S != "ada" || !row[2].Null || row[3].S != `\xff00` {
		t.Errorf("row = %+v", row)
	}
}

// The classifier can be wrong; the server must still refuse writes on the
// read path.
func TestQueryCannotWrite(t *testing.T) {
	c := testConn(t, nil)
	for _, q := range []string{
		"DELETE FROM users",
		"INSERT INTO users (name) VALUES ('eve') RETURNING id",
		"DROP TABLE users",
	} {
		if _, err := c.Query(context.Background(), q, 10); err == nil {
			t.Errorf("Query(%q) succeeded on the read path", q)
		}
	}
	if n := count(t, c); n != "3" {
		t.Errorf("count = %s after refused writes, want 3", n)
	}
	// and the connection must be writable again afterwards
	p, err := c.Exec(context.Background(), sqlsafe.Classify("DELETE FROM users WHERE id = 1", sqlite.SQL))
	if err != nil {
		t.Fatal(err)
	}
	p.Rollback()
}

func TestExecCommitRollback(t *testing.T) {
	c := testConn(t, nil)
	ctx := context.Background()
	stmt := sqlsafe.Classify("DELETE FROM users WHERE id > 1", sqlite.SQL)

	p, err := c.Exec(ctx, stmt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Affected != 2 || !p.Open() {
		t.Fatalf("affected = %d, open = %v; want 2, true", p.Affected, p.Open())
	}
	if err := p.Rollback(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, c); n != "3" {
		t.Errorf("count = %s after rollback, want 3", n)
	}

	if p, err = c.Exec(ctx, stmt); err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, c); n != "1" {
		t.Errorf("count = %s after commit, want 1", n)
	}
}

func TestExecReadOnlyProfile(t *testing.T) {
	c := testConn(t, func(p *config.Profile) { p.ReadOnly = true })
	_, err := c.Exec(context.Background(), sqlsafe.Classify("DELETE FROM users", sqlite.SQL))
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("err = %v, want ErrReadOnly", err)
	}
	if n := count(t, c); n != "3" {
		t.Errorf("count = %s, want 3", n)
	}
}

func TestCatalog(t *testing.T) {
	c := testConn(t, nil)
	ctx := context.Background()
	tables, err := c.Tables(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 || tables[0].Name != "names" || tables[0].Kind != "view" || tables[1].Name != "users" {
		t.Errorf("tables = %+v", tables)
	}
	names, err := c.ColumnNames(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names["users"], ","); got != "id,name,note,blob" || len(names["names"]) != 1 {
		t.Errorf("column names = %v", names)
	}
	cols, err := c.Columns(ctx, "", "users")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 4 {
		t.Fatalf("columns = %+v", cols)
	}
	if id := cols[0]; id.Name != "id" || !id.PK {
		t.Errorf("id = %+v", id)
	}
	if name := cols[1]; name.Nullable || name.Type != "TEXT" {
		t.Errorf("name = %+v", name)
	}
	if note := cols[2]; !note.Nullable || note.Default != "'x'" {
		t.Errorf("note = %+v", note)
	}
}

func TestSelectSQL(t *testing.T) {
	c := testConn(t, nil)
	got := c.SelectSQL(Browse{Table: `we"ird`, Where: "id > 1", Order: "name", Desc: true, Limit: 10})
	want := `SELECT * FROM "we""ird" WHERE (id > 1) ORDER BY "name" DESC LIMIT 11`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestOpenMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.db")
	if _, err := Open(context.Background(), config.Profile{Driver: "sqlite", Path: path}); err == nil {
		t.Fatal("opened a missing sqlite file")
	}
}

func TestSearch(t *testing.T) {
	c := testConn(t, nil)
	ctx := context.Background()
	cols := []string{"id", "name", "note", "blob"}
	for text, want := range map[string]int{"AD": 1, "b": 1, "3": 1, "y": 1, "i": 1, "%": 0, "_": 0, "'": 0, "zzz": 0} {
		n, err := c.CountMatches(ctx, "", "users", cols, text, 100)
		if err != nil || n != want {
			t.Errorf("CountMatches(%q) = %d, %v; want %d", text, n, err, want)
		}
	}
	if n, _ := c.CountMatches(ctx, "", "users", []string{"name"}, "", 2); n != 2 {
		t.Errorf("limit not applied: %d", n)
	}
	res, err := c.Query(ctx, c.SelectSQL(Browse{Table: "users", Where: "id > 1 OR id = 1", Search: "y", SearchCols: cols}), 10)
	if err != nil || len(res.Rows) != 1 {
		t.Errorf("browse with search: %v, %v", res, err)
	}
}
