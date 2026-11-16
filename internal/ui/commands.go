package ui

import (
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"dbreader/internal/db"
	"github.com/Gandalfalex/vim-kit/cmdline"
)

// Commands, lookup and completion come from vimkit/cmdline; this file holds
// the command list and the argument completers.
type (
	command   = cmdline.Command[*model, tea.Cmd]
	complItem = cmdline.Item
)

const maxPopup = 10

var commands cmdline.Set[*model, tea.Cmd]

func init() {
	commands = cmdline.Set[*model, tea.Cmd]{
		{Name: "sql", Args: "<statement>", Desc: "run one statement (writes ask before running)",
			Run: func(m *model, arg string) tea.Cmd { return m.runTyped(arg) }},
		{Name: "edit", Aliases: []string{"e"}, Desc: "write a statement in $EDITOR and run it",
			Run: func(m *model, arg string) tea.Cmd { return m.editSQL() }},
		{Name: "scripts", Desc: "sql scripts linked to this connection",
			Run: func(m *model, arg string) tea.Cmd { return m.openScripts() }},
		{Name: "script", Args: "<name>", Desc: "open a sql script", Complete: completeScripts,
			Run: func(m *model, arg string) tea.Cmd { return m.cmdScript(arg) }},
		{Name: "run", Aliases: []string{"exec"}, Desc: "(script) run the selection, or the statement under the cursor",
			Run: func(m *model, arg string) tea.Cmd { return m.runScript() }},
		{Name: "new", Args: "<name>", Desc: "create a sql script for this connection and start writing",
			Run: func(m *model, arg string) tea.Cmd { return m.newScript(arg) }},
		{Name: "write", Aliases: []string{"w"}, Desc: "(script) save",
			Run: func(m *model, arg string) tea.Cmd { return m.cmdWrite(false) }},
		{Name: "write!", Aliases: []string{"w!"}, Desc: "(script) save over a file that changed on disk",
			Run: func(m *model, arg string) tea.Cmd { return m.cmdWrite(true) }},
		{Name: "revert", Aliases: []string{"e!"}, Desc: "(script) drop unsaved changes and re-read the file",
			Run: func(m *model, arg string) tea.Cmd { return m.cmdRevert() }},
		{Name: "connect", Aliases: []string{"c"}, Args: "<name>", Desc: "switch connection", Complete: completeConns,
			Run: func(m *model, arg string) tea.Cmd { return m.cmdConnect(arg) }},
		{Name: "schema", Args: "<name>", Desc: "switch schema", Complete: completeSchemas,
			Run: func(m *model, arg string) tea.Cmd { return m.cmdSchema(arg) }},
		{Name: "table", Aliases: []string{"t"}, Args: "<name>", Desc: "open table", Complete: completeTables,
			Run: func(m *model, arg string) tea.Cmd { return m.cmdTable(arg) }},
		{Name: "where", Aliases: []string{"w"}, Args: "<condition>", Desc: "filter the table on the server (empty clears)", Complete: completeColumns,
			Run: func(m *model, arg string) tea.Cmd { return m.cmdWhere(arg) }},
		{Name: "order", Args: "<column> [desc]", Desc: "sort the table on the server (empty clears)", Complete: completeColumns,
			Run: func(m *model, arg string) tea.Cmd { return m.cmdOrder(arg) }},
		{Name: "limit", Args: "<rows>", Desc: "rows fetched per query",
			Run: func(m *model, arg string) tea.Cmd { return m.cmdLimit(arg) }},
		{Name: "filter", Aliases: []string{"f"}, Args: "<text>", Desc: "filter loaded rows (empty clears)",
			Run: func(m *model, arg string) tea.Cmd { m.popPages(); m.applyFilter(arg); return nil }},
		{Name: "nohlsearch", Aliases: []string{"noh"}, Desc: "clear filter",
			Run: func(m *model, arg string) tea.Cmd { m.popPages(); m.applyFilter(""); return nil }},
		{Name: "structure", Aliases: []string{"info", "i"}, Desc: "columns of the table",
			Run: func(m *model, arg string) tea.Cmd { return m.openStruct() }},
		{Name: "yank", Aliases: []string{"y"}, Args: "[tsv|csv|json|markdown]", Desc: "copy the cell under the cursor; with a format, the selected or all loaded rows",
			Complete: func(m *model, arg string) []complItem { return cmdline.MatchStrings(yankFormats, arg) },
			Run: func(m *model, arg string) tea.Cmd {
				if arg == "" && !m.grid.selecting {
					return m.cmdYank()
				}
				return m.yankRows(arg)
			}},
		{Name: "grep", Aliases: []string{"search"}, Args: "<text>", Desc: "rows containing the text: in the open table, else in every table of the schema",
			Run: func(m *model, arg string) tea.Cmd {
				m.popPages()
				if m.top() == scrData && m.grid.browse != nil {
					return m.search(arg)
				}
				if arg == "" {
					m.setStatus("usage: :grep <text>", true)
					return nil
				}
				return m.startGrep(arg)
			}},
		{Name: "reload", Aliases: []string{"r"}, Desc: "reload current view",
			Run: func(m *model, arg string) tea.Cmd { return m.reload() }},
		{Name: "messages", Aliases: []string{"errors", "mes"}, Desc: "error log",
			Run: func(m *model, arg string) tea.Cmd { m.openPage(scrErrors, true); return nil }},
		{Name: "help", Aliases: []string{"h"}, Desc: "key bindings and the write guard",
			Run: func(m *model, arg string) tea.Cmd { m.openPage(scrHelp, false); return nil }},
		{Name: "quit", Aliases: []string{"q", "q!", "qa", "qa!", "x", "wq"}, Desc: "quit (an edited script is saved)",
			Run: func(m *model, arg string) tea.Cmd { return tea.Quit }},
	}
}

