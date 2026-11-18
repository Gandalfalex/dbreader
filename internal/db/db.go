// Package db talks to the database servers the drivers in package driver
// describe, through database/sql.
// Reads and writes take separate paths: Query runs inside a read-only
// transaction the server enforces, Exec opens a transaction the caller must
// commit or roll back explicitly.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dbreader/internal/config"
	"dbreader/internal/driver"
	"dbreader/internal/sqlsafe"
)

type Conn struct {
	Profile config.Profile
	db      *sql.DB
	d       driver.Driver
	cat     driver.Catalog
}

// Cell is one value of a result, already formatted.
type Cell = driver.Cell

type Result struct {
	Columns   []string
	Types     []string // database type names, upper case
	Rows      [][]Cell
	Truncated bool // more rows were available than the limit
	Took      time.Duration
}

type Table struct {
	Name string
	Kind string // table, view, ...
	Rows int64  // planner estimate, -1 when unknown
}

type Schema struct {
	Name    string
	Objects int  // tables and views
	System  bool // owned by the server itself
}

type Column struct {
	Name     string
	Type     string
	Nullable bool
	Default  string
	PK       bool
}

// Open connects and pings, so a bad profile fails here.
func Open(ctx context.Context, p config.Profile) (*Conn, error) {
	d, ok := driver.Lookup(p.Driver)
	if !ok {
		return nil, fmt.Errorf("unknown driver %q (%s)", p.Driver, strings.Join(driver.Names(), ", "))
	}
	name, dsn, err := d.DSN(p)
	if err != nil {
		return nil, err
	}
	h, err := sql.Open(name, dsn)
	if err != nil {
		return nil, err
	}
	h.SetMaxOpenConns(3)
	h.SetConnMaxIdleTime(5 * time.Minute)
	if err := h.PingContext(ctx); err != nil {
		h.Close()
		return nil, err
	}
	return &Conn{Profile: p, db: h, d: d, cat: d.Catalog()}, nil
}

func (c *Conn) Close() error { return c.db.Close() }

// Dialect is the lexical dialect for sqlsafe.
func (c *Conn) Dialect() *sqlsafe.Dialect { return c.d.Dialect() }

// HasSchemas reports whether the server has more than one namespace to pick.
func (c *Conn) HasSchemas() bool { return c.cat.Schemas != "" }

// args are the catalog query arguments: without schemas, no schema.
func (c *Conn) args(schemaTable ...string) []any {
	if !c.HasSchemas() {
		schemaTable = schemaTable[1:]
	}
	out := make([]any, len(schemaTable))
	for i, s := range schemaTable {
		out[i] = s
	}
	return out
}

// read runs fn on a connection the server keeps from changing data, and
// repeats it when the driver says the error is passing.
func (c *Conn) read(ctx context.Context, fn func(q querier) error) error {
	for attempt := 0; ; attempt++ {
		err := c.d.ReadOnly(ctx, c.db, fn)
		if err == nil || !c.d.Retry(err) || attempt >= 3 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Second):
		}
	}
}

type querier = driver.Querier

