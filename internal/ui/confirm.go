package ui

import (
	"fmt"
	"log/slog"
	"strings"

	"dbreader/internal/sqlsafe"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	tk "github.com/Gandalfalex/tuikit"
)

// confirmState is a non-read statement waiting for the user to type want.
type confirmState struct {
	stmt sqlsafe.Stmt
	want string
}

// confirmWord is what must be typed before stmt runs: "yes" for an ordinary
// write, the connection name when the statement is destructive, cannot be
// rolled back, or the profile is protected.
func (m *model) confirmWord(stmt sqlsafe.Stmt) string {
	p := m.conn.Profile
	if stmt.Kind == sqlsafe.Destructive || stmt.NoTx || p.Protect {
		return p.Name
	}
	return "yes"
}

func (m *model) askConfirm(stmt sqlsafe.Stmt) tea.Cmd {
	m.confirm = &confirmState{stmt: stmt, want: m.confirmWord(stmt)}
	slog.Info("confirm asked", "connection", m.conn.Profile.Name, "kind", stmt.Kind.String(), "sql", stmt.Text)
	return m.prompt.Open(inputConfirm, modeConfirm, "type "+m.confirm.want+" › ", "", "")
}

func (m *model) closeConfirm() {
	m.confirm = nil
	m.prompt.Close()
}

func (m *model) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+[":
		m.closeConfirm()
		m.queue = nil
		m.SetStatus("cancelled, nothing was executed", false)
		return m, nil
	case "enter":
		c := m.confirm
		typed := strings.TrimSpace(m.prompt.Input.Value())
		m.closeConfirm()
		// exact match, case included: a habitual "y" or "Yes" must not pass
		if typed != c.want {
			m.queue = nil
			m.SetStatus(fmt.Sprintf("not confirmed (expected %q), nothing was executed", c.want), true)
			return m, nil
		}
		slog.Info("confirmed", "connection", m.conn.Profile.Name, "sql", c.stmt.Text)
		return m, m.exec(c.stmt)
	}
	var cmd tea.Cmd
	m.prompt.Input, cmd = m.prompt.Input.Update(msg)
	return m, cmd
}

// handlePendingKey is the only key handling while a write waits for its
// verdict: nothing else may happen with a transaction open.
func (m *model) handlePendingKey(k string) (tea.Model, tea.Cmd) {
	if m.Ops.Loading() { // commit or rollback already under way
		return m, nil
	}
	switch k {
	case "c":
		m.Ops.Busy("committing")
		return m, finish(m.pending, true)
	case "r":
		m.Ops.Busy("rolling back")
		return m, finish(m.pending, false)
	}
	m.SetStatus("transaction open: c commits, r rolls back", true)
	return m, nil
}

// guarding reports whether the write guard owns the screen.
func (m *model) guarding() bool { return m.confirm != nil || m.pending != nil }

// renderGuard draws the confirmation or commit panel in place of the body.
func (m *model) renderGuard(h int) string {
	p := m.conn.Profile
	where := sCrumb.Bold(true).Render(p.Name) + sMuted.Render("  "+p.Driver+" · "+p.Target())
	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }

	if c := m.confirm; c != nil {
		badge := sCaution.Render("WRITE")
		if c.want != "yes" {
			badge = sDanger.Render(strings.ToUpper(c.stmt.Kind.String()))
		}
		reason := sWarn.Render(c.stmt.Reason)
		if pos := m.queuePos(); pos != "" {
			reason += sMuted.Render("  ·  " + pos + " of this run")
		}
		add("", "  "+badge+"  on  "+where, "", "  "+reason, "")
		add(m.quoteSQL(c.stmt.Text, h-12)...)
		add("")
		if c.stmt.NoTx {
			add("  " + sErr.Bold(true).Render("Takes effect immediately and cannot be rolled back."))
		} else {
			add("  " + sMuted.Render("Runs in a transaction. You see the affected rows, then commit or roll back."))
		}
		add("", "  Type "+sWarn.Bold(true).Render(c.want)+" and press enter to execute. Esc cancels.")
	} else {
		pd := m.pending
		add("", "  "+sDanger.Render("NOT COMMITTED")+"  on  "+where, "")
		add(m.quoteSQL(pd.Stmt.Text, h-10)...)
		rows := "rows"
		if pd.Affected == 1 {
			rows = "row"
		}
		add("", fmt.Sprintf("  %s %s affected  %s", sWarn.Bold(true).Render(tk.FmtInt(pd.Affected)), rows,
			sMuted.Render("· "+tk.FmtDur(pd.Took)+" · locks are held until you decide")))
		add("", "  "+sOK.Bold(true).Render("c")+" commit    "+sErr.Bold(true).Render("r")+" roll back")
	}

	var b strings.Builder
	for i := range h {
		if i < len(lines) {
			b.WriteString(ansi.Truncate(lines[i], m.W, "…"))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// quoteSQL shows a statement as an indented block of at most maxLines lines.
func (m *model) quoteSQL(sql string, maxLines int) []string {
	maxLines = max(maxLines, 1)
	bar := sMuted.Render("  ┃ ")
	var out []string
	wrapped := strings.Split(ansi.Hardwrap(tk.Clean(sql), max(10, m.W-6), true), "\n")
	for i, l := range wrapped {
		if len(out) == maxLines-1 && i < len(wrapped)-1 {
			out = append(out, bar+sMuted.Render("…"))
			break
		}
		out = append(out, bar+l)
	}
	return out
}
