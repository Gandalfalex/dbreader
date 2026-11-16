// Package db talks to PostgreSQL, MySQL and SQLite through database/sql.
// Reads and writes take separate paths: Query runs inside a read-only
// transaction the server enforces, Exec opens a transaction the caller must
// commit or roll back explicitly.
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	go_ora "github.com/sijms/go-ora/v2"

	"dbreader/internal/config"
	"dbreader/internal/sqlsafe"
)

type Conn struct {
	Profile config.Profile
	db      *sql.DB
	d       dialect
}

// Cell is one value of a result, already formatted.
type Cell struct {
	S    string
	Null bool
}

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

type Column struct {
	Name     string
	Type     string
	Nullable bool
	Default  string
	PK       bool
}

// Open connects and pings, so a bad profile fails here.
func Open(ctx context.Context, p config.Profile) (*Conn, error) {
	d, ok := dialects[p.Driver]
	if !ok {
		return nil, fmt.Errorf("unknown driver %q", p.Driver)
	}
	dsn, err := d.dsn(p)
	if err != nil {
		return nil, err
	}
	h, err := sql.Open(d.driver, dsn)
	if err != nil {
		return nil, err
	}
	h.SetMaxOpenConns(3)
	h.SetConnMaxIdleTime(5 * time.Minute)
	if err := h.PingContext(ctx); err != nil {
		h.Close()
		return nil, err
	}
	return &Conn{Profile: p, db: h, d: d}, nil
}

func (c *Conn) Close() error { return c.db.Close() }

// Dialect is the lexical dialect for sqlsafe.
func (c *Conn) Dialect() sqlsafe.Dialect { return c.d.safe }

// HasSchemas reports whether the server has more than one namespace to pick.
func (c *Conn) HasSchemas() bool { return c.d.schemas != "" }

// read runs fn on a connection that cannot change data: a READ ONLY
// transaction on PostgreSQL and MySQL, query_only on SQLite.
func (c *Conn) read(ctx context.Context, fn func(q querier) error) error {
	for attempt := 0; ; attempt++ {
		err := c.readOnce(ctx, fn)
		if err == nil || c.d.retry == nil || !c.d.retry(err) || attempt >= 3 {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Second):
		}
	}
}

