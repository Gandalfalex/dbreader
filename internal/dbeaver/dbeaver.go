// Package dbeaver reads a DBeaver workspace in place: its connections, the
// saved logins and the SQL scripts linked to each connection. Nothing is
// copied or written; what DBeaver changes shows up on the next start.
package dbeaver

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"dbreader/internal/config"
)

// Workspace is what was found in one DBeaver data directory.
type Workspace struct {
	Dir      string
	Profiles []config.Profile
	Scripts  []config.Script // scripts not linked to any connection
	Skipped  []string        // connections with drivers dbreader has no support for
}

// DefaultDirs lists where DBeaver keeps its data on this system.
func DefaultDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var dirs []string
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		dirs = append(dirs, filepath.Join(d, "DBeaverData"))
	}
	switch runtime.GOOS {
	case "darwin":
		dirs = append(dirs, filepath.Join(home, "Library", "DBeaverData"))
	case "windows":
		if d := os.Getenv("APPDATA"); d != "" {
			dirs = append(dirs, filepath.Join(d, "DBeaverData"))
		}
	}
	return append(dirs,
		filepath.Join(home, ".local", "share", "DBeaverData"),
		filepath.Join(home, "snap", "dbeaver-ce", "current", ".local", "share", "DBeaverData"),
		filepath.Join(home, ".var", "app", "io.dbeaver.DBeaverCommunity", "data", "DBeaverData"),
	)
}

// Find loads the workspace at dir, or the first default location that holds
// one when dir is empty. It returns nil, nil when there is none to find.
func Find(dir string) (*Workspace, error) {
	if dir != "" {
		return Load(dir)
	}
	for _, d := range DefaultDirs() {
		if len(projects(d)) > 0 {
			return Load(d)
		}
	}
	return nil, nil
}

const dataSources = ".dbeaver/data-sources.json"

// projects returns the project directories under dir, which may be the
// DBeaverData directory, a workspace in it, or a project itself.
func projects(dir string) []string {
	if _, err := os.Stat(filepath.Join(dir, dataSources)); err == nil {
		return []string{dir}
	}
	for _, pattern := range []string{"*", "workspace6/*", "workspace*/*"} {
		hits, _ := filepath.Glob(filepath.Join(dir, pattern, dataSources))
		if len(hits) > 0 {
			sort.Strings(hits)
			out := make([]string, len(hits))
			for i, h := range hits {
				out[i] = filepath.Dir(filepath.Dir(h))
			}
			return out
		}
	}
	return nil
}

// Load reads every project under dir.
func Load(dir string) (*Workspace, error) {
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, rest)
		}
	}
	ps := projects(dir)
	if len(ps) == 0 {
		return nil, fmt.Errorf("no DBeaver project (%s) under %s", dataSources, dir)
	}
	ws := &Workspace{Dir: dir}
	for _, p := range ps {
		if err := ws.loadProject(p); err != nil {
			return nil, err
		}
	}
	return ws, nil
}

type dataSourcesFile struct {
	Connections map[string]connection `json:"connections"`
}

type connection struct {
	Provider string `json:"provider"`
	Driver   string `json:"driver"`
	Name     string `json:"name"`
	ReadOnly bool   `json:"read-only"`
	Config   struct {
		Host      string             `json:"host"`
		Port      string             `json:"port"`
		Database  string             `json:"database"`
		URL       string             `json:"url"`
		User      string             `json:"user"`
		Type      string             `json:"type"` // dev, test, prod
		Handlers  map[string]handler `json:"handlers"`
		AuthModel string             `json:"auth-model"`
		Provider  map[string]any     `json:"provider-properties"`
		Auth      map[string]any     `json:"auth-properties"`
	} `json:"configuration"`
}

type handler struct {
	Type       string         `json:"type"`
	Enabled    bool           `json:"enabled"`
	Properties map[string]any `json:"properties"`
}

