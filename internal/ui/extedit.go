package ui

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/Gandalfalex/vim-kit/motion"
)

// editorCommand is $VISUAL, else $EDITOR, else vi, split into words.
func editorCommand() []string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(v)); len(f) > 0 {
			return f
		}
	}
	return []string{"vi"}
}

// hasEditor reports whether the user named an editor of their own.
func hasEditor() bool { return os.Getenv("VISUAL") != "" || os.Getenv("EDITOR") != "" }

// editorKind is what dbreader can do with an editor beyond opening a file.
type editorKind int

const (
	plainEditor editorKind = iota
	plusLine               // takes +N to start on line N
	vim                    // also reports the cursor and completes through an omnifunc
	nvim                   // like vim, completing through a language server
)

func kindOf(cmd string) editorKind {
	switch filepath.Base(cmd) {
	case "vi", "vim", "view":
		return vim
	case "nvim", "nview":
		return nvim
	case "nano", "emacs", "micro", "kak":
		return plusLine
	}
	return plainEditor
}

type scriptEditedMsg struct {
	path      string
	line, col int // 1-based cursor the editor was left with, 0 if unknown
	err       error
}

// editScript suspends dbreader and opens the script on screen in the
// user's editor, on the line the cursor is on. Back in dbreader the file is
// read again and, with vim or neovim, the cursor is where it was left there.
func (m *model) editScript() tea.Cmd {
	if !m.onStack(scrScript) {
		m.setStatus("no script open", true)
		return nil
	}
	m.popTo(scrScript)
	if m.script.dirty && !m.saveScript(false) {
		return nil // the status says why
	}
	path, line := m.scriptFile.Path, m.script.Cursor+1
	args := editorCommand()
	var dir, spot string // scratch files of a vim session; spot gets the cursor on leaving
	var load tea.Cmd
	switch kind := kindOf(args[0]); kind {
	case vim, nvim:
		var err error
		if dir, err = os.MkdirTemp("", "dbreader-edit-*"); err != nil {
			m.fail("editor", err)
			return nil
		}
		spot = filepath.Join(dir, "cursor")
		args = append(args, "+"+strconv.Itoa(line), "-c",
			"autocmd VimLeavePre * call writefile([line('.'), col('.'), expand('%:p')], "+vimString(spot)+")")
		if extra, cmd, err := m.completionSetup(dir, kind == nvim); err != nil {
			slog.Warn("no completion in the editor", "err", err)
		} else {
			args, load = append(args, extra...), cmd
		}
	case plusLine:
		args = append(args, "+"+strconv.Itoa(line))
	}
	args = append(args, path)
	edit := tea.ExecProcess(exec.Command(args[0], args[1:]...), func(err error) tea.Msg {
		msg := scriptEditedMsg{path: path, err: err}
		if dir == "" {
			return msg
		}
		defer os.RemoveAll(dir)
		data, _ := os.ReadFile(spot)
		f := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(f) == 3 && samePath(f[2], path) {
			msg.line, _ = strconv.Atoi(f[0])
			msg.col, _ = strconv.Atoi(f[1])
		}
		return msg
	})
	return tea.Batch(load, edit)
}

// completionSetup writes the completion catalog into dir and returns the
// editor arguments that use it: a language server for neovim (picked up by
// nvim-cmp, blink and the like; built-in completion otherwise), an
// omnifunc for vim. cmd, if any, loads the columns not known yet.
func (m *model) completionSetup(dir string, nvim bool) (args []string, cmd tea.Cmd, err error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	schema := m.editorSchema()
	ec := &editorCatalog{path: filepath.Join(dir, "catalog.json"), schema: schema, base: m.catalogFile(schema)}
	if err := ec.base.write(ec.path); err != nil {
		return nil, nil, err
	}
	m.edCatalog.Store(ec)
	if ec.base.Columns == nil && m.colsLoading != schema {
		cmd = m.loadColNames(schema)
	}
	setup, src, source := filepath.Join(dir, "init.vim"), fmt.Sprintf(vimSetup, vimString(exe), vimString(ec.path)), "source "
	if nvim {
		setup, src, source = filepath.Join(dir, "init.lua"), fmt.Sprintf(nvimSetup, luaString(exe), luaString(ec.path)), "luafile "
	}
	if err := os.WriteFile(setup, []byte(src), 0o600); err != nil {
		return nil, nil, err
	}
	return []string{"-c", source + vimEscape(setup)}, cmd, nil
}

// nvimSetup starts the language server for the script's buffer. Without a
// completion plugin it turns on neovim's own completion as you type.
const nvimSetup = `local buf = vim.api.nvim_get_current_buf()
local id = vim.lsp.start({
  name = 'dbreader',
  cmd = { %s, '-lsp', %s },
  root_dir = vim.fn.expand('%%:p:h'),
}, { bufnr = buf })
local function has(mod)
  if package.loaded[mod] then return true end
  local ok, lazy = pcall(require, 'lazy.core.config')
  return ok and lazy.plugins and (lazy.plugins[mod] ~= nil or lazy.plugins[mod:gsub('%%.', '-')] ~= nil)
end
if id and vim.lsp.completion and not (has('cmp') or has('nvim-cmp') or has('blink.cmp')) then
  vim.lsp.completion.enable(true, id, buf, { autotrigger = true })
end
`

// vimSetup completes with ctrl-x ctrl-o through dbreader -complete.
const vimSetup = `let s:cmd = join(map([%s, '-complete', %s], 'shellescape(v:val)'))
function! DbreaderComplete(findstart, base) abort
  if a:findstart
    let input = line('.') . "\n" . col('.') . "\n" . join(getline(1, '$'), "\n")
    let out = systemlist(s:cmd, input)
    if v:shell_error || empty(out)
      return -3
    endif
    let s:items = []
    for row in out[1:]
      let f = split(row, "\t", 1)
      call add(s:items, {'word': f[0], 'abbr': f[1], 'menu': get(f, 2, '')})
    endfor
    return str2nr(out[0])
  endif
  return s:items
endfunction
setlocal omnifunc=DbreaderComplete
`

// vimString quotes s as a vim script string literal.
func vimString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// luaString quotes s as a Lua long string.
func luaString(s string) string {
	eq := ""
	for strings.Contains(s, "]"+eq+"]") {
		eq += "="
	}
	return "[" + eq + "[" + s + "]" + eq + "]"
}

// vimEscape escapes a file name for an ex command.
func vimEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(" \\\t%#|\"", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// runeIndex turns vim's 1-based byte column of line into a rune index.
func runeIndex(line string, byteCol int) int {
	return utf8.RuneCountInString(line[:max(0, min(byteCol-1, len(line)))])
}

// scriptEdited re-reads a script after the editor closed.
func (m *model) scriptEdited(msg scriptEditedMsg) {
	m.edCatalog.Store(nil)
	if msg.err != nil {
		m.fail("editor", msg.err)
	}
	if !m.onStack(scrScript) || m.scriptFile.Path != msg.path {
		return
	}
	m.popTo(scrScript)
	m.openScript(m.scriptFile)
	if m.top() != scrScript || m.scriptFile.Path != msg.path {
		return
	}
	f := &m.script
	if msg.line > 0 {
		f.Cursor = min(msg.line, len(f.lines)) - 1
		f.col = motion.ClampCol(f.line(), runeIndex(f.line(), msg.col))
	}
	f.Clamp(len(f.lines))
	f.Scroll(len(f.lines), m.h-3)
	if msg.err == nil {
		m.setStatus(m.status+" · enter runs the statement under the cursor", false)
	}
}
