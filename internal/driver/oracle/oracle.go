// Package oracle is the Oracle driver (go-ora, no client libraries).
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"dbreader/internal/config"
	"dbreader/internal/driver"
	"dbreader/internal/sqlsafe"
)

const Name = "oracle"

func init() { driver.Register(Driver{}) }

type Driver struct{ driver.Base }

func (Driver) Name() string              { return Name }
func (Driver) Aliases() []string         { return []string{"oracle_thin", "ora"} }
func (Driver) Dialect() *sqlsafe.Dialect { return SQL }
func (Driver) Catalog() driver.Catalog   { return catalog }
func (Driver) Contains(col, pattern string) string {
	return fmt.Sprintf(`UPPER(%s) LIKE UPPER(%s) ESCAPE '!'`, col, pattern)
}

// ReadOnly uses SET TRANSACTION READ ONLY: go-ora has no read-only option.
func (Driver) ReadOnly(ctx context.Context, db *sql.DB, fn func(driver.Querier) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return err
	}
	return fn(tx)
}

// GuardReads: Oracle commits and runs DDL even in a read-only transaction.
func (Driver) GuardReads() bool { return true }

// Retry: ORA-01466 for a table changed within the last second.
func (Driver) Retry(err error) bool { return strings.Contains(err.Error(), "ORA-01466") }

// Limit uses ROWNUM, which every Oracle version has.
func (Driver) Limit(query string, n int) string {
	return "SELECT * FROM (" + query + ") WHERE ROWNUM <= " + strconv.Itoa(n)
}

func (Driver) DSN(p config.Profile) (string, string, error) {
	if p.URL != "" {
		return "oracle", p.URL, nil // oracle://user:password@host:1521/service
	}
	if p.Host == "" || p.Database == "" {
		return "", "", fmt.Errorf("profile %q: oracle needs host and database (service name, or SID with \"sid\": true)", p.Name)
	}
	port := p.Port
	if port == 0 {
		port = 1521
	}
	opts := map[string]string{}
	service := p.Database
	if p.SID {
		opts["SID"], service = p.Database, ""
	}
	switch p.SSLMode {
	case "", "disable":
	case "require":
		opts["SSL"], opts["SSL VERIFY"] = "true", "false" // encrypt, as sslmode=require does
	default:
		opts["SSL"], opts["SSL VERIFY"] = "true", "true"
	}
	switch role := strings.ToUpper(p.Role); role {
	case "":
	case "SYSDBA", "SYSOPER":
		opts["DBA PRIVILEGE"] = role
	default:
		return "", "", fmt.Errorf("profile %q: unknown role %q (sysdba, sysoper)", p.Name, p.Role)
	}
	if len(opts) == 0 {
		opts = nil
	}
	return "oracle", go_ora.BuildUrl(p.Host, port, service, p.User, p.Password, opts), nil
}

// typeNames maps type names go-ora reports in its own spelling to the ones
// Oracle users know.
var typeNames = map[string]string{
	"TIMESTAMPDTY": "TIMESTAMP", "TIMESTAMPTZ_DTY": "TIMESTAMP WITH TIME ZONE", "TIMESTAMPLTZ_DTY": "TIMESTAMP WITH LOCAL TIME ZONE",
	"TIMESTAMPELTZ": "TIMESTAMP WITH LOCAL TIME ZONE", "TIMESTAMPETZ": "TIMESTAMP WITH TIME ZONE",
	"INTERVALDS_DTY": "INTERVAL DAY TO SECOND", "INTERVALYM_DTY": "INTERVAL YEAR TO MONTH",
	"IBDOUBLE": "BINARY_DOUBLE", "IBFLOAT": "BINARY_FLOAT", "NCHAR": "VARCHAR2", "LONGVARCHAR": "CLOB",
	"OCICLOBLOCATOR": "CLOB", "OCIBLOBLOCATOR": "BLOB", "OCIFILELOCATOR": "BFILE", "LONGRAW": "LONG RAW",
}

func (Driver) TypeName(name string) string {
	up := strings.ToUpper(name)
	if n, ok := typeNames[up]; ok {
		return n
	}
	return up
}

func (d Driver) Format(v any, typ string) (driver.Cell, bool) {
	switch v := v.(type) {
	case go_ora.Clob: // LOBs arrive as structs
		return lob(v.String, v.Valid), true
	case go_ora.NClob:
		return lob(v.String, v.Valid), true
	case go_ora.Blob:
		if !v.Valid && v.Data == nil {
			return driver.Cell{Null: true}, true
		}
		return driver.Value(d, v.Data, typ), true
	case go_ora.TimeStamp:
		return driver.Value(d, time.Time(v), "TIMESTAMP"), true
	case go_ora.TimeStampTZ:
		return driver.Value(d, time.Time(v), "TIMESTAMPTZ"), true
	case go_ora.Number:
		s, _ := v.String()
		return driver.Cell{S: s}, true
	case time.Time:
		if typ == "DATE" { // an Oracle DATE has a time of day
			return driver.Value(d, v, "TIMESTAMP"), true
		}
	}
	return driver.Cell{}, false
}

func lob(s string, valid bool) driver.Cell {
	if !valid && s == "" {
		return driver.Cell{Null: true}
	}
	return driver.Cell{S: s}
}

// SQL is how Oracle statements are read. SHOW, DESCRIBE and PRAGMA are not
// SQL in Oracle, and EXPLAIN PLAN writes to the plan table, so only SELECT,
// WITH and VALUES read. DDL commits before and after it runs.
var SQL = &sqlsafe.Dialect{
	QQuotes: true, SlashTerminator: true, Blocks: true, Fold: sqlsafe.FoldUpper,
	ReadVerbs: map[string]bool{"SELECT": true, "WITH": true, "VALUES": true},
	ImplicitCommit: map[string]bool{
		"CREATE": true, "DROP": true, "ALTER": true, "TRUNCATE": true, "RENAME": true,
		"GRANT": true, "REVOKE": true, "COMMENT": true, "FLASHBACK": true, "PURGE": true,
		"ANALYZE": true, "AUDIT": true, "NOAUDIT": true, "ASSOCIATE": true, "DISASSOCIATE": true,
	},
}