// credentials is the decrypted credentials-config.json: connection id →
// section → key → value. The login is in the "#connection" section.
type credentials map[string]map[string]map[string]any

type metadata struct {
	Resources map[string]struct {
		DataSource string `json:"default-datasource"`
	} `json:"resources"`
}

func (ws *Workspace) loadProject(dir string) error {
	var ds dataSourcesFile
	if err := readJSON(filepath.Join(dir, dataSources), &ds); err != nil {
		return err
	}
	creds, err := readCredentials(filepath.Join(dir, ".dbeaver", "credentials-config.json"))
	if err != nil {
		// e.g. a master password is set; connections still load, without logins
		slog.Warn("dbeaver: saved logins not readable", "project", dir, "err", err)
	}
	scripts := projectScripts(dir)

	ids := make([]string, 0, len(ds.Connections))
	for id := range ds.Connections {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ds.Connections[ids[i]], ds.Connections[ids[j]]
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	for _, id := range ids {
		c := ds.Connections[id]
		p, ok := c.profile()
		if !ok {
			ws.Skipped = append(ws.Skipped, fmt.Sprintf("%s (%s)", c.Name, c.Provider))
			delete(scripts, id)
			continue
		}
		if login := creds[id]["#connection"]; login != nil {
			if u, _ := login["user"].(string); u != "" {
				p.User = u
			}
			p.Password, _ = login["password"].(string)
		}
		p.Scripts = scripts[id]
		delete(scripts, id)
		// Scripts dbreader creates go to Scripts/<connection>/. DBeaver shows
		// them as ordinary, unlinked scripts; here the folder is the link.
		folder := config.SafeName(p.Name)
		p.ScriptDir = filepath.Join(dir, "Scripts", folder)
		var rest []config.Script
		for _, s := range scripts[""] {
			if strings.HasPrefix(s.Name, folder+"/") {
				p.Scripts = append(p.Scripts, s)
			} else {
				rest = append(rest, s)
			}
		}
		scripts[""] = rest
		ws.Profiles = append(ws.Profiles, p)
	}
	// what is left is unlinked, or linked to a connection that is gone
	for _, id := range sortedKeys(scripts) {
		ws.Scripts = append(ws.Scripts, scripts[id]...)
	}
	return nil
}

// profile maps a DBeaver connection to a dbreader profile.
func (c connection) profile() (config.Profile, bool) {
	p := config.Profile{Name: c.Name, Source: "dbeaver", ReadOnly: c.ReadOnly}
	switch c.Provider {
	case "postgresql":
		p.Driver = config.Postgres
	case "mysql", "mariaDB", "mariadb":
		p.Driver = config.MySQL
	case "sqlite":
		p.Driver = config.SQLite
	case "oracle":
		p.Driver = config.Oracle
	default:
		return p, false
	}
	cfg := c.Config
	p.Host, p.User, p.Database = cfg.Host, cfg.User, cfg.Database
	p.Port, _ = strconv.Atoi(cfg.Port)
	p.Protect = cfg.Type == "prod" // DBeaver's "Production" connection type
	switch {
	case p.Driver == config.Oracle:
		props := map[string]string{}
		for _, m := range []map[string]any{cfg.Provider, cfg.Auth} {
			for k, v := range m {
				if str, ok := v.(string); ok {
					props[strings.ToLower(k)] = str
				}
			}
		}
		oracleTarget(cfg.URL, props, &p)
		// wallet, Kerberos and OS logins have no password dbreader could use
		if m := cfg.AuthModel; m != "" && m != "native" && m != "oracle_native" && p.Blocked == "" {
			p.Blocked = "it logs on with " + m + ", and dbreader only does user and password"
		}
	case p.Host == "" && p.Driver != config.SQLite:
		jdbcURL(cfg.URL, &p)
	}
	if p.Driver == config.SQLite {
		p.Path, p.Database = cfg.Database, ""
		if p.Path == "" {
			p.Path = strings.TrimPrefix(cfg.URL, "jdbc:sqlite:")
		}
	}
	for name, h := range cfg.Handlers {
		switch {
		case !h.Enabled:
		case h.Type == "SSH" || h.Type == "PROXY":
			p.Blocked = "it connects through an " + strings.ReplaceAll(name, "_", " ") + ", which dbreader cannot open yet"
		case strings.HasSuffix(name, "_ssl"):
			p.SSLMode, _ = h.Properties["sslMode"].(string)
			if p.SSLMode == "" {
				p.SSLMode = "require"
			}
		}
	}
	return p, true
}

var (
	oracleThin = regexp.MustCompile(`(?i)^jdbc:oracle:thin:@(?:(tcps?):)?(?://)?([^:/]+|\[[^\]]+\])(?::(\d+))?(?:[/:](.*))?$`)
	tnsHost    = regexp.MustCompile(`(?i)\(\s*HOST\s*=\s*([^)\s]+)\s*\)`)
	tnsPort    = regexp.MustCompile(`(?i)\(\s*PORT\s*=\s*(\d+)\s*\)`)
	tnsService = regexp.MustCompile(`(?i)\(\s*SERVICE_NAME\s*=\s*([^)\s]+)\s*\)`)
	tnsSID     = regexp.MustCompile(`(?i)\(\s*SID\s*=\s*([^)\s]+)\s*\)`)
	tnsProto   = regexp.MustCompile(`(?i)\(\s*PROTOCOL\s*=\s*(\w+)\s*\)`)
)

// oracleTarget reads host, port and service or SID from the JDBC URL, which
// DBeaver always writes, in whichever form it has:
//
//	jdbc:oracle:thin:@//host:1521/SERVICE
//	jdbc:oracle:thin:@host:1521/SERVICE
//	jdbc:oracle:thin:@host:1521:SID
//	jdbc:oracle:thin:@(DESCRIPTION=(ADDRESS=(HOST=h)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=s)))
//	jdbc:oracle:thin:@ALIAS          resolved through tnsnames.ora
//
// props are DBeaver's provider and auth properties, keys lower-cased: they
// say whether the name is a SID, where tnsnames.ora is, and the logon role.
func oracleTarget(raw string, props map[string]string, p *config.Profile) {
	for k, v := range props {
		switch up := strings.ToUpper(v); {
		case (strings.Contains(k, "logon") || strings.Contains(k, "role")) && (up == "SYSDBA" || up == "SYSOPER"):
			p.Role = strings.ToLower(v)
		}
	}
	raw = strings.TrimSpace(raw)
	at := raw[strings.Index(raw, "@")+1:]
	isAlias := !strings.ContainsAny(at, ":/(") && at != ""

	if isAlias {
		tnsDir := ""
		for k, v := range props {
			if strings.Contains(k, "tns") && (strings.Contains(k, "path") || strings.Contains(k, "admin")) {
				tnsDir = v
			}
		}
		desc, ok := lookupTNS(at, tnsDir)
		if !ok {
			p.Host, p.Database = "", at
			p.Blocked = "its TNS alias " + at + " is in no tnsnames.ora dbreader can find (set TNS_ADMIN to the directory holding it)"
			return
		}
		at = desc
	}
	if strings.HasPrefix(at, "(") {
		// several addresses (RAC, failover): the first one is used
		if m := tnsHost.FindStringSubmatch(at); m != nil {
			p.Host = m[1]
		}
		if m := tnsPort.FindStringSubmatch(at); m != nil {
			p.Port, _ = strconv.Atoi(m[1])
		}
		if m := tnsService.FindStringSubmatch(at); m != nil {
			p.Database, p.SID = m[1], false
		} else if m := tnsSID.FindStringSubmatch(at); m != nil {
			p.Database, p.SID = m[1], true
		}
		if m := tnsProto.FindStringSubmatch(at); m != nil && strings.EqualFold(m[1], "tcps") {
			p.SSLMode = "require"
		}
		if p.Host == "" || p.Database == "" {
			p.Blocked = "its TNS descriptor has no HOST or SERVICE_NAME/SID dbreader can read"
		}
		return
	}
	m := oracleThin.FindStringSubmatch(raw)
	if m == nil {
		// no usable URL: the host, port and database fields, if DBeaver wrote them
		p.SID = strings.EqualFold(props["@dbeaver-sid-service@"], "SID")
		if p.Host == "" || p.Database == "" {
			p.Blocked = "its connection URL (" + raw + ") is not in a form dbreader understands"
		}
		return
	}
	p.Host = strings.Trim(m[2], "[]")
	if m[3] != "" {
		p.Port, _ = strconv.Atoi(m[3])
	}
	if strings.EqualFold(m[1], "tcps") {
		p.SSLMode = "require"
	}
	switch rest := m[4]; {
	case rest == "":
		p.Blocked = "its URL names no service or SID"
	case strings.Contains(raw, "@//") || strings.Contains(raw, "/"+rest):
		p.Database, p.SID = rest, false // host:port/service
	default:
		p.Database, p.SID = rest, true // host:port:SID
	}
}

// jdbcURL fills host, port and database from "jdbc:postgresql://host:5432/db".
func jdbcURL(raw string, p *config.Profile) {
	u, err := url.Parse(strings.TrimPrefix(raw, "jdbc:"))
	if err != nil || u.Host == "" {
		return
	}
	p.Host = u.Hostname()
	p.Port, _ = strconv.Atoi(u.Port())
	if p.Database == "" {
		p.Database = strings.TrimPrefix(u.Path, "/")
	}
}

// projectScripts finds the project's SQL scripts, grouped by the id of the
// connection DBeaver linked them to; "" holds the unlinked ones.
func projectScripts(dir string) map[string][]config.Script {
	var meta metadata
	if err := readJSON(filepath.Join(dir, ".dbeaver", "project-metadata.json"), &meta); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("dbeaver: script links not readable", "project", dir, "err", err)
	}
	out := map[string][]config.Script{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".sql") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		id := meta.Resources[rel].DataSource
		out[id] = append(out[id], config.Script{Name: strings.TrimPrefix(rel, "Scripts/"), Path: path})
		return nil
	})
	return out
}

