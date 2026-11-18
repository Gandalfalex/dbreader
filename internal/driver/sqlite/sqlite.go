// Package sqlite is the SQLite driver.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"os"
	"strings"

	_ "modernc.org/sqlite"

	"dbreader/internal/config"
	dbdriver "dbreader/internal/driver"
	"dbreader/internal/sqlsafe"
)

const Name = "sqlite"

func init() { dbdriver.Register(Driver{}) }

type Driver struct{ dbdriver.Base }

func (Driver) Name() string              { return Name }
func (Driver) Aliases() []string         { return []string{"sqlite3"} }
func (Driver) Dialect() *sqlsafe.Dialect { return SQL }
func (Driver) Catalog() dbdriver.Catalog { return catalog }
func (Driver) Contains(col, pattern string) string {
	return fmt.Sprintf(`CAST(%s AS TEXT) LIKE %s ESCAPE '!'`, col, pattern)
}

func (Driver) DSN(p config.Profile) (string, string, error) {
	if p.Path == "" {
		return "", "", fmt.Errorf("profile %q: sqlite needs a path", p.Name)
	}
	// sqlite would silently create a missing file
	if _, err := os.Stat(p.Path); err != nil {
		return "", "", err
	}
	q := url.Values{"_pragma": {"busy_timeout(5000)"}}
	if p.ReadOnly {
		q.Set("mode", "ro")
	}
	return "sqlite", (&url.URL{Scheme: "file", Opaque: url.PathEscape(p.Path), RawQuery: q.Encode()}).String(), nil
}

// ReadOnly uses PRAGMA query_only: SQLite has no read-only transactions.
func (Driver) ReadOnly(ctx context.Context, db *sql.DB, fn func(dbdriver.Querier) error) error {
	conn, err := db.Conn(ctx)
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

// one namespace: no Schemas query, and the queries take no schema
var catalog = dbdriver.Catalog{
	Tables: `SELECT name, type, -1 FROM sqlite_master
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`,
	Columns: `SELECT name, type, "notnull" = 0, coalesce(dflt_value, ''), pk > 0 FROM pragma_table_info(?)`,
	ColNames: `SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p
		WHERE m.type IN ('table', 'view') ORDER BY m.name, p.cid`,
}

func (Driver) DBeaverProviders() []string { return []string{"sqlite"} }

// FromDBeaver takes the database file from DBeaver's database field or URL.
func (Driver) FromDBeaver(c dbdriver.DBeaverConn, p *config.Profile) bool {
	p.Path, p.Database = p.Database, ""
	if p.Path == "" {
		p.Path = strings.TrimPrefix(c.URL, "jdbc:sqlite:")
	}
	return true
}

// SQL is how SQLite statements are read: standard SQL.
var SQL = &sqlsafe.Dialect{}