func (m *model) needConn() bool {
	if m.conn == nil {
		m.setStatus("not connected (:connect <name>)", true)
		return false
	}
	return true
}

// needTable returns the table the grid shows, for commands that refine it.
func (m *model) needTable() (db.Browse, bool) {
	if (m.top() == scrData || m.top() == scrRow) && m.grid.browse != nil {
		return *m.grid.browse, true
	}
	m.setStatus("open a table first (query results: put it in the statement)", true)
	return db.Browse{}, false
}

func (m *model) cmdConnect(arg string) tea.Cmd {
	if arg == "" {
		m.stack = m.stack[:1]
		return nil
	}
	for i, p := range m.settings.Profiles {
		if p.Name == arg {
			m.conns.setFilter("")
			m.conns.selectKey(strconv.Itoa(i))
			return m.connect(i)
		}
	}
	m.setStatus("unknown connection: "+arg, true)
	return nil
}

func (m *model) cmdSchema(arg string) tea.Cmd {
	if !m.needConn() {
		return nil
	}
	if !m.conn.HasSchemas() {
		m.setStatus(m.conn.Profile.Driver+" has no schemas", false)
		return nil
	}
	if arg == "" {
		m.popTo(scrSchemas)
		return nil
	}
	m.schemas.setFilter("")
	if !m.schemas.selectKey(arg) {
		m.setStatus("unknown schema: "+arg, true)
		return nil
	}
	return m.loadTables(arg)
}

func (m *model) cmdTable(arg string) tea.Cmd {
	if !m.needConn() {
		return nil
	}
	if arg == "" {
		if !m.popTo(scrTables) {
			m.setStatus("pick a schema first", true)
		}
		return nil
	}
	schema := m.schema
	if s, t, ok := strings.Cut(arg, "."); ok && m.conn.HasSchemas() {
		schema, arg = s, t
	}
	m.tables.selectKey(arg)
	return m.browse(db.Browse{Schema: schema, Table: arg})
}

func (m *model) cmdWhere(arg string) tea.Cmd {
	b, ok := m.needTable()
	if !ok {
		return nil
	}
	b.Where = arg
	return m.browse(b)
}

