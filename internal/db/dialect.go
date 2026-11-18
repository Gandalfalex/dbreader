package db

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	go_ora "github.com/sijms/go-ora/v2"
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
	// schemas lists name, number of tables and views, and 1 for schemas
	// the server itself owns; "" when the server has a single namespace.
	schemas string
	// schemasOld replaces schemas on servers too old for it (Oracle 11g).
	schemasOld string
	current    string
	tables     string
	columns    string
	colNames   string // table and column name of every column in a schema, in order
	contains   string // "%s contains the pattern %s" for a column of any type, ignoring case
	backslash  bool   // backslash escapes inside string literals

	// searchNames is colNames restricted to columns text can be searched in;
	// "" when all of them can.
	searchNames string
	// readTx is run first in a read-only transaction when the driver has no
	// read-only option of its own.
	readTx string
	// rownum limits rows with ROWNUM (all Oracle versions) instead of LIMIT.
	rownum bool
	// dateHasTime: DATE columns carry a time of day (Oracle).
	dateHasTime bool
	// guardReads: the server's read-only transaction does not stop everything
	// (Oracle runs DDL in it after committing), so Query refuses any statement
	// that is not plainly a read.
	guardReads bool
	// retry reports errors that go away when the read is repeated a moment
	// later, e.g. Oracle's ORA-01466 for a table changed within the last second.
	retry func(error) bool
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
		schemas: `SELECT n.nspname,
				(SELECT count(*) FROM pg_class c WHERE c.relnamespace = n.oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f')),
				CASE WHEN n.nspname IN ('pg_catalog', 'information_schema') THEN 1 ELSE 0 END sys
			FROM pg_namespace n WHERE n.nspname !~ '^pg_(toast|temp)'
			ORDER BY sys, n.nspname`,
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
		schemas: `SELECT s.schema_name,
				(SELECT count(*) FROM information_schema.tables t WHERE t.table_schema = s.schema_name),
				CASE WHEN s.schema_name IN ('information_schema', 'mysql', 'performance_schema', 'sys') THEN 1 ELSE 0 END sys
			FROM information_schema.schemata s ORDER BY sys, s.schema_name`,
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
	config.Oracle: {
		driver: "oracle", safe: sqlsafe.Oracle, quote: '"', dsn: oracleDSN, args: all,
		readTx: "SET TRANSACTION READ ONLY", rownum: true, dateHasTime: true, guardReads: true,
		retry:      func(err error) bool { return strings.Contains(err.Error(), "ORA-01466") },
		contains:   `UPPER(%s) LIKE UPPER(%s) ESCAPE '!'`,
		schemas:    oracleSchemas(`OR u.oracle_maintained = 'Y'`),
		schemasOld: oracleSchemas(""),
		current:    `SELECT sys_context('USERENV', 'CURRENT_SCHEMA') FROM dual`,
		tables: `SELECT o.object_name, lower(o.object_type), nvl(t.num_rows, -1)
			FROM all_objects o
			LEFT JOIN all_tables t ON t.owner = o.owner AND t.table_name = o.object_name
			WHERE o.owner = :1 AND o.object_name NOT LIKE 'BIN$%'
				AND (o.object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW')
					OR (o.object_type = 'SYNONYM' AND EXISTS (
						SELECT 1 FROM all_synonyms s
						JOIN all_objects x ON x.owner = s.table_owner AND x.object_name = s.table_name
						WHERE s.owner = o.owner AND s.synonym_name = o.object_name
							AND x.object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW'))))
			ORDER BY o.object_name`,
		// a synonym shows the columns of the table it stands for
		columns: `WITH a AS (SELECT CAST(:1 AS VARCHAR2(128)) o, CAST(:2 AS VARCHAR2(128)) t FROM dual),
			r AS (SELECT o, t FROM a UNION ALL
				SELECT s.table_owner, s.table_name FROM all_synonyms s JOIN a ON s.owner = a.o AND s.synonym_name = a.t)
			SELECT c.column_name,
				c.data_type || CASE
					WHEN c.data_type LIKE '%CHAR%' AND c.char_length > 0 THEN '(' || c.char_length || ')'
					WHEN c.data_type = 'NUMBER' AND c.data_precision IS NOT NULL THEN
						'(' || c.data_precision || CASE WHEN nvl(c.data_scale, 0) > 0 THEN ',' || c.data_scale END || ')'
				END,
				CASE c.nullable WHEN 'Y' THEN 1 ELSE 0 END,
				c.data_default,
				CASE WHEN EXISTS (
					SELECT 1 FROM all_constraints k
					JOIN all_cons_columns cc ON cc.owner = k.owner AND cc.constraint_name = k.constraint_name
					WHERE k.constraint_type = 'P' AND k.owner = c.owner AND k.table_name = c.table_name
						AND cc.column_name = c.column_name) THEN 1 ELSE 0 END
			FROM all_tab_columns c JOIN r ON c.owner = r.o AND c.table_name = r.t ORDER BY c.column_id`,
		colNames: oracleColNames(""),
		searchNames: oracleColNames(`AND c.data_type_owner IS NULL
			AND c.data_type NOT IN ('BLOB', 'RAW', 'LONG RAW', 'LONG', 'BFILE', 'XMLTYPE', 'ANYDATA', 'ROWID', 'UROWID')`),
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

// oracleColNames lists the columns of a schema's tables, views and
// synonyms (under the synonym's name), restricted by cond.
func oracleColNames(cond string) string {
	return `WITH a AS (SELECT CAST(:1 AS VARCHAR2(128)) o FROM dual)
		SELECT name, column_name FROM (
			SELECT c.table_name name, c.column_name, c.column_id
			FROM all_tab_columns c JOIN a ON c.owner = a.o
			WHERE c.table_name NOT LIKE 'BIN$%' ` + cond + `
			UNION ALL
			SELECT s.synonym_name, c.column_name, c.column_id
			FROM all_synonyms s JOIN a ON s.owner = a.o
			JOIN all_tab_columns c ON c.owner = s.table_owner AND c.table_name = s.table_name
			WHERE 1 = 1 ` + cond + `)
		ORDER BY name, column_id`
}

// oracleSchemas lists the users with their tables, views and synonyms;
// system is a further condition for schemas Oracle itself owns.
func oracleSchemas(system string) string {
	return `SELECT u.username, nvl(o.n, 0),
			CASE WHEN u.username IN (` + oracleSystem + `) ` + system + ` THEN 1 ELSE 0 END sys
		FROM all_users u LEFT JOIN (
			SELECT owner, count(*) n FROM all_objects
			WHERE object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW', 'SYNONYM') AND object_name NOT LIKE 'BIN$%'
			GROUP BY owner) o ON o.owner = u.username
		ORDER BY sys, u.username`
}

// oracleSystem lists the schemas Oracle itself owns, which sort last.
const oracleSystem = `'SYS', 'SYSTEM', 'OUTLN', 'DBSNMP', 'XDB', 'CTXSYS', 'MDSYS', 'ORDSYS', 'ORDDATA', 'ORDPLUGINS',
	'WMSYS', 'APPQOSSYS', 'AUDSYS', 'GSMADMIN_INTERNAL', 'LBACSYS', 'OJVMSYS', 'DVSYS', 'DVF', 'OLAPSYS',
	'REMOTE_SCHEDULER_AGENT', 'SI_INFORMTN_SCHEMA', 'ANONYMOUS', 'XS$NULL', 'DIP', 'SYSBACKUP', 'SYSDG',
	'SYSKM', 'SYSRAC', 'GGSYS', 'DBSFWUSER', 'PDBADMIN', 'EXFSYS', 'SPATIAL_CSW_ADMIN_USR', 'SPATIAL_WFS_ADMIN_USR'`

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

func oracleDSN(p config.Profile) (string, error) {
	if p.URL != "" {
		return p.URL, nil // oracle://user:password@host:1521/service
	}
	if p.Host == "" || p.Database == "" {
		return "", fmt.Errorf("profile %q: oracle needs host and database (service name, or SID with \"sid\": true)", p.Name)
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
		return "", fmt.Errorf("profile %q: unknown role %q (sysdba, sysoper)", p.Name, p.Role)
	}
	if len(opts) == 0 {
		opts = nil
	}
	return go_ora.BuildUrl(p.Host, port, service, p.User, p.Password, opts), nil
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
