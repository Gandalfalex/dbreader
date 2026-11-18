package all

import (
	"strings"
	"testing"

	"dbreader/internal/config"
	"dbreader/internal/driver"
)

func TestRegistry(t *testing.T) {
	if got := strings.Join(driver.Names(), ","); got != "mysql,oracle,postgres,sqlite" {
		t.Errorf("names = %s", got)
	}
	ps := []config.Profile{{Name: "a", Driver: "PostgreSQL"}, {Name: "b", Driver: "mariadb"}, {Name: "c"}}
	if err := driver.Resolve(ps); err != nil || ps[0].Driver != "postgres" || ps[1].Driver != "mysql" || ps[2].Driver != "" {
		t.Errorf("resolve: %v %+v", err, ps)
	}
	if err := driver.Resolve([]config.Profile{{Name: "x", Driver: "db2"}}); err == nil || !strings.Contains(err.Error(), "oracle") {
		t.Errorf("unknown driver: %v", err)
	}
	if d, ok := driver.ForDBeaver("mariaDB"); !ok || d.Name() != "mysql" {
		t.Errorf("dbeaver mariaDB: %v", ok)
	}
	if _, ok := driver.ForDBeaver("db2"); ok {
		t.Error("dbeaver db2 found")
	}
}

func TestLimit(t *testing.T) {
	pg, _ := driver.Lookup("postgres")
	ora, _ := driver.Lookup("oracle")
	if got := pg.Limit("SELECT 1", 5); got != "SELECT 1 LIMIT 5" {
		t.Error(got)
	}
	if got := ora.Limit("SELECT 1 FROM dual", 5); got != "SELECT * FROM (SELECT 1 FROM dual) WHERE ROWNUM <= 5" {
		t.Error(got)
	}
	my, _ := driver.Lookup("mysql")
	if got := my.Literal(`a\'b`); got != `'a\\''b'` {
		t.Error(got)
	}
}
