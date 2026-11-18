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

// serverSpec is what differs between servers in testServer.
type serverSpec struct {
	schema string
	table  string              // the test table, spelled as the catalog reports it
	create string              // CREATE TABLE statement for it
	insert string              // inserts (1, 'ada', NULL) and (2, 'bob', 'x')
	drop   string              // DROP TABLE statement that tolerates a missing table
	fold   func(string) string // how the catalog spells an unquoted column name
	// ddlRefused: the read-only transaction also refuses DDL (not so in Oracle, where
	// DDL commits the transaction first and then runs)
	ddlRefused bool
}

var standardSpec = serverSpec{
	table:      "dbreader_t",
	create:     "CREATE TABLE dbreader_t (id INT PRIMARY KEY, name VARCHAR(20) NOT NULL, note TEXT)",
	insert:     "INSERT INTO dbreader_t VALUES (1, 'ada', NULL), (2, 'bob', 'x')",
	drop:       "DROP TABLE IF EXISTS dbreader_t",
	fold:       func(s string) string { return s },
	ddlRefused: true,
}

func testServer(t *testing.T, c *Conn, schema string) {
	spec := standardSpec
	spec.schema = schema
	testServerSpec(t, c, spec)
}

func testServerSpec(t *testing.T, c *Conn, spec serverSpec) {
	schema, table, col := spec.schema, spec.table, spec.fold
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
		res, err := c.Query(ctx, "SELECT count(*) FROM "+table, 1)
		if err != nil {
			t.Fatal(err)
		}
		return res.Rows[0][0].S
	}

	drop := func() { // a missing table is fine
		if p, err := c.Exec(ctx, sqlsafe.Classify(spec.drop, d)); err == nil && p.Open() {
			p.Commit()
		}
	}
	drop()
	exec(spec.create, true)
	t.Cleanup(drop)
	if p := exec(spec.insert, true); p.Affected != 2 {
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
		found = found || (tb.Name == table && tb.Kind == "table")
	}
	if !found {
		t.Errorf("%s not in %+v", table, tables)
	}
	cols, err := c.Columns(ctx, schema, table)
	if err != nil || len(cols) != 3 {
		t.Fatalf("columns = %+v, err = %v", cols, err)
	}
	if !cols[0].PK || cols[0].Nullable || cols[1].Nullable || !cols[2].Nullable {
		t.Errorf("columns = %+v", cols)
	}

	names, err := c.ColumnNames(ctx, schema)
	if err != nil || strings.Join(names[table], ",") != col("id")+","+col("name")+","+col("note") {
		t.Errorf("column names = %v, err = %v", names[table], err)
	}

	for text, want := range map[string]int{"AD": 1, "x": 1, "2": 1, "%": 0, `a\`: 0, "'": 0} {
		n, err := c.CountMatches(ctx, schema, table, []string{col("id"), col("name"), col("note")}, text, 10)
		if err != nil || n != want {
			t.Errorf("CountMatches(%q) = %d, %v; want %d", text, n, err, want)
		}
	}

	res, err := c.Query(ctx, c.SelectSQL(Browse{Schema: schema, Table: table, Order: col("id"), Desc: true, Limit: 1}), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || !res.Truncated || res.Rows[0][1].S != "bob" {
		t.Errorf("rows = %+v, truncated = %v", res.Rows, res.Truncated)
	}

	// the read path must refuse writes whatever the classifier thought
	refused := []string{"DELETE FROM " + table, "INSERT INTO " + table + " VALUES (3, 'eve', NULL)"}
	if spec.ddlRefused {
		refused = append(refused, "DROP TABLE "+table, "CREATE TABLE dbreader_u (a INT)")
	}
	for _, q := range refused {
		if _, err := c.Query(ctx, q, 10); err == nil {
			t.Errorf("Query(%q) succeeded on the read path", q)
		}
	}
	if n := count(); n != "2" {
		t.Fatalf("count = %s after refused writes, want 2", n)
	}

	if p := exec("DELETE FROM "+table, false); p.Affected != 2 {
		t.Errorf("affected = %d, want 2", p.Affected)
	}
	if n := count(); n != "2" {
		t.Errorf("count = %s after rollback, want 2", n)
	}
	exec("DELETE FROM "+table+" WHERE id = 1", true)
	if n := count(); n != "1" {
		t.Errorf("count = %s after commit, want 1", n)
	}
}

func TestPostgres(t *testing.T) {
	c := serverConn(t, "postgres", "DBREADER_TEST_POSTGRES")
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

// TestOracle: DBREADER_TEST_ORACLE=oracle://app:app@localhost:1521/FREEPDB1
func TestOracle(t *testing.T) {
	c := serverConn(t, "oracle", "DBREADER_TEST_ORACLE")
	schema := os.Getenv("DBREADER_TEST_ORACLE_SCHEMA")
	if schema == "" {
		schema = "APP"
	}
	testServerSpec(t, c, serverSpec{
		schema: schema,
		table:  "DBREADER_T",
		create: "CREATE TABLE dbreader_t (id NUMBER(10) PRIMARY KEY, name VARCHAR2(20) NOT NULL, note CLOB)",
		insert: "INSERT ALL INTO dbreader_t VALUES (1, 'ada', NULL) INTO dbreader_t VALUES (2, 'bob', 'x') SELECT 1 FROM dual",
		drop:   "DROP TABLE dbreader_t PURGE",
		fold:   strings.ToUpper,
		// Oracle's read-only transaction does not stop DDL (it commits first and
		// runs it), so Query refuses it itself.
		ddlRefused: true,
	})
}

// TestOracleCatalog covers what only Oracle has: synonyms, LONG defaults.
func TestOracleCatalog(t *testing.T) {
	c := serverConn(t, "oracle", "DBREADER_TEST_ORACLE")
	ctx := context.Background()
	run := func(sql string) error {
		p, err := c.Exec(ctx, sqlsafe.Classify(sql, c.Dialect()))
		if err == nil && p.Open() {
			err = p.Commit()
		}
		return err
	}
	run("DROP SYNONYM dbreader_syn")
	run("DROP TABLE dbreader_d PURGE")
	for _, sql := range []string{
		"CREATE TABLE dbreader_d (id NUMBER(10) PRIMARY KEY, state VARCHAR2(10) DEFAULT 'new' NOT NULL, made DATE DEFAULT SYSDATE, raw_data BLOB)",
		"CREATE SYNONYM dbreader_syn FOR dbreader_d",
		"INSERT INTO dbreader_d (id) VALUES (7)",
	} {
		if err := run(sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() { run("DROP SYNONYM dbreader_syn"); run("DROP TABLE dbreader_d PURGE") })

	// a PL/SQL block runs as one statement, inside the transaction: its DML
	// can still be rolled back
	block := sqlsafe.Split("BEGIN\n  INSERT INTO dbreader_d (id) VALUES (8);\n  INSERT INTO dbreader_d (id) VALUES (9);\nEND;\n/\n", c.Dialect())
	if len(block) != 1 {
		t.Fatalf("block split into %q", block)
	}
	p, err := c.Exec(ctx, sqlsafe.Classify(block[0], c.Dialect()))
	if err != nil || !p.Open() {
		t.Fatalf("block: %v", err)
	}
	p.Rollback()
	if res, err := c.Query(ctx, "SELECT count(*) FROM dbreader_d", 1); err != nil || res.Rows[0][0].S != "1" {
		t.Errorf("after rolling back the block: %+v, %v", res, err)
	}

	tables, err := c.Tables(ctx, "APP")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, tb := range tables {
		kinds[tb.Name] = tb.Kind
	}
	if kinds["DBREADER_SYN"] != "synonym" || kinds["DBREADER_D"] != "table" {
		t.Errorf("kinds = %v", kinds)
	}

	// table and synonym show the same structure, defaults included
	for _, name := range []string{"DBREADER_D", "DBREADER_SYN"} {
		cols, err := c.Columns(ctx, "APP", name)
		if err != nil || len(cols) != 4 {
			t.Fatalf("%s: columns = %+v, err = %v", name, cols, err)
		}
		if id := cols[0]; id.Name != "ID" || !id.PK || id.Nullable || id.Type != "NUMBER(10)" || id.Default != "" {
			t.Errorf("%s: id = %+v", name, id)
		}
		if st := cols[1]; st.Type != "VARCHAR2(10)" || st.Default != "'new'" || st.Nullable {
			t.Errorf("%s: state = %+v", name, st)
		}
		if made := cols[2]; made.Default != "SYSDATE" || !made.Nullable {
			t.Errorf("%s: made = %+v", name, made)
		}
	}
	names, err := c.ColumnNames(ctx, "APP")
	if err != nil || strings.Join(names["DBREADER_SYN"], ",") != "ID,STATE,MADE,RAW_DATA" {
		t.Errorf("synonym columns = %v, err = %v", names["DBREADER_SYN"], err)
	}
	search, err := c.SearchColumns(ctx, "APP")
	if err != nil || strings.Join(search["DBREADER_SYN"], ",") != "ID,STATE,MADE" {
		t.Errorf("searchable synonym columns = %v, err = %v", search["DBREADER_SYN"], err)
	}
	res, err := c.Query(ctx, c.SelectSQL(Browse{Schema: "APP", Table: "DBREADER_SYN", Search: "NEW", SearchCols: search["DBREADER_SYN"], Limit: 5}), 5)
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0].S != "7" {
		t.Errorf("browse through the synonym: %+v, %v", res, err)
	}
}

func TestMySQL(t *testing.T) {
	testServer(t, serverConn(t, "mysql", "DBREADER_TEST_MYSQL"), "mysql")
}
