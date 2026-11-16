package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"dbreader/internal/config"
	"dbreader/internal/sqlsafe"
)

// Server tests run against real databases when their URL is set, e.g.
//
//	DBREADER_TEST_POSTGRES=postgres://postgres:test@localhost:55432/postgres?sslmode=disable
//	DBREADER_TEST_MYSQL=root:test@tcp(localhost:53306)/mysql
func serverConn(t *testing.T, driver, env string) *Conn {
	t.Helper()
	url := os.Getenv(env)
	if url == "" {
		t.Skip(env + " not set")
	}
	c, err := Open(context.Background(), config.Profile{Name: "server", Driver: driver, URL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func testServer(t *testing.T, c *Conn, schema string) {
	ctx := context.Background()
	d := c.Dialect()
	exec := func(sql string, commit bool) *Pending {
		t.Helper()
		p, err := c.Exec(ctx, sqlsafe.Classify(sql, d))
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if p.Open() {
			if commit {
				err = p.Commit()
			} else {
				err = p.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	count := func() string {
		t.Helper()
		res, err := c.Query(ctx, "SELECT count(*) FROM dbreader_t", 1)
		if err != nil {
			t.Fatal(err)
		}
		return res.Rows[0][0].S
	}

	exec("DROP TABLE IF EXISTS dbreader_t", true)
	exec("CREATE TABLE dbreader_t (id INT PRIMARY KEY, name VARCHAR(20) NOT NULL, note TEXT)", true)
	t.Cleanup(func() { exec("DROP TABLE IF EXISTS dbreader_t", true) })
	if p := exec("INSERT INTO dbreader_t VALUES (1, 'ada', NULL), (2, 'bob', 'x')", true); p.Affected != 2 {
		t.Errorf("affected = %d, want 2", p.Affected)
	}

	schemas, err := c.Schemas(ctx)
	if err != nil || len(schemas) == 0 {
		t.Fatalf("schemas = %v, err = %v", schemas, err)
	}
	if cur := c.CurrentSchema(ctx); cur != schema {
		t.Errorf("current schema = %q, want %q", cur, schema)
	}
	tables, err := c.Tables(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tb := range tables {
		found = found || (tb.Name == "dbreader_t" && tb.Kind == "table")
	}
	if !found {
		t.Errorf("dbreader_t not in %+v", tables)
	}
	cols, err := c.Columns(ctx, schema, "dbreader_t")
	if err != nil || len(cols) != 3 {
		t.Fatalf("columns = %+v, err = %v", cols, err)
	}
	if !cols[0].PK || cols[0].Nullable || cols[1].Nullable || !cols[2].Nullable {
		t.Errorf("columns = %+v", cols)
	}

	names, err := c.ColumnNames(ctx, schema)
	if err != nil || strings.Join(names["dbreader_t"], ",") != "id,name,note" {
		t.Errorf("column names = %v, err = %v", names["dbreader_t"], err)
	}

	for text, want := range map[string]int{"AD": 1, "x": 1, "2": 1, "%": 0, `a\`: 0, "'": 0} {
		n, err := c.CountMatches(ctx, schema, "dbreader_t", []string{"id", "name", "note"}, text, 10)
		if err != nil || n != want {
			t.Errorf("CountMatches(%q) = %d, %v; want %d", text, n, err, want)
		}
	}

	res, err := c.Query(ctx, c.SelectSQL(Browse{Schema: schema, Table: "dbreader_t", Order: "id", Desc: true, Limit: 1}), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || !res.Truncated || res.Rows[0][1].S != "bob" {
		t.Errorf("rows = %+v, truncated = %v", res.Rows, res.Truncated)
	}

	// the read path must refuse writes whatever the classifier thought
	for _, q := range []string{
		"DELETE FROM dbreader_t",
		"INSERT INTO dbreader_t VALUES (3, 'eve', NULL)",
		"DROP TABLE dbreader_t",
		"CREATE TABLE dbreader_u (a INT)",
	} {
		if _, err := c.Query(ctx, q, 10); err == nil {
			t.Errorf("Query(%q) succeeded on the read path", q)
		}
	}
	if n := count(); n != "2" {
		t.Fatalf("count = %s after refused writes, want 2", n)
	}

	if p := exec("DELETE FROM dbreader_t", false); p.Affected != 2 {
		t.Errorf("affected = %d, want 2", p.Affected)
	}
	if n := count(); n != "2" {
		t.Errorf("count = %s after rollback, want 2", n)
	}
	exec("DELETE FROM dbreader_t WHERE id = 1", true)
	if n := count(); n != "1" {
		t.Errorf("count = %s after commit, want 1", n)
	}
}

func TestPostgres(t *testing.T) {
	c := serverConn(t, config.Postgres, "DBREADER_TEST_POSTGRES")
	testServer(t, c, "public")
	// functions with side effects are caught by the server, not the classifier
	ctx := context.Background()
	exec := func(sql string) {
		p, err := c.Exec(ctx, sqlsafe.Classify(sql, c.Dialect()))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SEQUENCE IF NOT EXISTS dbreader_s")
	defer exec("DROP SEQUENCE dbreader_s")
	if _, err := c.Query(ctx, "SELECT nextval('dbreader_s')", 1); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("nextval in a read-only transaction: err = %v", err)
	}
}

func TestMySQL(t *testing.T) {
	testServer(t, serverConn(t, config.MySQL, "DBREADER_TEST_MYSQL"), "mysql")
}
