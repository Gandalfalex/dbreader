// Package mysql is the MySQL and MariaDB driver.
package mysql

import (
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"

	"dbreader/internal/config"
	"dbreader/internal/driver"
	"dbreader/internal/sqlsafe"
)

const Name = "mysql"

func init() { driver.Register(Driver{}) }

type Driver struct{ driver.Base }

func (Driver) Name() string              { return Name }
func (Driver) Aliases() []string         { return []string{"mariadb"} }
func (Driver) Dialect() *sqlsafe.Dialect { return SQL }
func (Driver) Catalog() driver.Catalog   { return catalog }
func (Driver) Quote() string             { return "`" }
func (Driver) Contains(col, pattern string) string {
	return fmt.Sprintf(`CAST(%s AS CHAR) LIKE %s ESCAPE '!'`, col, pattern)
}

// Literal also escapes backslashes, which MySQL reads as escapes.
func (d Driver) Literal(s string) string {
	return d.Base.Literal(strings.ReplaceAll(s, `\`, `\\`))
}

func (Driver) DSN(p config.Profile) (string, string, error) {
	if p.URL != "" {
		return "mysql", p.URL, nil
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = p.User, p.Password, p.Database
	cfg.Net, cfg.Addr = "tcp", driver.HostPort(p, 3306)
	switch p.SSLMode {
	case "":
	case "disable":
		cfg.TLSConfig = "false"
	case "require":
		cfg.TLSConfig = "skip-verify" // encrypt without checking the certificate, as sslmode=require does
	case "verify-ca", "verify-full":
		cfg.TLSConfig = "true"
	default:
		cfg.TLSConfig = p.SSLMode
	}
	return "mysql", cfg.FormatDSN(), nil
}

var catalog = driver.Catalog{
	Schemas: `SELECT s.schema_name,
			(SELECT count(*) FROM information_schema.tables t WHERE t.table_schema = s.schema_name),
			CASE WHEN s.schema_name IN ('information_schema', 'mysql', 'performance_schema', 'sys') THEN 1 ELSE 0 END sys
		FROM information_schema.schemata s ORDER BY sys, s.schema_name`,
	Current: `SELECT database()`,
	Tables: `SELECT table_name, CASE table_type WHEN 'BASE TABLE' THEN 'table' ELSE lower(table_type) END,
			coalesce(table_rows, -1)
		FROM information_schema.tables WHERE table_schema = ? ORDER BY table_name`,
	Columns: `SELECT column_name, column_type, is_nullable = 'YES', coalesce(column_default, ''), column_key = 'PRI'
		FROM information_schema.columns WHERE table_schema = ? AND table_name = ?
		ORDER BY ordinal_position`,
	ColNames: `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = ? ORDER BY table_name, ordinal_position`,
}

func (Driver) DBeaverProviders() []string { return []string{"mysql", "mariadb"} }

// SQL is how MySQL statements are read. MySQL commits DDL implicitly,
// whatever the transaction state.
var SQL = &sqlsafe.Dialect{BackslashEscapes: true, ImplicitCommit: map[string]bool{
	"CREATE": true, "DROP": true, "ALTER": true, "TRUNCATE": true, "RENAME": true,
}}
