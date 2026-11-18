package ui

import "testing"

func TestHiddenEntries(t *testing.T) {
	var l listState
	l.set([]entry{{Key: "app", Name: "app"}, {Key: "sys", Name: "sys", Hidden: true}})
	if len(l.vis) != 1 || l.hidden() != 1 {
		t.Fatalf("visible = %v, hidden = %d", l.vis, l.hidden())
	}
	l.setFilter("sy") // a filter finds hidden entries too
	if e, ok := l.current(); !ok || e.Key != "sys" {
		t.Errorf("filter: %v %v", e, ok)
	}
	l.setFilter("")
	l.showHidden = true
	l.refilter()
	if len(l.vis) != 2 {
		t.Errorf("showHidden: visible = %v", l.vis)
	}
}
