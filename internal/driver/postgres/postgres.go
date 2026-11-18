// Package postgres is the PostgreSQL driver.
package postgres

import (
	"fmt"
	"net/url"

	_ "github.com/jackc/pgx/v5/stdlib"

	"dbreader/internal/config"
	"dbreader/internal/driver"
	"dbreader/internal/sqlsafe"
)

const Name = "postgres"

func init() { driver.Register(Driver{}) }

type Driver struct{ driver.Base }

func (Driver) Name() string              { return Name }
func (Driver) Aliases() []string         { return []string{"postgresql", "pg", "pgx"} }
func (Driver) Dialect() *sqlsafe.Dialect { return SQL }
func (Driver) Catalog() driver.Catalog   { return catalog }
func (Driver) Contains(col, pattern string) string {
	return fmt.Sprintf(`CAST(%s AS TEXT) ILIKE %s ESCAPE '!'`, col, pattern)
}

func (Driver) DSN(p config.Profile) (string, string, error) {
	if p.URL != "" {
		return "pgx", p.URL, nil
	}
	u := url.URL{Scheme: "postgres", Host: driver.HostPort(p, 5432), Path: "/" + p.Database}
	switch {
	case p.Password != "":
		u.User = url.UserPassword(p.User, p.Password)
	case p.User != "":
		u.User = url.User(p.User)
	}
	q := url.Values{"application_name": {"dbreader"}}
	if p.SSLMode != "" {
		q.Set("sslmode", p.SSLMode)
	}
	u.RawQuery = q.Encode()
	return "pgx", u.String(), nil
}

var catalog = driver.Catalog{
	Schemas: `SELECT n.nspname,
			(SELECT count(*) FROM pg_class c WHERE c.relnamespace = n.oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f')),
			CASE WHEN n.nspname IN ('pg_catalog', 'information_schema') THEN 1 ELSE 0 END sys
		FROM pg_namespace n WHERE n.nspname !~ '^pg_(toast|temp)'
		ORDER BY sys, n.nspname`,
	Current: `SELECT current_schema()`,
	Tables: `SELECT c.relname,
			CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview' WHEN 'f' THEN 'foreign' ELSE 'table' END,
			c.reltuples::bigint
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		ORDER BY c.relname`,
	Columns: `SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
			coalesce(pg_get_expr(d.adbin, d.adrelid), ''), coalesce(i.indisprimary, false)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		LEFT JOIN pg_index i ON i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey)
		WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`,
	ColNames: `SELECT c.relname, a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY c.relname, a.attnum`,
}

func (Driver) DBeaverProviders() []string { return []string{"postgresql"} }

// SQL is how PostgreSQL statements are read.
var SQL = &sqlsafe.Dialect{NestedComments: true, DollarQuotes: true, Fold: sqlsafe.FoldLower}
