package ui

import (
	"dbreader/internal/db"

	tea "charm.land/bubbletea/v2"

	tk "github.com/Gandalfalex/tuikit"
)

type (
	connMsg struct {
		tk.Op
		conn    *db.Conn
		schemas []db.Schema
		current string // schema unqualified names resolve to
		name    string
		err     error
	}
	tablesMsg struct {
		tk.Op
		schema string
		tables []db.Table
		err    error
	}
	dataMsg struct {
		tk.Op
		res    *db.Result
		sql    string
		browse *db.Browse
		err    error
	}
	structMsg struct {
		tk.Op
		table string
		cols  []db.Column
		err   error
	}
	execMsg struct {
		tk.Op
		pending *db.Pending
		err     error
	}
	// finishMsg ends a write transaction. It carries no generation: the
	// outcome of a commit must never be dropped.
	finishMsg struct {
		pending   *db.Pending
		committed bool
		err       error
	}
	// colNamesMsg feeds sql completion; it loads beside whatever else runs.
	colNamesMsg struct {
		conn   *db.Conn
		schema string
		names  map[string][]string
		search map[string][]string
		err    error
	}
	grepMsg struct {
		tk.Op
		table string
		n     int
		err   error
		done  bool
		ch    <-chan tea.Msg
	}
	editorMsg struct {
		sql string
		err error
	}
)
