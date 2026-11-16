package db

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"dbreader/internal/config"
	"dbreader/internal/sqlsafe"
)

// dialect holds what differs between servers. The catalog queries take the
// schema (and table) as positional arguments; system schemas sort last.
type dialect struct {
	driver    string // database/sql driver name
	safe      sqlsafe.Dialect
	quote     byte
	queryOnly bool // read-only via PRAGMA query_only instead of a transaction
	dsn       func(config.Profile) (string, error)
	args      func(schemaTable ...string) []any
	schemas   string // "" when the server has a single namespace
	current   string
	tables    string
	columns   string
	colNames  string // table and column name of every column in a schema, in order
	contains  string // "%s contains the pattern %s" for a column of any type, ignoring case
	backslash bool   // backslash escapes inside string literals
}

func all(a ...string) []any {
	out := make([]any, len(a))
	for i, s := range a {
		out[i] = s
	}
	return out
}

var dialects = map[string]dialect{
	config.Postgres: {
		driver: "pgx", safe: sqlsafe.Postgres, quote: '"', dsn: postgresDSN, args: all,
		contains: `CAST(%s AS TEXT) ILIKE %s ESCAPE '!'`,
		schemas: `SELECT nspname FROM pg_namespace WHERE nspname !~ '^pg_(toast|temp)'
			ORDER BY nspname IN ('pg_catalog', 'information_schema'), nspname`,
		current: `SELECT current_schema()`,
		tables: `SELECT c.relname,
				CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview' WHEN 'f' THEN 'foreign' ELSE 'table' END,
				c.reltuples::bigint
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
			ORDER BY c.relname`,
		columns: `SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
				coalesce(pg_get_expr(d.adbin, d.adrelid), ''), coalesce(i.indisprimary, false)
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			LEFT JOIN pg_index i ON i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey)
			WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY a.attnum`,
		colNames: `SELECT c.relname, a.attname
			FROM pg_attribute a
			JOIN pg_class c ON c.oid = a.attrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY c.relname, a.attnum`,
	},
	config.MySQL: {
		driver: "mysql", safe: sqlsafe.MySQL, quote: '`', dsn: mysqlDSN, args: all,
		contains: `CAST(%s AS CHAR) LIKE %s ESCAPE '!'`, backslash: true,
		schemas: `SELECT schema_name FROM information_schema.schemata
			ORDER BY schema_name IN ('information_schema', 'mysql', 'performance_schema', 'sys'), schema_name`,
		current: `SELECT database()`,
		tables: `SELECT table_name, CASE table_type WHEN 'BASE TABLE' THEN 'table' ELSE lower(table_type) END,
				coalesce(table_rows, -1)
			FROM information_schema.tables WHERE table_schema = ? ORDER BY table_name`,
		columns: `SELECT column_name, column_type, is_nullable = 'YES', coalesce(column_default, ''), column_key = 'PRI'
			FROM information_schema.columns WHERE table_schema = ? AND table_name = ?
			ORDER BY ordinal_position`,
		colNames: `SELECT table_name, column_name FROM information_schema.columns
			WHERE table_schema = ? ORDER BY table_name, ordinal_position`,
	},
	config.SQLite: {
		driver: "sqlite", safe: sqlsafe.SQLite, quote: '"', queryOnly: true, dsn: sqliteDSN,
		contains: `CAST(%s AS TEXT) LIKE %s ESCAPE '!'`,
		// one namespace: the schema argument is dropped
		args: func(a ...string) []any { return all(a[1:]...) },
		tables: `SELECT name, type, -1 FROM sqlite_master
			WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`,
		columns: `SELECT name, type, "notnull" = 0, coalesce(dflt_value, ''), pk > 0 FROM pragma_table_info(?)`,
		colNames: `SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p
			WHERE m.type IN ('table', 'view') ORDER BY m.name, p.cid`,
	},
}

func hostPort(p config.Profile, defPort int) string {
	host, port := p.Host, p.Port
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		port = defPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func postgresDSN(p config.Profile) (string, error) {
	if p.URL != "" {
		return p.URL, nil
	}
	u := url.URL{Scheme: "postgres", Host: hostPort(p, 5432), Path: "/" + p.Database}
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
	return u.String(), nil
}

func mysqlDSN(p config.Profile) (string, error) {
	if p.URL != "" {
		return p.URL, nil
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = p.User, p.Password, p.Database
	cfg.Net, cfg.Addr = "tcp", hostPort(p, 3306)
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
	return cfg.FormatDSN(), nil
}

func sqliteDSN(p config.Profile) (string, error) {
	if p.Path == "" {
		return "", fmt.Errorf("profile %q: sqlite needs a path", p.Name)
	}
	// sqlite would silently create a missing file
	if _, err := os.Stat(p.Path); err != nil {
		return "", err
	}
	q := url.Values{"_pragma": {"busy_timeout(5000)"}}
	if p.ReadOnly {
		q.Set("mode", "ro")
	}
	return (&url.URL{Scheme: "file", Opaque: url.PathEscape(p.Path), RawQuery: q.Encode()}).String(), nil
}
