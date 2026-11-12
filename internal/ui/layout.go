package ui

import (
	"slices"

	"github.com/aapzu/drover/internal/store"
)

const (
	headerHeight       = 1
	footerHeight       = 1
	columnHeaderHeight = 2
	minColumnWidth     = 28
	minResizedWidth    = 16
	minPreviewHeight   = 6
	minBoardHeight     = 8
)

// colBox is a column's place on the board. y is relative to the top of the
// board, as the review lane sits in a row of its own below the others.
type colBox struct {
	index, x, y, width, height int
	first, slots               int
}

// layout is the screen geometry shared by View and mouse hit-testing.
type layout struct {
	bodyY, bodyH       int
	boardH             int
	boardW, assistantW int
	previewY, previewH int
	cols               []colBox
	rows               [][]colBox
	hiddenColumns      bool
}

func (m Model) bodyHeight() int { return max(0, m.height-headerHeight-footerHeight) }

func (m Model) layout() layout {
	l := layout{bodyY: headerHeight, bodyH: m.bodyHeight()}
	l.assistantW, l.boardW = m.assistantWidth(), m.boardWidth()
	if !m.prefs.HidePreview && l.bodyH >= minPreviewHeight+minBoardHeight {
		h := m.prefs.PreviewHeight
		if h == 0 {
			h = l.bodyH * 2 / 5
		}
		l.previewH = clamp(h, minPreviewHeight, l.bodyH-minBoardHeight)
	}
	l.boardH = l.bodyH - l.previewH
	l.previewY = l.bodyY + l.boardH

	lanes := m.lanes()
	heights := laneHeights(l.boardH, len(lanes), m.prefs.Density.cardHeight())
	y := 0
	for k, idxs := range lanes {
		start, widths := m.columnWindow(idxs)
		l.hiddenColumns = l.hiddenColumns || start > 0 || start+len(widths) < len(idxs)
		slots := max(1, (heights[k]-columnHeaderHeight)/m.prefs.Density.cardHeight())
		var row []colBox
		x := 0
		for j, w := range widths {
			i := idxs[start+j]
			box := colBox{index: i, x: x, y: y, width: w, height: heights[k], slots: slots}
			if i == m.col {
				if sel := m.selectedIndex(); sel >= slots {
					box.first = sel - slots + 1
				}
			}
			row = append(row, box)
			x += w
		}
		l.rows = append(l.rows, row)
		l.cols = append(l.cols, row...)
		y += heights[k]
	}
	return l
}

// laneHeights splits the board's height between its lanes. The review lane
// gets two fifths, but at least room for a card, up to half the board.
func laneHeights(total, lanes, cardHeight int) []int {
	if lanes < 2 {
		return []int{total}
	}
	review := max(total*2/5, min(total/2, columnHeaderHeight+cardHeight))
	return []int{total - review, review}
}

// columnWindow picks the columns of one lane, given by their indexes, that fit
// around the selected one, and their widths. start is a position in idxs.
// Resized columns keep their width; the rest share what is left.
func (m Model) columnWindow(idxs []int) (start int, widths []int) {
	cols, width := m.columns(), m.boardWidth()
	if len(idxs) == 0 || width <= 0 {
		return 0, nil
	}
	want := func(k int) (int, bool) {
		if w, ok := m.prefs.ColumnWidths[cols[idxs[k]].ID]; ok {
			return clamp(w, minResizedWidth, width), true
		}
		return minColumnWidth, false
	}
	cur := max(0, slices.Index(idxs, m.col))
	start, end := cur, cur+1
	used, _ := want(cur)
	for grew := true; grew; {
		grew = false
		if end < len(idxs) {
			if w, _ := want(end); used+w <= width {
				used += w
				end++
				grew = true
			}
		}
		if start > 0 {
			if w, _ := want(start - 1); used+w <= width {
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
	slack := width - used
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
	hitAssistant
	hitAssistantEdge
)

type hit struct {
	kind hitKind
	col  int
	box  colBox
	task *store.Task
}

func (l layout) hitTest(m Model, x, y int) hit {
	if l.assistantW > 0 && x >= l.boardW && y >= l.bodyY && y < l.bodyY+l.bodyH {
		if x == l.boardW {
			return hit{kind: hitAssistantEdge}
		}
		return hit{kind: hitAssistant}
	}
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
		if x < c.x || x >= c.x+c.width || y < l.bodyY+c.y || y >= l.bodyY+c.y+c.height {
			continue
		}
		// The two cells right of the cards are the column's resize handle.
		if x >= c.x+c.width-2 {
			return hit{kind: hitColumnEdge, col: c.index, box: c}
		}
		h := hit{kind: hitColumn, col: c.index, box: c}
		row := y - l.bodyY - c.y - columnHeaderHeight
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
