package oracle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dbreader/internal/config"
	"dbreader/internal/db"
)

const tnsnames = `# corporate tnsnames.ora
DEV =
  (DESCRIPTION =
    (ADDRESS = (PROTOCOL = TCP)(HOST = dev-db.example.com)(PORT = 1521))
    (CONNECT_DATA = (SERVER = DEDICATED) (SERVICE_NAME = devsvc))
  )

# HOST = commented.example.com
PROD.example.com, PRODALIAS =
  (DESCRIPTION =
    (ADDRESS_LIST =
      (ADDRESS = (PROTOCOL = TCPS)(HOST = rac1.example.com)(PORT = 2484))
      (ADDRESS = (PROTOCOL = TCPS)(HOST = rac2.example.com)(PORT = 2484))
    )
    (CONNECT_DATA = (SERVICE_NAME = prod.example.com))
  )
legacy=(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=old)(PORT=1522))(CONNECT_DATA=(SID=ORCL)))
`

func TestOracleTargetTNSAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tnsnames.ora"), []byte(tnsnames), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TNS_ADMIN", dir)

	tests := []struct {
		url  string
		want config.Profile
	}{
		{"jdbc:oracle:thin:@DEV", config.Profile{Host: "dev-db.example.com", Port: 1521, Database: "devsvc"}},
		{"jdbc:oracle:thin:@dev", config.Profile{Host: "dev-db.example.com", Port: 1521, Database: "devsvc"}},
		{"jdbc:oracle:thin:@PROD", config.Profile{Host: "rac1.example.com", Port: 2484, Database: "prod.example.com", SSLMode: "require"}},
		{"jdbc:oracle:thin:@prodalias", config.Profile{Host: "rac1.example.com", Port: 2484, Database: "prod.example.com", SSLMode: "require"}},
		{"jdbc:oracle:thin:@LEGACY", config.Profile{Host: "old", Port: 1522, Database: "ORCL", SID: true}},
	}
	for _, tt := range tests {
		var p config.Profile
		target(tt.url, nil, &p)
		if p.Blocked != "" || p.Host != tt.want.Host || p.Port != tt.want.Port || p.Database != tt.want.Database || p.SID != tt.want.SID || p.SSLMode != tt.want.SSLMode {
			t.Errorf("%s: got %+v", tt.url, p)
		}
	}

	var p config.Profile
	target("jdbc:oracle:thin:@NOPE", nil, &p)
	if p.Blocked == "" {
		t.Errorf("unknown alias not blocked: %+v", p)
	}

	// DBeaver can name the tnsnames.ora directory itself
	t.Setenv("TNS_ADMIN", "")
	p = config.Profile{}
	target("jdbc:oracle:thin:@DEV", map[string]string{"@dbeaver-tns-path@": dir}, &p)
	if p.Host != "dev-db.example.com" {
		t.Errorf("tns path property ignored: %+v", p)
	}
}

func TestOracleRole(t *testing.T) {
	var p config.Profile
	target("jdbc:oracle:thin:@//h:1521/s", map[string]string{"@dbeaver-internal-logon@": "SYSDBA", "@dbeaver-sid-service@": "SERVICE"}, &p)
	if p.Role != "sysdba" || p.SID {
		t.Errorf("role = %q, sid = %v", p.Role, p.SID)
	}
	p = config.Profile{}
	target("jdbc:oracle:thin:@//h:1521/s", map[string]string{"@dbeaver-internal-logon@": "Normal"}, &p)
	if p.Role != "" {
		t.Errorf("role = %q for a normal logon", p.Role)
	}
}

// TestOracleAliasConnects resolves an alias from tnsnames.ora and logs on,
// also as SYSDBA: DBREADER_TEST_ORACLE_SYS=<sys password>, container on 1521.
func TestOracleAliasConnects(t *testing.T) {
	sys := os.Getenv("DBREADER_TEST_ORACLE_SYS")
	if sys == "" {
		t.Skip("DBREADER_TEST_ORACLE_SYS not set")
	}
	dir := t.TempDir()
	entry := "LOCALFREE = (DESCRIPTION = (ADDRESS = (PROTOCOL = TCP)(HOST = 127.0.0.1)(PORT = 1521)) (CONNECT_DATA = (SERVICE_NAME = FREEPDB1)))\n"
	os.WriteFile(filepath.Join(dir, "tnsnames.ora"), []byte(entry), 0o600)
	t.Setenv("TNS_ADMIN", dir)

	p := config.Profile{Name: "sys", Driver: Name, User: "sys", Password: sys}
	target("jdbc:oracle:thin:@localfree", map[string]string{"@dbeaver-internal-logon@": "SYSDBA"}, &p)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := db.Open(ctx, p)
	if err != nil {
		t.Fatalf("profile %+v: %v", config.Profile{Host: p.Host, Port: p.Port, Database: p.Database, Role: p.Role}, err)
	}
	defer c.Close()
	res, err := c.Query(ctx, "SELECT sys_context('USERENV', 'SESSION_USER'), sys_context('USERENV', 'ISDBA') FROM dual", 1)
	if err != nil || res.Rows[0][0].S != "SYS" || res.Rows[0][1].S != "TRUE" {
		t.Fatalf("session = %+v, err = %v", res, err)
	}
	// (the driver logs SYS on as SYSDBA by itself, so the role option cannot
	// be told apart here; it matters for other users granted SYSDBA)
}

// The URL alone must be enough: DBeaver's "URL" connection type writes no
// host, port or database fields.
func TestOracleTargetURLForms(t *testing.T) {
	tests := []struct {
		url           string
		host, db, ssl string
		port          int
		sid           bool
	}{
		{"jdbc:oracle:thin:@//h:1521/svc", "h", "svc", "", 1521, false},
		{"jdbc:oracle:thin:@//db.example.com/svc.example.com", "db.example.com", "svc.example.com", "", 0, false},
		{"jdbc:oracle:thin:@h:1521/svc", "h", "svc", "", 1521, false},
		{"jdbc:oracle:thin:@h:1522:ORCL", "h", "ORCL", "", 1522, true},
		{"jdbc:oracle:thin:@tcps://h:2484/svc", "h", "svc", "require", 2484, false},
		{"jdbc:oracle:thin:@tcp://h:1521/svc", "h", "svc", "", 1521, false},
		{"JDBC:ORACLE:THIN:@//H:1521/SVC", "H", "SVC", "", 1521, false},
		{"jdbc:oracle:thin:@(description=(address=(protocol=tcp)(host=h)(port=1521))(connect_data=(sid=X)))", "h", "X", "", 1521, true},
	}
	for _, tt := range tests {
		var p config.Profile
		target(tt.url, nil, &p)
		if p.Blocked != "" || p.Host != tt.host || p.Database != tt.db || p.Port != tt.port || p.SID != tt.sid || p.SSLMode != tt.ssl {
			t.Errorf("%s: got host=%q db=%q port=%d sid=%v ssl=%q blocked=%q", tt.url, p.Host, p.Database, p.Port, p.SID, p.SSLMode, p.Blocked)
		}
	}
}
