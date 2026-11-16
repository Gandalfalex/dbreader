// Package config loads database connection profiles from settings.json.
// The file is optional when a DBeaver workspace provides the connections.
package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	Postgres = "postgres"
	MySQL    = "mysql"
	SQLite   = "sqlite"
)

type Profile struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`             // postgres, mysql or sqlite
	URL      string `json:"url,omitempty"`      // full connection URL/DSN instead of the fields below; supports ${ENV_VAR}
	Host     string `json:"host,omitempty"`     // defaults to localhost
	Port     int    `json:"port,omitempty"`     // defaults to 5432 / 3306
	User     string `json:"user,omitempty"`     // supports ${ENV_VAR}
	Password string `json:"password,omitempty"` // supports ${ENV_VAR}
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema,omitempty"`   // optional: open this schema directly
	SSLMode  string `json:"sslMode,omitempty"`  // postgres sslmode / mysql tls (e.g. require, disable)
	Path     string `json:"path,omitempty"`     // sqlite database file
	ReadOnly bool   `json:"readOnly,omitempty"` // refuse every write, no confirmation offered
	Protect  bool   `json:"protect,omitempty"`  // every write needs the connection name typed (use for production)
	Limit    int    `json:"limit,omitempty"`    // rows fetched per query, default 500

	Source  string   `json:"-"` // "dbeaver" for connections read from a DBeaver workspace
	Blocked string   `json:"-"` // why the connection cannot be opened, e.g. it needs an SSH tunnel
	Scripts []Script `json:"-"` // SQL scripts linked to this connection

	// ScriptDir is where new scripts of this connection are created. Every
	// .sql file in it belongs to the connection.
	ScriptDir string `json:"-"`
}

// Script is a SQL file on disk.
type Script struct {
	Name string
	Path string
}

// DefaultLimit is the number of rows fetched per query unless a profile
// says otherwise.
const DefaultLimit = 500

type Settings struct {
	// DBeaver is the DBeaver data, workspace or project directory to read
	// connections, logins and scripts from. Empty looks in the default
	// location; "off" disables the link.
	DBeaver string `json:"dbeaver,omitempty"`
	// ReadOnlyDefault makes linked connections that have no entry under
	// profiles read-only, so nothing is writable unless listed as such.
	ReadOnlyDefault bool      `json:"readOnlyDefault,omitempty"`
	Profiles        []Profile `json:"profiles"`

	Scripts []Script `json:"-"` // scripts not linked to a connection
}

func Path(flagPath string) (string, error) {
	if flagPath != "" {
		return flagPath, nil
	}
	candidates := []string{"settings.json"}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "dbreader", "settings.json"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "dbreader", "settings.json"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("no settings.json found (looked in %v)", candidates)
}

func Load(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range s.Profiles {
		p := &s.Profiles[i]
		if p.Name == "" {
			p.Name = fmt.Sprintf("profile-%d", i+1)
		}
		switch p.Driver = strings.ToLower(p.Driver); p.Driver {
		case "postgresql", "pg", "pgx":
			p.Driver = Postgres
		case "mariadb":
			p.Driver = MySQL
		case "sqlite3":
			p.Driver = SQLite
		case Postgres, MySQL, SQLite:
		case "":
			// an entry that only sets flags for a linked (DBeaver) connection
			if p.URL != "" || p.Host != "" || p.Path != "" {
				return nil, fmt.Errorf("profile %q: driver missing (postgres, mysql, sqlite)", p.Name)
			}
		default:
			return nil, fmt.Errorf("profile %q: unknown driver %q (postgres, mysql, sqlite)", p.Name, p.Driver)
		}
		p.URL = expand(p.Name, p.URL)
		p.Host = expand(p.Name, p.Host)
		p.User = expand(p.Name, p.User)
		p.Password = expand(p.Name, p.Password)
		p.Path = expand(p.Name, p.Path)
		if rest, ok := strings.CutPrefix(p.Path, "~/"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				p.Path = filepath.Join(home, rest)
			}
		}
	}
	return &s, nil
}

// SafeName turns a connection name into a directory name.
func SafeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < ' ' || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	if name = strings.Trim(name, " ."); name == "" {
		return "_"
	}
	return name
}

// UseScriptDir makes dir the profile's script directory and picks up the
// scripts already in it.
func (p *Profile) UseScriptDir(dir string) {
	p.ScriptDir = dir
	known := map[string]bool{}
	for _, s := range p.Scripts {
		known[s.Path] = true
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || known[path] || !strings.EqualFold(filepath.Ext(path), ".sql") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		p.Scripts = append(p.Scripts, Script{Name: filepath.ToSlash(rel), Path: path})
		return nil
	})
}

// DataDir is where dbreader keeps scripts of connections that do not come
// from DBeaver: $XDG_DATA_HOME/dbreader (~/.local/share/dbreader).
func DataDir() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "dbreader")
}

// Target describes where the profile points, without credentials.
func (p Profile) Target() string {
	switch {
	case p.Driver == SQLite:
		return p.Path
	case p.URL != "":
		return "(url)"
	}
	host := p.Host
	if host == "" {
		host = "localhost"
	}
	if p.Port != 0 {
		host = fmt.Sprintf("%s:%d", host, p.Port)
	}
	return host + "/" + p.Database
}

// expand is os.ExpandEnv that warns about unset variables, which would
// otherwise silently turn into empty credentials.
func expand(profile, s string) string {
	return os.Expand(s, func(k string) string {
		v, ok := os.LookupEnv(k)
		if !ok {
			slog.Warn("env var not set", "profile", profile, "var", k)
		}
		return v
	})
}
