// Package driver is the plugin point for database servers. Each server is
// a package that implements Driver and registers itself in init; package
// all lists the built-in ones. Everything that differs between servers
// lives behind this interface: how to connect, the catalog queries, how to
// read without being able to write, SQL quirks and value decoding.
package driver

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"

	"dbreader/internal/config"
	"dbreader/internal/sqlsafe"
)

type Driver interface {
	// Name is the canonical name used in settings.json ("postgres").
	Name() string
	// Aliases are other names accepted for it ("postgresql", "pg").
	Aliases() []string
	// DSN returns the database/sql driver name and data source for p.
	DSN(p config.Profile) (sqlDriver, dsn string, err error)
	// Dialect is how sqlsafe reads statements for this server.
	Dialect() *sqlsafe.Dialect
	Catalog() Catalog

	// ReadOnly runs fn on a connection the server keeps from changing data.
	ReadOnly(ctx context.Context, db *sql.DB, fn func(Querier) error) error
	// GuardReads reports that the server's read-only mode does not stop
	// every write, so the read path must refuse anything not plainly a read.
	GuardReads() bool
	// Retry reports errors that go away when a read is repeated a moment
	// later.
	Retry(err error) bool

	// Quote is the character identifiers are quoted with.
	Quote() string
	// Literal quotes a string value.
	Literal(s string) string
	// Limit restricts a SELECT to n rows.
	Limit(query string, n int) string
	// Contains is the condition "col contains pattern" (a quoted LIKE
	// pattern escaped with !) for a column of any type, ignoring case.
	Contains(col, pattern string) string

	// TypeName turns the type name the Go driver reports into the one
	// users of the server know, upper case.
	TypeName(name string) string
	// Format decodes values of driver-specific types; ok is false for the
	// ones the common formatting handles.
	Format(v any, typ string) (c Cell, ok bool)
}

// Cell is one value of a result, already formatted.
type Cell struct {
	S    string
	Null bool
}

// Querier is a connection or transaction that can run queries.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Catalog holds the queries that describe a database. They take the schema
// (and table) as positional arguments; when the server has a single
// namespace (Schemas is empty) the schema argument is left out.
type Catalog struct {
	// Schemas lists name, number of tables and views, and 1 for schemas
	// the server itself owns, system schemas last.
	Schemas string
	// SchemasOld replaces Schemas on server versions too old for it.
	SchemasOld string
	// Current returns the schema unqualified names resolve to.
	Current string
	// Tables lists name, kind (table, view, ...) and estimated rows (-1
	// when unknown).
	Tables string
	// Columns lists name, type, nullable, default and primary key.
	Columns string
	// ColNames lists table and column name of every column, in order.
	ColNames string
	// SearchNames is ColNames restricted to columns text can be searched
	// in; empty when all of them can.
	SearchNames string
}

// Base gives the behaviour most servers share; drivers embed it and
// override what differs.
type Base struct{}

// ReadOnly runs fn in a transaction opened read-only.
func (Base) ReadOnly(ctx context.Context, db *sql.DB, fn func(Querier) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

func (Base) Aliases() []string { return nil }
func (Base) GuardReads() bool  { return false }
func (Base) Retry(error) bool  { return false }
func (Base) Quote() string     { return `"` }
func (Base) Literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
func (Base) Limit(query string, n int) string { return query + " LIMIT " + strconv.Itoa(n) }
func (Base) TypeName(name string) string      { return strings.ToUpper(name) }
func (Base) Format(any, string) (Cell, bool)  { return Cell{}, false }

// HostPort joins the profile's host (default localhost) and port.
func HostPort(p config.Profile, defPort int) string {
	host, port := p.Host, p.Port
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		port = defPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

var (
	mu      sync.RWMutex
	drivers = map[string]Driver{} // by name and alias
)

// Register makes d available under its name and aliases. It panics on a
// name taken twice, which is a programming error.
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	for _, n := range append([]string{d.Name()}, d.Aliases()...) {
		n = strings.ToLower(n)
		if _, dup := drivers[n]; dup {
			panic("driver: " + n + " registered twice")
		}
		drivers[n] = d
	}
}

// Lookup finds a driver by name or alias, ignoring case.
func Lookup(name string) (Driver, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := drivers[strings.ToLower(name)]
	return d, ok
}

// Names lists the canonical names of the registered drivers.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, d := range drivers {
		if !seen[d.Name()] {
			seen[d.Name()] = true
			out = append(out, d.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Resolve checks the driver of every profile and replaces aliases with the
// canonical name. Profiles without a driver only set flags for a linked
// (DBeaver) connection and are left alone.
func Resolve(profiles []config.Profile) error {
	for i := range profiles {
		p := &profiles[i]
		if p.Driver == "" {
			continue
		}
		d, ok := Lookup(p.Driver)
		if !ok {
			return fmt.Errorf("profile %q: unknown driver %q (%s)", p.Name, p.Driver, strings.Join(Names(), ", "))
		}
		p.Driver = d.Name()
	}
	return nil
}

// DBeaverConn is what a DBeaver connection says about its target beyond
// host, port, user and database.
type DBeaverConn struct {
	URL       string            // the JDBC URL
	AuthModel string            // how it logs on; "" or "native" for user and password
	Props     map[string]string // provider and auth properties, keys lower case
}

// DBeaver is implemented by drivers that take connections from a DBeaver
// workspace.
type DBeaver interface {
	Driver
	// DBeaverProviders are the DBeaver provider ids the driver serves.
	DBeaverProviders() []string
	// FromDBeaver completes p, which has host, port, user and database
	// from DBeaver's fields, and reports whether it set the target itself;
	// otherwise it is read from a host-style JDBC URL.
	FromDBeaver(c DBeaverConn, p *config.Profile) bool
}

func (Base) FromDBeaver(DBeaverConn, *config.Profile) bool { return false }

// ForDBeaver finds the driver for a DBeaver provider id.
func ForDBeaver(provider string) (DBeaver, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, d := range drivers {
		if db, ok := d.(DBeaver); ok {
			for _, id := range db.DBeaverProviders() {
				if strings.EqualFold(id, provider) {
					return db, true
				}
			}
		}
	}
	return nil, false
}