// credentialsKey is the fixed key DBeaver encrypts credentials-config.json
// with (it is in DBeaver's source). The file is protected by its
// permissions, not by this key.
var credentialsKey = []byte{0xba, 0xbb, 0x4a, 0x9f, 0x77, 0x4a, 0xb8, 0x53, 0xc9, 0x6c, 0x2d, 0x65, 0x3d, 0xfe, 0x54, 0x4a}

func readCredentials(path string) (credentials, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no saved logins
	}
	if err != nil {
		return nil, err
	}
	plain, err := decrypt(data)
	if err != nil {
		return nil, err
	}
	var c credentials
	if err := json.Unmarshal(plain, &c); err != nil {
		return nil, errors.New("not in the expected format (master password set?)")
	}
	return c, nil
}

// decrypt undoes AES-128-CBC with PKCS#7 padding; the IV leads the file.
func decrypt(data []byte) ([]byte, error) {
	const bs = aes.BlockSize
	if len(data) < 2*bs || len(data)%bs != 0 {
		return nil, errors.New("not in the expected format (master password set?)")
	}
	block, err := aes.NewCipher(credentialsKey)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(data)-bs)
	cipher.NewCBCDecrypter(block, data[:bs]).CryptBlocks(plain, data[bs:])
	n := int(plain[len(plain)-1])
	if n < 1 || n > bs || n > len(plain) {
		return nil, errors.New("not in the expected format (master password set?)")
	}
	return plain[:len(plain)-n], nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
