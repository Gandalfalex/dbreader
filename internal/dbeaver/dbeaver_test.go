package dbeaver

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"os"
	"path/filepath"
	"testing"

	"dbreader/internal/config"
)

// encrypt writes data the way DBeaver stores credentials-config.json.
func encrypt(t *testing.T, plain string) []byte {
	t.Helper()
	block, err := aes.NewCipher(credentialsKey)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	data := append([]byte(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := bytes.Repeat([]byte{7}, aes.BlockSize)
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	return append(iv, out...)
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// workspace builds DBeaverData/workspace6/General as DBeaver lays it out.
func workspace(t *testing.T) (root, project string) {
	t.Helper()
	root = t.TempDir()
	project = filepath.Join(root, "workspace6", "General")
	write(t, filepath.Join(project, ".dbeaver", "data-sources.json"), []byte(`{
		"folders": {},
		"connections": {
			"postgres-jdbc-1": {
				"provider": "postgresql", "driver": "postgres-jdbc", "name": "shop prod",
				"save-password": true, "read-only": false,
				"configuration": {
					"host": "db.example.com", "port": "5433", "database": "shop",
					"url": "jdbc:postgresql://db.example.com:5433/shop", "type": "prod",
					"handlers": {"postgre_ssl": {"type": "CONFIG", "enabled": true, "properties": {"sslMode": "verify-full"}}}
				}
			},
			"mysql8-2": {
				"provider": "mysql", "driver": "mysql8", "name": "analytics", "read-only": true,
				"configuration": {"url": "jdbc:mysql://10.0.0.5:3307/stats", "configurationType": "URL", "type": "dev", "user": "olduser"}
			},
			"sqlite_jdbc-3": {
				"provider": "sqlite", "driver": "sqlite_jdbc", "name": "local file",
				"configuration": {"database": "/data/app.db", "url": "jdbc:sqlite:/data/app.db"}
			},
			"postgres-jdbc-4": {
				"provider": "postgresql", "driver": "postgres-jdbc", "name": "behind bastion",
				"configuration": {"host": "10.1.1.1", "port": "5432", "database": "x",
					"handlers": {"ssh_tunnel": {"type": "SSH", "enabled": true, "properties": {"host": "bastion"}}}}
			},
			"oracle_thin-5": {
				"provider": "oracle", "driver": "oracle_thin", "name": "legacy",
				"configuration": {"host": "ora", "port": "1521", "database": "ORCL"}
			}
		}
	}`))
	write(t, filepath.Join(project, ".dbeaver", "credentials-config.json"), encrypt(t, `{
		"postgres-jdbc-1": {"#connection": {"user": "shop_rw", "password": "s3cr$t ${HOME}"}},
		"mysql8-2": {"#connection": {"user": "reader", "password": "pw"}},
		"postgres-jdbc-4": {"#connection": {"user": "u", "password": "p"}, "network/ssh_tunnel": {"user": "jump", "password": "j"}}
	}`))
	write(t, filepath.Join(project, ".dbeaver", "project-metadata.json"), []byte(`{"resources": {
		"Scripts/orders.sql": {"default-datasource": "postgres-jdbc-1", "default-schema": "public"},
		"Scripts/reports/monthly.sql": {"default-datasource": "postgres-jdbc-1"},
		"Scripts/stats.sql": {"default-datasource": "mysql8-2"},
		"Scripts/gone.sql": {"default-datasource": "deleted-connection-9"}
	}}`))
	for _, f := range []string{"orders.sql", "reports/monthly.sql", "stats.sql", "gone.sql", "scratch.sql", "notes.txt", "analytics/by dbreader.sql"} {
		write(t, filepath.Join(project, "Scripts", f), []byte("select 1;"))
	}
	write(t, filepath.Join(project, ".dbeaver", "hidden.sql"), []byte("select 1;"))
	return root, project
}

func names(scripts []config.Script) (out []string) {
	for _, s := range scripts {
		out = append(out, s.Name)
	}
	return out
}

func TestLoad(t *testing.T) {
	root, project := workspace(t)
	// the data directory, the workspace and the project all resolve
	for _, dir := range []string{root, filepath.Join(root, "workspace6"), project} {
		ws, err := Load(dir)
		if err != nil {
			t.Fatalf("Load(%s): %v", dir, err)
		}
		if len(ws.Profiles) != 4 {
			t.Fatalf("Load(%s): %d profiles, want 4", dir, len(ws.Profiles))
		}
	}

	ws, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]config.Profile{}
	for _, p := range ws.Profiles {
		by[p.Name] = p
	}

	pg := by["shop prod"]
	if pg.Driver != config.Postgres || pg.Host != "db.example.com" || pg.Port != 5433 || pg.Database != "shop" {
		t.Errorf("postgres target = %+v", pg)
	}
	// the password is taken literally: no ${ENV} expansion on DBeaver logins
	if pg.User != "shop_rw" || pg.Password != "s3cr$t ${HOME}" {
		t.Errorf("postgres login = %q / %q", pg.User, pg.Password)
	}
	if !pg.Protect || pg.ReadOnly || pg.SSLMode != "verify-full" || pg.Source != "dbeaver" {
		t.Errorf("postgres flags = %+v", pg)
	}
	if got := names(pg.Scripts); len(got) != 2 || got[0] != "orders.sql" || got[1] != "reports/monthly.sql" {
		t.Errorf("postgres scripts = %v", got)
	}

	my := by["analytics"]
	if my.Driver != config.MySQL || my.Host != "10.0.0.5" || my.Port != 3307 || my.Database != "stats" {
		t.Errorf("mysql target from jdbc url = %+v", my)
	}
	if my.User != "reader" || my.Password != "pw" || !my.ReadOnly || my.Protect {
		t.Errorf("mysql = %+v", my)
	}
	// linked in DBeaver, plus the one in the connection's own folder
	if got := names(my.Scripts); len(got) != 2 || got[0] != "stats.sql" || got[1] != "analytics/by dbreader.sql" {
		t.Errorf("mysql scripts = %v", got)
	}
	if want := filepath.Join(project, "Scripts", "analytics"); my.ScriptDir != want {
		t.Errorf("script dir = %q, want %q", my.ScriptDir, want)
	}

	if lite := by["local file"]; lite.Driver != config.SQLite || lite.Path != "/data/app.db" || lite.Password != "" {
		t.Errorf("sqlite = %+v", lite)
	}
	if ssh := by["behind bastion"]; ssh.Blocked == "" {
		t.Errorf("ssh tunnel connection not blocked: %+v", ssh)
	}
	if len(ws.Skipped) != 1 || ws.Skipped[0] != "legacy (oracle)" {
		t.Errorf("skipped = %v", ws.Skipped)
	}
	if got := names(ws.Scripts); len(got) != 2 || got[0] != "scratch.sql" || got[1] != "gone.sql" {
		t.Errorf("unlinked scripts = %v", got)
	}
}

func TestUnreadableCredentials(t *testing.T) {
	root, project := workspace(t)
	write(t, filepath.Join(project, ".dbeaver", "credentials-config.json"), []byte("locked with a master password"))
	ws, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ws.Profiles {
		if p.Password != "" {
			t.Errorf("%s has a password without readable credentials", p.Name)
		}
	}
	if len(ws.Profiles) != 4 {
		t.Errorf("profiles = %d, want 4", len(ws.Profiles))
	}
}

func TestFindNothing(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("Load of an empty directory succeeded")
	}
}
