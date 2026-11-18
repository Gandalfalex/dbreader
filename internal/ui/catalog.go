package ui

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/Gandalfalex/vim-kit/cmdline"

	"dbreader/internal/config"
	"dbreader/internal/db"
	"dbreader/internal/sqlsafe"
)

// catalogFile is the completion catalog handed to an external editor: the
// language server (-lsp) and the vim helper (-complete) read it, and
// loadColNames rewrites it when columns arrive while the editor is open.
type catalogFile struct {
	Dialect sqlsafe.Dialect     `json:"dialect"`
	Quote   string              `json:"quote"`
	Tables  []string            `json:"tables"`
	Schemas []string            `json:"schemas,omitempty"`
	Columns map[string][]string `json:"columns,omitempty"`
}

// editorCatalog is the catalog file of the editor session in progress.
type editorCatalog struct {
	path, schema string
	base         catalogFile
}

// catalogFile is what is known of schema so far; Columns is nil until its
// column names have loaded.
func (m *model) catalogFile(schema string) catalogFile {
	c := catalogFile{Dialect: m.conn.Dialect(), Quote: m.conn.QuoteChar()}
	for _, e := range m.schemas.all {
		c.Schemas = append(c.Schemas, e.Name)
	}
	if m.schema == schema {
		for _, e := range m.tables.all {
			c.Tables = append(c.Tables, e.Name)
		}
	}
	if m.colSchema == schema {
		c.withColumns(m.colNames)
	}
	return c
}

// withColumns sets the columns; without a table list the tables are the
// ones the columns belong to.
func (c *catalogFile) withColumns(names map[string][]string) {
	c.Columns = names
	if len(c.Tables) > 0 {
		return
	}
	for t := range names {
		c.Tables = append(c.Tables, t)
	}
	sort.Strings(c.Tables)
}

// editorSchema is the schema a script is completed against: the one being
// browsed, else the one unqualified names resolve to.
func (m *model) editorSchema() string {
	if m.schema != "" || !m.conn.HasSchemas() {
		return m.schema
	}
	return m.currentSchema
}

func (c catalogFile) write(path string) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return config.WriteFile(path, string(data))
}

func readCatalog(path string) (catalogFile, error) {
	var c catalogFile
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	return c, err
}

func (c catalogFile) catalog() sqlCatalog {
	return sqlCatalog{
		tables: c.Tables, schemas: c.Schemas, columns: c.Columns, fold: foldFor(c.Dialect),
		quote: func(s string) string { return db.QuoteIdent(c.Quote, s) },
	}
}

// completeAt completes the word left of line:col (0-based, col in runes) of
// a whole file, from the statement the cursor is in exactly as the built-in
// editor does, and returns the column where the word starts.
func (c catalogFile) completeAt(text string, line, col int) (items []cmdline.Item, start int) {
	f := newFileView(text, c.Dialect)
	if line < 0 || line >= len(f.lines) {
		return nil, col
	}
	f.Cursor = line
	f.col = max(0, min(col, f.lineLen()))
	stmt, cursor := f.context()
	items, from := completeWord(stmt, cursor, c.catalog())
	return items, f.col - (cursor - from)
}
