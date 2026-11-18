package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// ServeLSP is a minimal language server for scripts opened in neovim: it
// completes tables, columns and keywords from the catalog file dbreader
// writes, with the same rules as the built-in editor. It speaks JSON-RPC on
// in and out until the client exits.
func ServeLSP(catalogPath string, in io.Reader, out io.Writer) error {
	s := &lspServer{catPath: catalogPath, docs: map[string]string{}, out: out}
	r := bufio.NewReader(in)
	for {
		msg, err := readLSP(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if done := s.handle(msg); done {
			return nil
		}
	}
}

type lspServer struct {
	catPath string
	cat     catalogFile
	catTime time.Time
	docs    map[string]string // uri → text
	out     io.Writer
}

type lspMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type lspPos struct {
	Line      int `json:"line"`
	Character int `json:"character"` // UTF-16 code units
}

type lspRange struct {
	Start lspPos `json:"start"`
	End   lspPos `json:"end"`
}

func readLSP(r *bufio.Reader) (lspMsg, error) {
	var msg lspMsg
	body, err := readFrame(r)
	if err != nil {
		return msg, err
	}
	return msg, json.Unmarshal(body, &msg)
}

// readFrame reads the body of one Content-Length framed message.
func readFrame(r *bufio.Reader) ([]byte, error) {
	h, err := textproto.NewReader(r).ReadMIMEHeader()
	if err != nil {
		if len(h) == 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
			return nil, io.EOF
		}
		return nil, err
	}
	n, err := strconv.Atoi(h.Get("Content-Length"))
	if err != nil {
		return nil, fmt.Errorf("bad Content-Length: %w", err)
	}
	body := make([]byte, n)
	_, err = io.ReadFull(r, body)
	return body, err
}

func (s *lspServer) send(v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n%s", len(data), data)
}

func (s *lspServer) reply(id json.RawMessage, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// handle answers one message and reports whether the client said exit.
func (s *lspServer) handle(msg lspMsg) bool {
	switch msg.Method {
	case "initialize":
		s.reply(msg.ID, map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":   1, // the whole text on every change
				"completionProvider": map[string]any{"triggerCharacters": []string{"."}},
			},
			"serverInfo": map[string]any{"name": "dbreader"},
		})
	case "textDocument/didOpen":
		if p, ok := docParams(msg); ok {
			s.docs[p.TextDocument.URI] = p.TextDocument.Text
		}
	case "textDocument/didChange":
		if p, ok := docParams(msg); ok && len(p.ContentChanges) > 0 {
			s.docs[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text
		}
	case "textDocument/didClose":
		if p, ok := docParams(msg); ok {
			delete(s.docs, p.TextDocument.URI)
		}
	case "textDocument/completion":
		p, ok := docParams(msg)
		if !ok {
			s.reply(msg.ID, nil)
			break
		}
		s.reply(msg.ID, s.complete(s.docs[p.TextDocument.URI], p.Position))
	case "shutdown":
		s.reply(msg.ID, nil)
	case "exit":
		return true
	default:
		if len(msg.ID) > 0 && msg.Method != "" {
			s.send(map[string]any{"jsonrpc": "2.0", "id": msg.ID,
				"error": map[string]any{"code": -32601, "message": "not supported: " + msg.Method}})
		}
	}
	return false
}

// lspParams holds the fields of the document notifications and completion
// requests this server reads.
type lspParams struct {
	TextDocument   struct{ URI, Text string } `json:"textDocument"`
	ContentChanges []struct{ Text string }    `json:"contentChanges"`
	Position       lspPos                     `json:"position"`
}

func docParams(msg lspMsg) (lspParams, bool) {
	var p lspParams
	return p, json.Unmarshal(msg.Params, &p) == nil
}

// LSP completion item kinds
const (
	kindField   = 5
	kindClass   = 7
	kindModule  = 9
	kindKeyword = 14
)

func (s *lspServer) complete(text string, pos lspPos) map[string]any {
	s.loadCatalog()
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	line := ""
	if pos.Line < len(lines) {
		line = lines[pos.Line]
	}
	col := runeCol(line, pos.Character)
	items, start := s.cat.completeAt(text, pos.Line, col)
	rng := lspRange{Start: lspPos{pos.Line, utf16Col(line, start)}, End: pos}
	out := make([]map[string]any, 0, len(items))
	for i, it := range items {
		kind := kindField
		switch it.Desc {
		case "table":
			kind = kindClass
		case "schema":
			kind = kindModule
		case "":
			kind = kindKeyword
		}
		out = append(out, map[string]any{
			"label": it.Label, "kind": kind, "detail": it.Desc,
			"filterText": it.Label, "sortText": fmt.Sprintf("%04d", i),
			"textEdit": map[string]any{"range": rng, "newText": it.Value},
		})
	}
	// the list is cut at maxCompletions: ask again as the word grows
	return map[string]any{"isIncomplete": true, "items": out}
}

// loadCatalog re-reads the catalog file when dbreader has rewritten it.
func (s *lspServer) loadCatalog() {
	info, err := os.Stat(s.catPath)
	if err != nil || info.ModTime().Equal(s.catTime) {
		return
	}
	if c, err := readCatalog(s.catPath); err == nil {
		s.cat, s.catTime = c, info.ModTime()
	}
}

// runeCol turns a UTF-16 column into a rune index of line.
func runeCol(line string, u16 int) int {
	n := 0
	for _, r := range line {
		if u16 <= 0 {
			break
		}
		u16 -= utf16Len(r)
		n++
	}
	return n
}

// utf16Col turns a rune index of line into a UTF-16 column.
func utf16Col(line string, runes int) int {
	n := 0
	for i, r := range []rune(line) {
		if i == runes {
			break
		}
		n += utf16Len(r)
	}
	return n
}

func utf16Len(r rune) int { return max(1, utf16.RuneLen(r)) }

// CompleteVim answers the -complete helper used by plain vim's omnifunc.
// in holds the 1-based line, the 1-based byte column of the cursor and the
// buffer text, one per line; out gets the 0-based byte column where the
// word starts, then one "insert\tlabel\tdetail" line per candidate.
func CompleteVim(catalogPath string, in io.Reader, out io.Writer) error {
	cat, err := readCatalog(catalogPath)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	head := strings.SplitN(string(data), "\n", 3)
	if len(head) < 3 {
		return fmt.Errorf("want line, column and text")
	}
	line, _ := strconv.Atoi(strings.TrimSpace(head[0]))
	col, _ := strconv.Atoi(strings.TrimSpace(head[1]))
	text := head[2]
	lines := strings.Split(text, "\n")
	if line < 1 || line > len(lines) {
		return fmt.Errorf("line %d out of range", line)
	}
	l := lines[line-1]
	items, start := cat.completeAt(text, line-1, runeIndex(l, col))
	w := bufio.NewWriter(out)
	fmt.Fprintln(w, len(string([]rune(l)[:start])))
	for _, it := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\n", it.Value, it.Label, it.Desc)
	}
	return w.Flush()
}
