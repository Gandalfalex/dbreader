package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// State is what dbreader remembers between runs, kept in
// DataDir()/state.json. It is not meant to be edited by hand.
type State struct {
	// Pinned are the schemas pinned per connection; they are listed first
	// and never hidden.
	Pinned map[string][]string `json:"pinnedSchemas,omitempty"`

	path string
}

// LoadState reads the state file. A missing or unreadable file gives an
// empty state, so a broken file never keeps dbreader from starting.
func LoadState() (*State, error) {
	s := &State{}
	dir := DataDir()
	if dir == "" {
		return s, nil
	}
	s.path = filepath.Join(dir, "state.json")
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(data, s)
	}
	return s, err
}

func (s *State) IsPinned(conn, schema string) bool {
	return slices.Contains(s.Pinned[conn], schema)
}

// TogglePin pins or unpins a schema, saves, and reports whether it is now
// pinned.
func (s *State) TogglePin(conn, schema string) (bool, error) {
	if s.Pinned == nil {
		s.Pinned = map[string][]string{}
	}
	pins := s.Pinned[conn]
	pinned := !slices.Contains(pins, schema)
	if pinned {
		pins = append(pins, schema)
		slices.Sort(pins)
	} else {
		pins = slices.DeleteFunc(pins, func(p string) bool { return p == schema })
	}
	if len(pins) == 0 {
		delete(s.Pinned, conn)
	} else {
		s.Pinned[conn] = pins
	}
	return pinned, s.save()
}

func (s *State) save() error {
	if s.path == "" {
		return errors.New("no data directory to keep state in")
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return WriteFile(s.path, string(data)+"\n")
}
