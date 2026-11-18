package config

import (
	"path/filepath"
	"testing"
)

func TestPins(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{"HR", "APP"} {
		if on, err := s.TogglePin("prod", schema); !on || err != nil {
			t.Fatalf("pin %s: %v, %v", schema, on, err)
		}
	}
	if on, _ := s.TogglePin("prod", "HR"); on {
		t.Fatal("second toggle left HR pinned")
	}

	again, err := LoadState()
	if err != nil || filepath.Base(again.path) != "state.json" {
		t.Fatalf("reload: %v", err)
	}
	if !again.IsPinned("prod", "APP") || again.IsPinned("prod", "HR") || again.IsPinned("dev", "APP") {
		t.Errorf("pins after reload = %v", again.Pinned)
	}
}
