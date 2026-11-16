package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// entry is how a linked connection is written to settings.json: its name
// and the two flags, always spelled out so they are there to edit. The
// connection itself (host, login, scripts) stays in DBeaver.
type entry struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"readOnly"`
	Protect  bool   `json:"protect"`
}

// file is settings.json with the profiles kept as written, so rewriting it
// never touches entries (or ${ENV} references) that are already there.
type file struct {
	DBeaver         string            `json:"dbeaver,omitempty"`
	ReadOnlyDefault *bool             `json:"readOnlyDefault,omitempty"`
	Profiles        []json.RawMessage `json:"profiles"`
}

// Init writes a settings file listing the linked connections, each
// read-only until its flag is changed. An existing file is kept: only
// connections it does not list yet are appended. It returns the names
// added.
func Init(path string, linked []Profile) (added []string, err error) {
	var f file
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		on := true
		f.ReadOnlyDefault = &on
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	listed := map[string]bool{}
	for _, raw := range f.Profiles {
		var e entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		listed[e.Name] = true
	}
	for _, p := range linked {
		if listed[p.Name] {
			continue
		}
		listed[p.Name] = true
		raw, _ := json.Marshal(entry{Name: p.Name, ReadOnly: true, Protect: p.Protect})
		f.Profiles = append(f.Profiles, raw)
		added = append(added, p.Name)
	}
	if len(added) == 0 && data != nil {
		return nil, nil
	}

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return added, os.WriteFile(path, out.Bytes(), 0o600)
}

// Overlay applies a settings.json entry to the linked connection of the
// same name. The entry decides the flags; where it names its own target or
// login those win too, otherwise they come from the linked connection.
func Overlay(own, linked Profile) Profile {
	out := linked
	out.ReadOnly, out.Protect = own.ReadOnly, own.Protect
	if own.Limit > 0 {
		out.Limit = own.Limit
	}
	if own.Schema != "" {
		out.Schema = own.Schema
	}
	if own.URL != "" || own.Host != "" || own.Path != "" {
		out.Driver, out.URL, out.Host, out.Port = own.Driver, own.URL, own.Host, own.Port
		out.Database, out.Path, out.SSLMode = own.Database, own.Path, own.SSLMode
		out.Blocked = ""
	}
	if own.User != "" {
		out.User = own.User
	}
	if own.Password != "" {
		out.Password = own.Password
	}
	return out
}