// Query runs a statement read-only and returns at most limit rows.
func (c *Conn) Query(ctx context.Context, query string, limit int, args ...any) (*Result, error) {
	if c.d.GuardReads() {
		if st := sqlsafe.Classify(query, c.Dialect()); st.Kind != sqlsafe.Read {
			return nil, fmt.Errorf("refused on the read path: %s", st.Reason)
		}
	}
	res := &Result{}
	start := time.Now()
	err := c.read(ctx, func(q querier) error {
		res.Rows, res.Truncated = nil, false // a retry starts over
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if res.Columns, err = rows.Columns(); err != nil {
			return err
		}
		types, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		for _, t := range types {
			res.Types = append(res.Types, c.d.TypeName(t.DatabaseTypeName()))
		}
		vals := make([]any, len(res.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if limit > 0 && len(res.Rows) >= limit {
				res.Truncated = true
				return nil
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			row := make([]Cell, len(vals))
			for i, v := range vals {
				row[i] = driver.Value(c.d, v, res.Types[i])
			}
			res.Rows = append(res.Rows, row)
		}
		return rows.Err()
	})
	res.Took = time.Since(start)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Pending is an executed write whose transaction is still open.
type Pending struct {
	Stmt     sqlsafe.Stmt
	Affected int64
	Took     time.Duration
	tx       *sql.Tx // nil once finished, or for statements that ran outside a transaction
}

// Open reports whether the write still waits for Commit or Rollback.
func (p *Pending) Open() bool { return p.tx != nil }

func (p *Pending) Commit() error {
	tx := p.tx
	p.tx = nil
	return tx.Commit()
}

func (p *Pending) Rollback() error {
	tx := p.tx
	p.tx = nil
	return tx.Rollback()
}

// ErrReadOnly is returned by Exec on profiles marked readOnly.
var ErrReadOnly = errors.New("connection is read-only (readOnly in settings.json)")

// Exec runs a write. The transaction stays open so the caller can show the
// affected rows before deciding; statements that cannot run in a transaction
// (stmt.NoTx) take effect immediately.
func (c *Conn) Exec(ctx context.Context, stmt sqlsafe.Stmt) (*Pending, error) {
	if c.Profile.ReadOnly {
		return nil, ErrReadOnly
	}
	p := &Pending{Stmt: stmt}
	start := time.Now()
	var res sql.Result
	var err error
	if stmt.NoTx {
		res, err = c.db.ExecContext(ctx, stmt.Text)
	} else {
		// The transaction must outlive ctx, which only bounds the statement.
		if p.tx, err = c.db.BeginTx(context.WithoutCancel(ctx), nil); err != nil {
			return nil, err
		}
		if res, err = p.tx.ExecContext(ctx, stmt.Text); err != nil {
			_ = p.Rollback()
		}
	}
	if err != nil {
		return nil, err
	}
	p.Took = time.Since(start)
	p.Affected, _ = res.RowsAffected()
	return p, nil
}

func (c *Conn) Schemas(ctx context.Context) ([]Schema, error) {
	if !c.HasSchemas() {
		return nil, nil
	}
	out, err := c.schemaList(ctx, c.cat.Schemas)
	if err != nil && c.cat.SchemasOld != "" && ctx.Err() == nil {
		out, err = c.schemaList(ctx, c.cat.SchemasOld)
	}
	return out, err
}

func (c *Conn) schemaList(ctx context.Context, query string) ([]Schema, error) {
	var out []Schema
	err := c.read(ctx, func(q querier) error {
		out = out[:0]
		rows, err := q.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s Schema
			var sys int
			if err := rows.Scan(&s.Name, &s.Objects, &sys); err != nil {
				return err
			}
			s.System = sys != 0
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// CurrentSchema is the schema unqualified names resolve to, "" if unknown.
func (c *Conn) CurrentSchema(ctx context.Context) string {
	if c.cat.Current == "" {
		return ""
	}
	res, err := c.Query(ctx, c.cat.Current, 1)
	if err != nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return ""
	}
	return res.Rows[0][0].S
}

func (c *Conn) Tables(ctx context.Context, schema string) ([]Table, error) {
	var out []Table
	err := c.read(ctx, func(q querier) error {
		rows, err := q.QueryContext(ctx, c.cat.Tables, c.args(schema)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Table
			var n float64 // Oracle returns NUMBER as a float or a string
			if err := rows.Scan(&t.Name, &t.Kind, &n); err != nil {
				return err
			}
			t.Rows = int64(n)
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

func (c *Conn) Columns(ctx context.Context, schema, table string) ([]Column, error) {
	var out []Column
	err := c.read(ctx, func(q querier) error {
		rows, err := q.QueryContext(ctx, c.cat.Columns, c.args(schema, table)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var col Column
			var def sql.NullString // Oracle has no empty string, only NULL
			if err := rows.Scan(&col.Name, &col.Type, &col.Nullable, &def, &col.PK); err != nil {
				return err
			}
			col.Default = strings.TrimSpace(def.String)
			out = append(out, col)
		}
		return rows.Err()
	})
	return out, err
}

// ColumnNames returns the columns of every table in schema, for completion.
func (c *Conn) ColumnNames(ctx context.Context, schema string) (map[string][]string, error) {
	return c.columnNames(ctx, c.cat.ColNames, schema)
}

// SearchColumns is ColumnNames without the columns text cannot be searched
// in, such as LOBs and raw bytes.
func (c *Conn) SearchColumns(ctx context.Context, schema string) (map[string][]string, error) {
	if c.cat.SearchNames == "" {
		return c.ColumnNames(ctx, schema)
	}
	return c.columnNames(ctx, c.cat.SearchNames, schema)
}

func (c *Conn) columnNames(ctx context.Context, query, schema string) (map[string][]string, error) {
	out := map[string][]string{}
	err := c.read(ctx, func(q querier) error {
		rows, err := q.QueryContext(ctx, query, c.args(schema)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var table, col string
			if err := rows.Scan(&table, &col); err != nil {
				return err
			}
			out[table] = append(out[table], col)
		}
		return rows.Err()
	})
	return out, err
}

// Browse describes a page of table data.
type Browse struct {
	Schema, Table string
	Where         string // raw condition, as typed
	Search        string // text any of SearchCols must contain
	SearchCols    []string
	Order         string // column name
	Desc          bool
	Limit         int
}

// SelectSQL builds the statement for b. The row limit is applied by Query,
// plus one row here so truncation can be detected.
func (c *Conn) SelectSQL(b Browse) string {
	var s strings.Builder
	s.WriteString("SELECT * FROM " + c.Qualified(b.Schema, b.Table))
	var conds []string
	if b.Where != "" {
		conds = append(conds, "("+b.Where+")")
	}
	if b.Search != "" && len(b.SearchCols) > 0 {
		conds = append(conds, c.SearchCond(b.SearchCols, b.Search))
	}
	if len(conds) > 0 {
		s.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	if b.Order != "" {
		s.WriteString(" ORDER BY " + c.Quote(b.Order))
		if b.Desc {
			s.WriteString(" DESC")
		}
	}
	if b.Limit > 0 {
		return c.d.Limit(s.String(), b.Limit+1)
	}
	return s.String()
}

// SearchCond is the condition "one of cols contains text", whatever the
// column types, ignoring case.
func (c *Conn) SearchCond(cols []string, text string) string {
	// ! escapes the LIKE wildcards, so the text is matched literally
	pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(text)
	lit := c.Literal("%" + pattern + "%")
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = c.d.Contains(c.Quote(col), lit)
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// Literal quotes a string value.
func (c *Conn) Literal(s string) string { return c.d.Literal(s) }

// CountMatches counts the rows of a table in which one of cols contains
// text, stopping at limit.
func (c *Conn) CountMatches(ctx context.Context, schema, table string, cols []string, text string, limit int) (int, error) {
	if len(cols) == 0 {
		return 0, nil
	}
	query := "SELECT count(*) FROM (" +
		c.d.Limit("SELECT 1 FROM "+c.Qualified(schema, table)+" WHERE "+c.SearchCond(cols, text), limit) + ") m"
	res, err := c.Query(ctx, query, 1)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(res.Rows[0][0].S)
}

// Quote quotes an identifier.
func (c *Conn) Quote(ident string) string { return QuoteIdent(c.QuoteChar(), ident) }

// QuoteChar is the character the server quotes identifiers with.
func (c *Conn) QuoteChar() string { return c.d.Quote() }

// QuoteIdent quotes ident with q, doubling any q inside it.
func QuoteIdent(q, ident string) string { return q + strings.ReplaceAll(ident, q, q+q) + q }

func (c *Conn) Qualified(schema, table string) string {
	if schema == "" || !c.HasSchemas() {
		return c.Quote(table)
	}
	return c.Quote(schema) + "." + c.Quote(table)
}
