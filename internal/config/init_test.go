package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	linked := []Profile{{Name: "dev", Password: "secret"}, {Name: "prod", Protect: true, Password: "secret"}}

	added, err := Init(path, linked)
	if err != nil || len(added) != 2 {
		t.Fatalf("added = %v, err = %v", added, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "secret") {
		t.Fatalf("a login was written to the settings file:\n%s", data)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ReadOnlyDefault || len(s.Profiles) != 2 {
		t.Fatalf("settings = %+v", s)
	}
	// everything starts read-only; DBeaver's production type carries over
	if dev, prod := s.Profiles[0], s.Profiles[1]; !dev.ReadOnly || dev.Protect || !prod.ReadOnly || !prod.Protect {
		t.Errorf("flags = %+v, %+v", dev, prod)
	}
	if !strings.Contains(string(data), `"protect": false`) {
		t.Errorf("flags must be spelled out to be edited:\n%s", data)
	}

	// the user edits the file; a later init only appends what is new
	edited := strings.Replace(string(data), `"name": "dev",
      "readOnly": true`, `"name": "dev", "password": "${DEV_PW}", "limit": 50,
      "readOnly": false`, 1)
	if edited == string(data) {
		t.Fatal("test edit did not apply")
	}
	os.WriteFile(path, []byte(edited), 0o600)
	added, err = Init(path, append(linked, Profile{Name: "new one"}))
	if err != nil || len(added) != 1 || added[0] != "new one" {
		t.Fatalf("added = %v, err = %v", added, err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "${DEV_PW}") || !strings.Contains(string(data), `"limit": 50`) {
		t.Errorf("existing entry was rewritten:\n%s", data)
	}
	if s, _ = Load(path); len(s.Profiles) != 3 || s.Profiles[0].ReadOnly || !s.Profiles[2].ReadOnly {
		t.Errorf("after second init: %+v", s.Profiles)
	}

	before, _ := os.ReadFile(path)
	if added, err = Init(path, linked); err != nil || added != nil {
		t.Errorf("third init: %v, %v", added, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("init rewrote a file that was complete")
	}
}

func TestOverlay(t *testing.T) {
	linked := Profile{Name: "db", Driver: Postgres, Host: "h", Port: 5433, Database: "d", User: "u", Password: "p",
		Protect: true, Source: "dbeaver", Scripts: []Script{{Name: "a.sql"}}, ScriptDir: "/s"}

	got := Overlay(Profile{Name: "db", ReadOnly: true}, linked)
	if !got.ReadOnly || got.Protect {
		t.Errorf("flags come from the settings entry: %+v", got)
	}
	if got.Host != "h" || got.Password != "p" || got.Driver != Postgres || len(got.Scripts) != 1 || got.ScriptDir != "/s" {
		t.Errorf("connection must come from the linked profile: %+v", got)
	}

	got = Overlay(Profile{Name: "db", Driver: Postgres, Host: "other", User: "me", Limit: 10}, linked)
	if got.Host != "other" || got.Port != 0 || got.User != "me" || got.Password != "p" || got.Limit != 10 {
		t.Errorf("own target and login win: %+v", got)
	}
}

func TestLoadFlagOnlyEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"profiles": [{"name": "x", "readOnly": true}]}`), 0o600)
	if _, err := Load(path); err != nil {
		t.Errorf("flag-only entry rejected: %v", err)
	}
	os.WriteFile(path, []byte(`{"profiles": [{"name": "x", "host": "h"}]}`), 0o600)
	if _, err := Load(path); err == nil {
		t.Error("entry with a host but no driver accepted")
	}
}
