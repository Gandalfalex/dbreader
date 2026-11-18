package ui

import (
	"log/slog"
	"strconv"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"dbreader/internal/config"
	"dbreader/internal/db"
	tk "github.com/Gandalfalex/tuikit"
	"github.com/Gandalfalex/vim-kit/cmdline"
	"github.com/Gandalfalex/vim-kit/visual"
)

type model struct {
	tk.Shell[screen] // stack, status, error log, async ops, keys, size

	settings *config.Settings
	state    *config.State

	conns   listState
	schemas listState
	// what the server said about the schemas, and the one names resolve to
	schemaInfo    []db.Schema
	currentSchema string
	tables        listState
	scripts       listState
	grep          listState // tables matching grepText
	script        fileView  // text of scriptFile
	grid          grid

	scriptFile config.Script
	findFrom   visual.Pos // cursor when / was opened in the script

	grepText             string
	grepSeen, grepFailed int // tables searched, and those that failed

	// column names per table of colSchema, for sql completion
	colNames    map[string][]string
	colSchema   string
	colsLoading string // schema whose column names are being fetched
	// the catalog file of an external editor session, which loadColNames
	// keeps up to date while dbreader waits for the editor
	edCatalog atomic.Pointer[editorCatalog]
	// the columns text can be searched in (no LOBs or raw bytes), per table
	searchCols map[string][]string
	complTail  string // text right of the cursor while completing

	edCompl cmdline.Line // completion popup of the script editor
	edStart int          // column where the word being completed starts

	// statements of a :run still to execute, and how many there were
	queue  []string
	queueN int

	conn   *db.Conn
	schema string
	limit  int // rows fetched per query

	structOf string // table shown on the structure page
	cols     []db.Column

	prompt     tk.Prompt[inputMode]
	prevFilter string

	vp tk.Pager

	// The write guard: a statement waits in confirm until the user types
	// the expected word, then in pending until they commit or roll back.
	confirm *confirmState
	pending *db.Pending

	cmd     tk.CommandLine[*model] // ":" commands, history and completion
	sqlHist cmdline.Line           // history of the sql prompt
	lastSQL string

	logPath string
}

func newModel(s *config.Settings, logPath string) *model {
	m := &model{
		Shell:    tk.NewShell(theme, scrConns),
		conns:    newList(),
		schemas:  newList(),
		tables:   newList(),
		scripts:  newList(),
		grep:     newList(),
		settings: s,
		state:    &config.State{},
		prompt:   tk.NewPrompt[inputMode](),
		cmd:      tk.CommandLine[*model]{Commands: commands, Goto: gotoLine},
		vp:       tk.NewPager(),
		logPath:  logPath,
	}
	var rows []entry
	for i, p := range s.Profiles {
		rows = append(rows, entry{Key: strconv.Itoa(i), Name: p.Name})
	}
	m.conns.Set(rows)
	return m
}

func (m *model) Init() tea.Cmd {
	if len(m.settings.Profiles) == 1 {
		return tea.Batch(m.connect(0), m.Ops.Tick())
	}
	return nil
}

// shutdown saves an edited script, rolls back an undecided write and closes
// the connection.
func (m *model) shutdown() {
	m.keepScript()
	if m.pending != nil && m.pending.Open() {
		slog.Warn("quit with open transaction, rolling back", "sql", m.pending.Stmt.Text)
		_ = m.pending.Rollback()
	}
	if m.conn != nil {
		_ = m.conn.Close()
	}
}

// popPages leaves pager screens, back to the list or grid below them.
func (m *model) popPages() {
	m.Stack.PopWhile(isPage)
}

// isPage reports whether s is a read-only pager.
func isPage(s screen) bool {
	switch s {
	case scrRow, scrStruct, scrHelp, scrErrors:
		return true
	}
	return false
}

func (m *model) list() *listState {
	switch m.Stack.Top() {
	case scrConns:
		return &m.conns
	case scrSchemas:
		return &m.schemas
	case scrTables:
		return &m.tables
	case scrScripts:
		return &m.scripts
	case scrGrep:
		return &m.grep
	}
	return nil
}

func (m *model) listHeight() int { return max(1, m.H-4) } // title, column header, status, hints

// cancelOp aborts the operation in flight, if any, and drops its result.
func (m *model) cancelOp() bool {
	if !m.Ops.Cancel() {
		return false
	}
	m.queue = nil
	return true
}

// fail records an operation error: status bar, error log (E) and log
// file. A failed statement stops the rest of a :run.
func (m *model) fail(what string, err error) {
	name := ""
	if m.conn != nil {
		name = m.conn.Profile.Name
	}
	if !m.Fail(what, err, "connection", name, "schema", m.schema) {
		return
	}
	m.queue = nil
	if m.Stack.Top() == scrErrors {
		m.refreshViewport()
	}
}
