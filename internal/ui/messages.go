package ui

import (
	tea "charm.land/bubbletea/v2"

	"dbreader/internal/db"
)

// op tags async results with the generation of the operation that produced
// them; Update drops results from superseded or cancelled operations.
type op int

func (o op) opGen() int { return int(o) }

type opResult interface{ opGen() int }

type (
	connMsg struct {
		op
		conn    *db.Conn
		schemas []string
		current string // schema unqualified names resolve to
		name    string
		err     error
	}
	tablesMsg struct {
		op
		schema string
		tables []db.Table
		err    error
	}
	dataMsg struct {
		op
		res    *db.Result
		sql    string
		browse *db.Browse
		err    error
	}
	structMsg struct {
		op
		table string
		cols  []db.Column
		err   error
	}
	execMsg struct {
		op
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
		err    error
	}
	grepMsg struct {
		op
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
	statusMsg string
)
