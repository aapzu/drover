package ui

import "github.com/aapzu/drover/internal/store"

const (
	headerHeight       = 1
	footerHeight       = 1
	columnHeaderHeight = 2
	minColumnWidth     = 28
	minResizedWidth    = 16
	minPreviewHeight   = 6
	minBoardHeight     = 8
)

type colBox struct {
	index, x, width int
	first, slots    int
}

// layout is the screen geometry shared by View and mouse hit-testing.
type layout struct {
	bodyY, bodyH       int
	boardH             int
	previewY, previewH int
	cols               []colBox
	hiddenColumns      bool
}

func (m Model) layout() layout {
	l := layout{bodyY: headerHeight, bodyH: max(0, m.height-headerHeight-footerHeight)}
	if !m.prefs.HidePreview && l.bodyH >= minPreviewHeight+minBoardHeight {
		h := m.prefs.PreviewHeight
		if h == 0 {
			h = l.bodyH * 2 / 5
		}
		l.previewH = clamp(h, minPreviewHeight, l.bodyH-minBoardHeight)
	}
	l.boardH = l.bodyH - l.previewH
	l.previewY = l.bodyY + l.boardH

	start, widths := m.columnWindow()
	l.hiddenColumns = start > 0 || start+len(widths) < len(m.columns())
	slots := max(1, (l.boardH-columnHeaderHeight)/m.prefs.Density.cardHeight())
	x := 0
	for k, w := range widths {
		i := start + k
		box := colBox{index: i, x: x, width: w, slots: slots}
		if i == m.col {
			if sel := m.selectedIndex(); sel >= slots {
				box.first = sel - slots + 1
			}
		}
		l.cols = append(l.cols, box)
		x += w
	}
	return l
}

// columnWindow picks the columns that fit around the selected one and their
// widths. Resized columns keep their width; the rest share what is left.
func (m Model) columnWindow() (start int, widths []int) {
	cols := m.columns()
	if len(cols) == 0 || m.width <= 0 {
		return 0, nil
	}
	want := func(i int) (int, bool) {
		if w, ok := m.prefs.ColumnWidths[cols[i].ID]; ok {
			return clamp(w, minResizedWidth, m.width), true
		}
		return minColumnWidth, false
	}
	cur := clamp(m.col, 0, len(cols)-1)
	start, end := cur, cur+1
	used, _ := want(cur)
	for grew := true; grew; {
		grew = false
		if end < len(cols) {
			if w, _ := want(end); used+w <= m.width {
				used += w
				end++
				grew = true
			}
		}
		if start > 0 {
			if w, _ := want(start - 1); used+w <= m.width {
				used += w
				start--
				grew = true
			}
		}
	}

	var auto []int
	for i := start; i < end; i++ {
		w, fixed := want(i)
		widths = append(widths, w)
		if !fixed {
			auto = append(auto, i-start)
		}
	}
	slack := m.width - used
	if len(auto) == 0 {
		widths[len(widths)-1] += slack
		return start, widths
	}
	for k, i := range auto {
		widths[i] += slack / len(auto)
		if k == len(auto)-1 {
			widths[i] += slack % len(auto)
		}
	}
	return start, widths
}

type hitKind int

const (
	hitNone hitKind = iota
	hitCard
	hitColumn
	hitColumnEdge
	hitPreview
	hitPreviewEdge
)

type hit struct {
	kind hitKind
	col  int
	box  colBox
	task *store.Task
}

func (l layout) hitTest(m Model, x, y int) hit {
	if l.previewH > 0 && y >= l.previewY && y < l.previewY+l.previewH {
		if y == l.previewY {
			return hit{kind: hitPreviewEdge}
		}
		return hit{kind: hitPreview}
	}
	if y < l.bodyY || y >= l.bodyY+l.boardH {
		return hit{}
	}
	for _, c := range l.cols {
		if x < c.x || x >= c.x+c.width {
			continue
		}
		// The two cells right of the cards are the column's resize handle.
		if x >= c.x+c.width-2 {
			return hit{kind: hitColumnEdge, col: c.index, box: c}
		}
		h := hit{kind: hitColumn, col: c.index, box: c}
		row := y - l.bodyY - columnHeaderHeight
		if row < 0 {
			return h
		}
		k := row / m.prefs.Density.cardHeight()
		tasks := m.tasksForColumn(c.index)
		if k < c.slots && c.first+k < len(tasks) {
			h.kind, h.task = hitCard, tasks[c.first+k]
		}
		return h
	}
	return hit{}
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
