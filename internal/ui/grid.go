package ui

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"dbreader/internal/db"

	tk "github.com/Gandalfalex/tuikit"
	"github.com/Gandalfalex/vim-kit/keys"
	"github.com/Gandalfalex/vim-kit/motion"
	"github.com/charmbracelet/x/ansi"
)

const (
	minColWidth = 4 // fits NULL
	maxColWidth = 40
	colGap      = 2
	widthSample = 200 // rows measured for column widths
)

// grid is a result set with a cell cursor: rows move with the vertical
// motions, columns with h and l.
type grid struct {
	res *db.Result
	vis []int // indices into res.Rows that pass the filter
	motion.Window
	col, left int // cursor column, first visible column
	widths    []int
	numeric   []bool
	filter    string

	sql    string
	browse *db.Browse // set when the grid shows a table, nil for free queries

	// V selects rows from selAnchor to the cursor, for yanking
	selecting bool
	selAnchor int

	// client-side sort of a free query; tables are sorted by the server
	sortCol  int
	sortDesc bool
}

func newGrid(res *db.Result, sql string, browse *db.Browse) grid {
	g := grid{res: res, sql: sql, browse: browse, sortCol: -1}
	g.widths = make([]int, len(res.Columns))
	g.numeric = make([]bool, len(res.Columns))
	for i, c := range res.Columns {
		g.widths[i] = max(minColWidth, ansi.StringWidth(tk.CleanLine(c, maxCellBytes)))
		g.numeric[i] = isNumeric(res.Types[i])
	}
	for _, row := range res.Rows[:min(len(res.Rows), widthSample)] {
		for i, c := range row {
			if w := ansi.StringWidth(tk.CleanLine(c.S, maxCellBytes)); w > g.widths[i] {
				g.widths[i] = min(w, maxColWidth)
			}
		}
	}
	g.refilter()
	return g
}

func isNumeric(typ string) bool {
	for _, t := range []string{"INT", "NUMBER", "NUMERIC", "DECIMAL", "FLOAT", "DOUBLE", "REAL", "SERIAL", "MONEY"} {
		if strings.Contains(typ, t) {
			return true
		}
	}
	return false
}

func (g *grid) setFilter(f string) {
	g.filter = f
	g.Cursor, g.Top = 0, 0
	g.sorted()
}

func (g *grid) refilter() {
	g.selecting = false
	g.vis = g.vis[:0]
	for i, row := range g.res.Rows {
		if g.filter == "" || rowMatches(row, g.filter) {
			g.vis = append(g.vis, i)
		}
	}
	g.Clamp(len(g.vis))
}

func rowMatches(row []db.Cell, f string) bool {
	for _, c := range row {
		if tk.SmartMatch(c.S, f) {
			return true
		}
	}
	return false
}

// selRange is the selected range of visible rows, in order.
func (g *grid) selRange() (from, to int) {
	return min(g.selAnchor, g.Cursor), max(g.selAnchor, g.Cursor)
}

func (g *grid) selected(r int) bool {
	from, to := g.selRange()
	return g.selecting && r >= from && r <= to
}

func (g *grid) row() ([]db.Cell, bool) {
	if g.res == nil || len(g.vis) == 0 {
		return nil, false
	}
	return g.res.Rows[g.vis[g.Cursor]], true
}

func (g *grid) cell() (db.Cell, bool) {
	row, ok := g.row()
	if !ok || g.col >= len(row) {
		return db.Cell{}, false
	}
	return row[g.col], true
}

// applyCol runs ev's horizontal motion over the columns.
func (g *grid) applyCol(ev keys.Event) bool {
	n := len(g.widths)
	switch ev.Action {
	case motion.Left, motion.WordBackward:
		g.col -= ev.Count
	case motion.Right, motion.WordForward, motion.WordEnd:
		g.col += ev.Count
	case motion.LineStart:
		g.col = 0
	case motion.LineEnd:
		g.col = n - 1
	default:
		return false
	}
	g.col = max(0, min(g.col, n-1))
	return true
}

// scrollCols adjusts left so the cursor column fits in w cells.
func (g *grid) scrollCols(w int) {
	g.left = min(g.left, g.col)
	for g.left < g.col {
		used := 0
		for i := g.left; i <= g.col; i++ {
			used += g.widths[i] + colGap
		}
		if used <= w {
			break
		}
		g.left++
	}
}

// sortBy cycles the client-side sort on col: ascending, descending, and
// back to the order the server returned.
func (g *grid) sortBy(col int) {
	switch {
	case g.sortCol != col:
		g.sortCol, g.sortDesc = col, false
	case !g.sortDesc:
		g.sortDesc = true
	default:
		g.sortCol = -1
	}
}

// sorted reorders the visible rows; the result itself keeps server order.
func (g *grid) sorted() {
	g.refilter()
	if g.sortCol < 0 {
		return
	}
	c, num := g.sortCol, g.numeric[g.sortCol]
	slices.SortStableFunc(g.vis, func(a, b int) int {
		x, y := g.res.Rows[a][c], g.res.Rows[b][c]
		r := 0
		switch {
		case x.Null || y.Null: // NULLs last, either direction
			if x.Null == y.Null {
				return 0
			}
			if x.Null {
				return 1
			}
			return -1
		case num:
			fx, _ := strconv.ParseFloat(x.S, 64)
			fy, _ := strconv.ParseFloat(y.S, 64)
			r = cmp.Compare(fx, fy)
		default:
			r = strings.Compare(x.S, y.S)
		}
		if g.sortDesc {
			return -r
		}
		return r
	})
}
