package ui

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	tk "github.com/Gandalfalex/tuikit"
	"github.com/Gandalfalex/vim-kit/motion"
)

// editScript suspends dbreader and opens the script on screen in the
// user's editor, on the line the cursor is on. Back in dbreader the file is
// read again and, with vim or neovim, the cursor is where it was left there
// and tables and columns complete in the editor.
func (m *model) editScript() tea.Cmd {
	if !m.Stack.Has(scrScript) {
		m.SetStatus("no script open", true)
		return nil
	}
	m.Stack.PopTo(scrScript)
	if m.script.dirty && !m.saveScript(false) {
		return nil // the status says why
	}
	var load tea.Cmd
	edit, err := tk.Edit(m.scriptFile.Path, m.script.Cursor+1, func(kind tk.EditorKind, dir string) []string {
		args, cmd, err := m.completionSetup(dir, kind == tk.Neovim)
		if err != nil {
			slog.Warn("no completion in the editor", "err", err)
			return nil
		}
		load = cmd
		return args
	})
	if err != nil {
		m.fail("editor", err)
		return nil
	}
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
	setup, src, source := filepath.Join(dir, "init.vim"), fmt.Sprintf(vimSetup, tk.VimString(exe), tk.VimString(ec.path)), "source "
	if nvim {
		setup, src, source = filepath.Join(dir, "init.lua"), fmt.Sprintf(nvimSetup, tk.LuaString(exe), tk.LuaString(ec.path)), "luafile "
	}
	if err := os.WriteFile(setup, []byte(src), 0o600); err != nil {
		return nil, nil, err
	}
	return []string{"-c", source + tk.VimEscape(setup)}, cmd, nil
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

// scriptEdited re-reads a script after the editor closed.
func (m *model) scriptEdited(msg tk.EditedMsg) {
	m.edCatalog.Store(nil)
	if msg.Err != nil {
		m.fail("editor", msg.Err)
	}
	if !m.Stack.Has(scrScript) || m.scriptFile.Path != msg.Path {
		return
	}
	m.Stack.PopTo(scrScript)
	m.openScript(m.scriptFile)
	if m.Stack.Top() != scrScript || m.scriptFile.Path != msg.Path {
		return
	}
	f := &m.script
	if msg.Line > 0 {
		f.Cursor = min(msg.Line, len(f.lines)) - 1
		f.col = motion.ClampCol(f.line(), msg.Col-1)
	}
	f.Clamp(len(f.lines))
	f.Scroll(len(f.lines), m.H-tk.Chrome)
	if msg.Err == nil {
		m.SetStatus(m.Status+" · enter runs the statement under the cursor", false)
	}
}
