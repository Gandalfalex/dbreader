package ui

import tk "github.com/Gandalfalex/tuikit"

// entry is one row of a list screen: connections, schemas, tables, scripts.
type entry struct {
	Key  string
	Name string
	Cols []string // extra columns after the name
	Text string   // searched by the filter besides the name: column names, file content
	Dim  bool     // drawn muted, e.g. system schemas and views
	// Hidden entries are left out until zh shows them; a filter finds them
	// either way.
	Hidden bool
}

type listState = tk.List[entry]

// newList is a list of entries filtered by name and text.
func newList() listState {
	l := tk.NewList(func(e entry) string { return e.Key })
	l.Match = func(f string) func(entry) bool {
		return func(e entry) bool { return tk.SmartMatch(e.Name, f) || (e.Text != "" && tk.SmartMatch(e.Text, f)) }
	}
	l.Hidden = func(e entry) bool { return e.Hidden }
	return l
}
