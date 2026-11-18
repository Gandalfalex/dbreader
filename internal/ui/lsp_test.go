package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"dbreader/internal/sqlsafe"
)

func catalogFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.json")
	c := catalogFile{Dialect: sqlsafe.Postgres, Quote: `"`,
		Tables:  []string{"users", "orders"},
		Columns: map[string][]string{"users": {"id", "name"}, "orders": {"id", "user_id", "total"}}}
	if err := c.write(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompleteAt(t *testing.T) {
	c, err := readCatalog(catalogFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	text := "select 1;\n\nselect o.to\n  from orders o;\n"
	items, start := c.completeAt(text, 2, len("select o.to"))
	if start != len("select o.") || len(items) != 1 || items[0].Value != "total" {
		t.Errorf("start = %d, items = %+v", start, items)
	}
	// the statement before the cursor's does not count: no o alias there
	items, _ = c.completeAt("select o.\n  from orders o;\nselect o.", 2, len("select o."))
	if len(items) != 0 {
		t.Errorf("alias of another statement: %+v", items)
	}
}

func TestLSP(t *testing.T) {
	cat := catalogFixture(t)
	cin, sin := io.Pipe()
	sout, cout := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeLSP(cat, cin, cout); cout.Close() }()
	r := bufio.NewReader(sout)

	send := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		data, _ := json.Marshal(v)
		fmt.Fprintf(sin, "Content-Length: %d\r\n\r\n%s", len(data), data)
	}
	var res struct {
		Result struct {
			Items []struct {
				Label    string
				TextEdit struct {
					Range   lspRange
					NewText string
				}
			}
		}
	}
	reply := func() {
		h, err := readFrame(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(h, &res); err != nil {
			t.Fatal(err)
		}
	}

	send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{}})
	reply()
	uri := "file:///x.sql"
	send(map[string]any{"method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri, "text": "select u.\n  from users u;"}}})
	send(map[string]any{"method": "textDocument/didChange", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri}, "contentChanges": []any{map[string]any{"text": "select ü, u.na\n  from users u;"}}}})
	send(map[string]any{"id": 2, "method": "textDocument/completion", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri}, "position": lspPos{0, 14}}})
	reply()
	it := res.Result.Items
	if len(it) != 1 || it[0].Label != "name" || it[0].TextEdit.Range.Start.Character != 12 || it[0].TextEdit.NewText != "name" {
		t.Fatalf("items = %+v", it)
	}

	send(map[string]any{"id": 3, "method": "shutdown"})
	reply()
	send(map[string]any{"method": "exit"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCompleteVim(t *testing.T) {
	var out strings.Builder
	in := "2\n10\nselect 1;\nselect us\n"
	if err := CompleteVim(catalogFixture(t), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.HasPrefix(got, "7\nusers\tusers\ttable\n") {
		t.Errorf("out = %q", got)
	}
}