func (m *model) cmdOrder(arg string) tea.Cmd {
	b, ok := m.needTable()
	if !ok {
		return nil
	}
	f := strings.Fields(arg)
	b.Order, b.Desc = "", false
	if len(f) > 0 {
		b.Order = f[0]
		b.Desc = len(f) > 1 && strings.HasPrefix("desc", strings.ToLower(f[1]))
	}
	return m.browse(b)
}

func (m *model) cmdLimit(arg string) tea.Cmd {
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		m.setStatus("usage: :limit <rows>  (now "+strconv.Itoa(m.limit)+")", true)
		return nil
	}
	m.limit = n
	m.popPages()
	if m.top() == scrData {
		return m.reload()
	}
	m.setStatus("limit "+strconv.Itoa(n), false)
	return nil
}

func completeConns(m *model, arg string) []complItem {
	var items []complItem
	for _, p := range m.settings.Profiles {
		items = append(items, complItem{Value: p.Name, Label: p.Name, Desc: p.Driver + " " + p.Target()})
	}
	return cmdline.Match(items, arg)
}

func (m *model) cmdWrite(force bool) tea.Cmd {
	if !m.onStack(scrScript) {
		m.setStatus("no script open", true)
		return nil
	}
	m.saveScript(force)
	return nil
}

func (m *model) cmdRevert() tea.Cmd {
	if m.top() != scrScript {
		m.setStatus("no script open", true)
		return nil
	}
	m.script.dirty = false
	m.openScript(m.scriptFile)
	return nil
}

func (m *model) cmdScript(arg string) tea.Cmd {
	if arg == "" {
		return m.openScripts()
	}
	if !m.needConn() {
		return nil
	}
	s, ok := m.findScript(arg)
	if !ok {
		m.setStatus("unknown script: "+arg, true)
		return nil
	}
	m.openScript(s)
	return nil
}

func completeScripts(m *model, arg string) []complItem {
	linked, loose := m.connScripts()
	var items []complItem
	for _, s := range linked {
		items = append(items, complItem{Value: s.Name, Label: s.Name, Desc: m.conn.Profile.Name})
	}
	for _, s := range loose {
		items = append(items, complItem{Value: s.Name, Label: s.Name, Desc: "any connection"})
	}
	return cmdline.Match(items, arg)
}

func completeSchemas(m *model, arg string) []complItem {
	var items []complItem
	for _, e := range m.schemas.all {
		items = append(items, complItem{Value: e.Name, Label: e.Name})
	}
	return cmdline.Match(items, arg)
}

func completeTables(m *model, arg string) []complItem {
	var items []complItem
	for _, e := range m.tables.all {
		items = append(items, complItem{Value: e.Name, Label: e.Name, Desc: strings.Join(e.Cols, "  ")})
	}
	return cmdline.Match(items, arg)
}

// completeColumns offers the grid's columns for the word being typed.
func completeColumns(m *model, arg string) []complItem {
	if m.grid.res == nil {
		return nil
	}
	head, last := "", arg
	if i := strings.LastIndexByte(arg, ' '); i >= 0 {
		head, last = arg[:i+1], arg[i+1:]
	}
	var items []complItem
	for i, c := range m.grid.res.Columns {
		items = append(items, complItem{Value: c, Label: c, Desc: strings.ToLower(m.grid.res.Types[i])})
	}
	items = cmdline.Match(items, last)
	for i := range items {
		items[i].Value = head + items[i].Value
	}
	return items
}

// computeCompletions builds the popup for the current command-line text.
func (m *model) computeCompletions(input string) []complItem {
	return commands.Complete(m, input)
}

// commandHelp lists commands for the help screen.
func commandHelp() string {
	cs := append([]command(nil), commands...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	var b strings.Builder
	for _, c := range cs {
		label := ":" + c.Name
		if c.Args != "" {
			label += " " + c.Args
		}
		alias := ""
		if len(c.Aliases) > 0 {
			alias = " (:" + strings.Join(c.Aliases, ", :") + ")"
		}
		b.WriteString("    " + pad(label, 26) + c.Desc + alias + "\n")
	}
	return b.String()
}
