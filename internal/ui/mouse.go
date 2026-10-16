package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type dragKind int

const (
	dragNone dragKind = iota
	dragPending
	dragCard
	dragColumn
	dragPreview
)

type dragState struct {
	kind     dragKind
	task     string
	fromCol  int
	col      int
	colX     int
	x, y     int
	startX   int
	startY   int
	clickAt  time.Time
	clickFor string
}

const doubleClick = 400 * time.Millisecond

func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeBoard && m.mode != modeLive {
		m.drag = dragState{}
		return m, nil
	}
	l := m.layout()
	switch {
	case msg.Action == tea.MouseActionPress && tea.MouseEvent(msg).IsWheel():
		h := l.hitTest(m, msg.X, msg.Y)
		if h.kind == hitPreview || h.kind == hitPreviewEdge {
			if msg.Button == tea.MouseButtonWheelUp {
				return m.scrollPreview(3)
			}
			return m.scrollPreview(-3)
		}
		if h.kind != hitCard && h.kind != hitColumn {
			return m, nil
		}
		m.col = h.col
		key := tea.KeyMsg{Type: tea.KeyDown}
		if msg.Button == tea.MouseButtonWheelUp {
			key.Type = tea.KeyUp
		}
		return m.updateBoard(key)

	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		h := l.hitTest(m, msg.X, msg.Y)
		m.drag = dragState{clickAt: m.drag.clickAt, clickFor: m.drag.clickFor}
		switch h.kind {
		case hitCard:
			m.mode = modeBoard
			m.col = h.col
			m.selected[m.columns()[h.col].ID] = h.task.ID
			if m.drag.clickFor == h.task.ID && time.Since(m.drag.clickAt) < doubleClick {
				m.drag = dragState{}
				return m.updateBoard(tea.KeyMsg{Type: tea.KeyEnter})
			}
			m.drag = dragState{kind: dragPending, task: h.task.ID, fromCol: h.col,
				startX: msg.X, startY: msg.Y, x: msg.X, y: msg.Y, clickAt: time.Now(), clickFor: h.task.ID}
		case hitColumn:
			m.mode = modeBoard
			m.col = h.col
		case hitColumnEdge:
			m.drag = dragState{kind: dragColumn, col: h.col, colX: h.box.x}
		case hitPreviewEdge:
			m.drag = dragState{kind: dragPreview}
		case hitPreview:
			if m.mode != modeLive {
				return m.startLive()
			}
		}

	case msg.Action == tea.MouseActionMotion:
		switch m.drag.kind {
		case dragPending:
			if abs(msg.X-m.drag.startX) >= 2 || msg.Y != m.drag.startY {
				m.drag.kind = dragCard
			}
			m.drag.x, m.drag.y = msg.X, msg.Y
		case dragCard:
			m.drag.x, m.drag.y = msg.X, msg.Y
		case dragColumn:
			id := m.columns()[m.drag.col].ID
			m.prefs.ColumnWidths[id] = clamp(msg.X-m.drag.colX+2, minResizedWidth, m.width)
		case dragPreview:
			m.prefs.PreviewHeight = clamp(l.bodyY+l.bodyH-msg.Y, minPreviewHeight, l.bodyH-minBoardHeight)
		}

	case msg.Action == tea.MouseActionRelease:
		d := m.drag
		m.drag = dragState{clickAt: d.clickAt, clickFor: d.clickFor}
		switch d.kind {
		case dragCard:
			m.drag = dragState{}
			h := l.hitTest(m, d.x, d.y)
			t := m.task(d.task)
			if t == nil || h.kind == hitNone || h.kind == hitPreview || h.kind == hitPreviewEdge || h.col == d.fromCol {
				return m, nil
			}
			target := m.columns()[h.col]
			m.col = h.col
			m.selected[target.ID] = t.ID
			return m, m.moveCmd(t, target)
		case dragColumn, dragPreview:
			m.savePrefs()
		}
	}
	return m, nil
}

// dropTarget is the column a dragged card would land in, or -1.
func (m Model) dropTarget(l layout) int {
	if m.drag.kind != dragCard {
		return -1
	}
	h := l.hitTest(m, m.drag.x, m.drag.y)
	if h.kind != hitCard && h.kind != hitColumn && h.kind != hitColumnEdge {
		return -1
	}
	if h.col == m.drag.fromCol {
		return -1
	}
	return h.col
}

// overlay draws fg over base with its top-left corner at x, y.
func overlay(base, fg string, x, y int) string {
	lines := strings.Split(base, "\n")
	for i, fl := range strings.Split(fg, "\n") {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		bl := lines[row]
		if pad := x - ansi.StringWidth(bl); pad > 0 {
			bl += strings.Repeat(" ", pad)
		}
		lines[row] = ansi.Truncate(bl, x, "") + fl + ansi.TruncateLeft(bl, x+lipgloss.Width(fl), "")
	}
	return strings.Join(lines, "\n")
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
