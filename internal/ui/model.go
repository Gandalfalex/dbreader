package ui

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"dbreader/internal/config"
	"dbreader/internal/db"
	"github.com/Gandalfalex/vim-kit/cmdline"
	"github.com/Gandalfalex/vim-kit/keys"
	"github.com/Gandalfalex/vim-kit/visual"
)

type model struct {
	settings *config.Settings
	w, h     int

	stack []screen

	conns   listState
	schemas listState
	tables  listState
	scripts listState
	grep    listState // tables matching grepText
	script  fileView  // text of scriptFile
	grid    grid

	scriptFile config.Script
	findFrom   visual.Pos // cursor when / was opened in the script

	grepText             string
	grepSeen, grepFailed int // tables searched, and those that failed

	// column names per table of colSchema, for sql completion
	colNames  map[string][]string
	colSchema string
	complTail string // text right of the cursor while completing

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

	mode       inputMode
	input      textinput.Model
	prevFilter string

	keys keys.Input // count prefix and pending g/z sequence

	loading  bool
	what     string
	spin     spinner.Model
	spinning bool // a spinner tick is in flight
	gen      int
	cancel   context.CancelFunc

	vp viewport.Model

	// The write guard: a statement waits in confirm until the user types
	// the expected word, then in pending until they commit or roll back.
	confirm *confirmState
	pending *db.Pending

	status    string
	statusErr bool

	cmd       cmdline.Line // ":" history and completion popup
	sqlHist   cmdline.Line // history of the sql prompt
	lastSQL   string
	quitArmed bool // q pressed once outside the top level

	opStart time.Time
	errs    []errEntry // newest last, capped at maxErrs
	logPath string
}

type errEntry struct {
	at   time.Time
	what string
	err  error
}

const maxErrs = 200

func newModel(s *config.Settings, logPath string) *model {
	in := textinput.New()
	in.Prompt = "/"
	in.CharLimit = 8192

	m := &model{
		settings: s,
		input:    in,
		spin:     spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(cAccent))),
		vp:       viewport.New(),
		logPath:  logPath,
	}
	var rows []entry
	for i, p := range s.Profiles {
		rows = append(rows, entry{Key: strconv.Itoa(i), Name: p.Name})
	}
	m.conns.set(rows)
	m.stack = []screen{scrConns}
	return m
}

func (m *model) Init() tea.Cmd {
	if len(m.settings.Profiles) == 1 {
		cmd := m.connect(0)
		m.spinning = true
		return tea.Batch(cmd, m.spin.Tick)
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

// onStack reports whether s is anywhere on the screen stack.
func (m *model) onStack(s screen) bool {
	for _, x := range m.stack {
		if x == s {
			return true
		}
	}
	return false
}

func (m *model) top() screen { return m.stack[len(m.stack)-1] }

func (m *model) push(s screen) { m.stack = append(m.stack, s) }

func (m *model) pop() {
	if len(m.stack) > 1 {
		m.stack = m.stack[:len(m.stack)-1]
	}
}

// popTo unwinds to the topmost s and reports whether it was on the stack.
func (m *model) popTo(s screen) bool {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i] == s {
			m.stack = m.stack[:i+1]
			return true
		}
	}
	return false
}

// popPages leaves pager screens, back to the list or grid below them.
func (m *model) popPages() {
	for m.isPage() && len(m.stack) > 1 {
		m.pop()
	}
}

// isPage reports whether the top screen is a read-only pager.
func (m *model) isPage() bool {
	switch m.top() {
	case scrRow, scrStruct, scrHelp, scrErrors:
		return true
	}
	return false
}

func (m *model) list() *listState {
	switch m.top() {
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

func (m *model) listHeight() int { return max(1, m.h-4) } // title, column header, status, hints

func (m *model) newOp(what string) context.Context {
	if m.cancel != nil {
		m.cancel()
	}
	m.gen++
	m.loading = true
	m.what = what
	m.opStart = time.Now()
	slog.Debug("op start", "op", what, "gen", m.gen)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	return ctx
}

// cancelOp aborts the operation in flight, if any, and drops its result.
func (m *model) cancelOp() {
	if !m.loading {
		return
	}
	m.cancel() // set by newOp whenever loading
	m.queue = nil
	m.done()
	m.gen++
}

func (m *model) done() {
	if m.loading {
		slog.Debug("op done", "op", m.what, "took", time.Since(m.opStart).Round(time.Millisecond))
	}
	m.loading = false
	m.what = ""
}

func (m *model) setStatus(s string, isErr bool) {
	m.status, m.statusErr = s, isErr
}

// fail records an operation error: status bar, error history (E) and log file.
// User cancellations are not errors.
func (m *model) fail(what string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	name := ""
	if m.conn != nil {
		name = m.conn.Profile.Name
	}
	m.queue = nil // a failed statement stops the rest of a :run
	slog.Error(what, "err", err, "connection", name, "schema", m.schema)
	m.errs = append(m.errs, errEntry{at: time.Now(), what: what, err: err})
	if len(m.errs) > maxErrs {
		m.errs = m.errs[len(m.errs)-maxErrs:]
	}
	m.setStatus(what+": "+shortErr(err)+"  (E: error log)", true)
	if m.top() == scrErrors {
		m.refreshViewport()
	}
}
