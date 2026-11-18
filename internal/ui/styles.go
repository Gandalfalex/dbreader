package ui

import (
	"charm.land/lipgloss/v2"

	tk "github.com/Gandalfalex/tuikit"
)

var (
	theme = tk.TokyoNight

	cAccent, cMuted, cDir, cErr = theme.C.Accent, theme.C.Muted, theme.C.Dir, theme.C.Err
	cOK, cWarn, cSelBg, cDark   = theme.C.OK, theme.C.Warn, theme.C.SelBg, theme.C.Dark

	sTitle, sCrumb, sMuted, sDir = theme.Title, theme.Crumb, theme.Muted, theme.Dir
	sSel, sHeader, sErr, sOK     = theme.Sel, theme.Header, theme.ErrText, theme.OKText
	sWarn, sBar, sMode, sSection = theme.Warn, theme.Bar, theme.Mode, theme.Section
	sFound, sDanger, sCaution    = theme.Found, theme.Danger, theme.Caution

	// the grid's own
	sSelNull = lipgloss.NewStyle().Background(cSelBg).Foreground(cMuted)
	sCell    = lipgloss.NewStyle().Background(cAccent).Foreground(cDark).Bold(true)
	sLabel   = lipgloss.NewStyle().Foreground(cAccent).Width(18)
)