func (c *Conn) readOnce(ctx context.Context, fn func(q querier) error) error {
	if c.d.queryOnly {
		conn, err := c.db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
			return err
		}
		defer func() {
			if _, err := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA query_only = OFF"); err != nil {
				// never hand a connection in an unknown state back to the pool
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}()
		return fn(conn)
	}
	var tx *sql.Tx
	var err error
	if c.d.readTx != "" { // the driver cannot ask for a read-only transaction
		if tx, err = c.db.BeginTx(ctx, nil); err == nil {
			if _, err = tx.ExecContext(ctx, c.d.readTx); err != nil {
				tx.Rollback()
			}
		}
	} else {
		tx, err = c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Query runs a statement read-only and returns at most limit rows.
func (c *Conn) Query(ctx context.Context, query string, limit int, args ...any) (*Result, error) {
	if c.d.guardReads {
		if st := sqlsafe.Classify(query, c.d.safe); st.Kind != sqlsafe.Read {
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
			res.Types = append(res.Types, typeName(t.DatabaseTypeName()))
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
				row[i] = format(v, res.Types[i], c.d.dateHasTime)
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

func (c *Conn) Schemas(ctx context.Context) ([]string, error) {
	if !c.HasSchemas() {
		return nil, nil
	}
	var out []string
	err := c.read(ctx, func(q querier) error {
		rows, err := q.QueryContext(ctx, c.d.schemas)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// CurrentSchema is the schema unqualified names resolve to, "" if unknown.
func (c *Conn) CurrentSchema(ctx context.Context) string {
	if c.d.current == "" {
		return ""
	}
	res, err := c.Query(ctx, c.d.current, 1)
	if err != nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return ""
	}
	return res.Rows[0][0].S
}

func (c *Conn) Tables(ctx context.Context, schema string) ([]Table, error) {
	var out []Table
	err := c.read(ctx, func(q querier) error {
		rows, err := q.QueryContext(ctx, c.d.tables, c.d.args(schema)...)
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
		rows, err := q.QueryContext(ctx, c.d.columns, c.d.args(schema, table)...)
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
	return c.columnNames(ctx, c.d.colNames, schema)
}

// SearchColumns is ColumnNames without the columns text cannot be searched
// in, such as LOBs and raw bytes.
func (c *Conn) SearchColumns(ctx context.Context, schema string) (map[string][]string, error) {
	if c.d.searchNames == "" {
		return c.ColumnNames(ctx, schema)
	}
	return c.columnNames(ctx, c.d.searchNames, schema)
}

func (c *Conn) columnNames(ctx context.Context, query, schema string) (map[string][]string, error) {
	out := map[string][]string{}
	err := c.read(ctx, func(q querier) error {
		rows, err := q.QueryContext(ctx, query, c.d.args(schema)...)
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
	switch {
	case b.Limit > 0 && c.d.rownum:
		return "SELECT * FROM (" + s.String() + ") WHERE ROWNUM <= " + strconv.Itoa(b.Limit+1)
	case b.Limit > 0:
		s.WriteString(" LIMIT " + strconv.Itoa(b.Limit+1))
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
		parts[i] = fmt.Sprintf(c.d.contains, c.Quote(col), lit)
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// Literal quotes a string value.
func (c *Conn) Literal(s string) string {
	if c.d.backslash {
		s = strings.ReplaceAll(s, "\\", "\\\\")
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// CountMatches counts the rows of a table in which one of cols contains
// text, stopping at limit.
func (c *Conn) CountMatches(ctx context.Context, schema, table string, cols []string, text string, limit int) (int, error) {
	if len(cols) == 0 {
		return 0, nil
	}
	query := fmt.Sprintf("SELECT count(*) FROM (SELECT 1 FROM %s WHERE %s LIMIT %d) AS m",
		c.Qualified(schema, table), c.SearchCond(cols, text), limit)
	if c.d.rownum {
		query = fmt.Sprintf("SELECT count(*) FROM (SELECT 1 FROM %s WHERE %s AND ROWNUM <= %d)",
			c.Qualified(schema, table), c.SearchCond(cols, text), limit)
	}
	res, err := c.Query(ctx, query, 1)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(res.Rows[0][0].S)
}

// Quote quotes an identifier.
func (c *Conn) Quote(ident string) string {
	q := string(c.d.quote)
	return q + strings.ReplaceAll(ident, q, q+q) + q
}

func (c *Conn) Qualified(schema, table string) string {
	if schema == "" || !c.HasSchemas() {
		return c.Quote(table)
	}
	return c.Quote(schema) + "." + c.Quote(table)
}

func format(v any, typ string, dateHasTime bool) Cell {
	switch v := v.(type) {
	case nil:
		return Cell{Null: true}
	case go_ora.Clob: // Oracle LOBs arrive as structs
		return lobCell(v.String, v.Valid)
	case go_ora.NClob:
		return lobCell(v.String, v.Valid)
	case go_ora.Blob:
		if !v.Valid && v.Data == nil {
			return Cell{Null: true}
		}
		return format(v.Data, typ, dateHasTime)
	case go_ora.TimeStamp:
		return format(time.Time(v), "TIMESTAMP", dateHasTime)
	case go_ora.TimeStampTZ:
		return format(time.Time(v), "TIMESTAMPTZ", dateHasTime)
	case go_ora.Number:
		str, _ := v.String()
		return Cell{S: str}
	case []byte:
		if utf8.Valid(v) {
			return Cell{S: string(v)}
		}
		const maxHex = 64
		if len(v) > maxHex {
			return Cell{S: fmt.Sprintf("\\x%s… (%d bytes)", hex.EncodeToString(v[:maxHex]), len(v))}
		}
		return Cell{S: "\\x" + hex.EncodeToString(v)}
	case string:
		return Cell{S: v}
	case time.Time:
		if typ == "DATE" && !dateHasTime {
			return Cell{S: v.Format("2006-01-02")}
		}
		if strings.Contains(typ, "TZ") || strings.Contains(typ, "TIME ZONE") {
			return Cell{S: v.Format("2006-01-02 15:04:05.999999 Z07:00")}
		}
		return Cell{S: v.Format("2006-01-02 15:04:05.999999")}
	case float64:
		return Cell{S: strconv.FormatFloat(v, 'g', -1, 64)}
	case float32:
		return Cell{S: strconv.FormatFloat(float64(v), 'g', -1, 32)}
	}
	return Cell{S: fmt.Sprint(v)}
}

func lobCell(s string, valid bool) Cell {
	if !valid && s == "" {
		return Cell{Null: true}
	}
	return Cell{S: s}
}

// driverTypes maps type names the Oracle driver reports in its own
// spelling to the ones Oracle users know.
var driverTypes = map[string]string{
	"TIMESTAMPDTY": "TIMESTAMP", "TIMESTAMPTZ_DTY": "TIMESTAMP WITH TIME ZONE", "TIMESTAMPLTZ_DTY": "TIMESTAMP WITH LOCAL TIME ZONE",
	"TIMESTAMPELTZ": "TIMESTAMP WITH LOCAL TIME ZONE", "TIMESTAMPETZ": "TIMESTAMP WITH TIME ZONE",
	"INTERVALDS_DTY": "INTERVAL DAY TO SECOND", "INTERVALYM_DTY": "INTERVAL YEAR TO MONTH",
	"IBDOUBLE": "BINARY_DOUBLE", "IBFLOAT": "BINARY_FLOAT", "NCHAR": "VARCHAR2", "LONGVARCHAR": "CLOB",
	"OCICLOBLOCATOR": "CLOB", "OCIBLOBLOCATOR": "BLOB", "OCIFILELOCATOR": "BFILE", "LONGRAW": "LONG RAW",
}

func typeName(driver string) string {
	up := strings.ToUpper(driver)
	if name, ok := driverTypes[up]; ok {
		return name
	}
	return up
}
