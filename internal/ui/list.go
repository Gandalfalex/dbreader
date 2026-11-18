package ui

import (
	"github.com/Gandalfalex/vim-kit/motion"
)

// entry is one row of a list screen: connections, schemas or tables.
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

type listState struct {
	all []entry
	vis []int
	motion.Window
	filter     string
	showHidden bool
}

func (l *listState) set(items []entry) {
	l.all = items
	l.refilter()
}

// setFilter replaces the filter and moves the cursor to the first match.
func (l *listState) setFilter(f string) {
	l.filter = f
	l.Cursor, l.Top = 0, 0
	l.refilter()
}

func (l *listState) refilter() {
	l.vis = l.vis[:0]
	for i, e := range l.all {
		if e.Hidden && !l.showHidden && l.filter == "" {
			continue
		}
		if smartMatch(e.Name, l.filter) || (e.Text != "" && smartMatch(e.Text, l.filter)) {
			l.vis = append(l.vis, i)
		}
	}
	l.clamp()
}

func (l *listState) clamp() { l.Clamp(len(l.vis)) }

func (l *listState) current() (entry, bool) {
	if len(l.vis) == 0 {
		return entry{}, false
	}
	return l.all[l.vis[l.Cursor]], true
}

func (l *listState) selectKey(key string) bool {
	return l.Select(len(l.vis), func(i int) bool { return l.all[l.vis[i]].Key == key })
}

// hidden counts the entries zh would show.
func (l *listState) hidden() int {
	n := 0
	for _, e := range l.all {
		if e.Hidden {
			n++
		}
	}
	return n
}

func (l *listState) has(key string) bool {
	for _, e := range l.all {
		if e.Key == key {
			return true
		}
	}
	return false
}

// scroll keeps the cursor visible with a small scrolloff.
func (l *listState) scroll(height int) { l.Scroll(len(l.vis), height) }
