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
	dragLane
	dragAssistant
	dragSelect
	dragForward
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
	target   mouseTarget
}

const doubleClick = 400 * time.Millisecond

func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeBoard && m.mode != modeLive && m.mode != modeFilter && m.mode != modeAssistant {
		m.drag = dragState{}
		return m, nil
	}
	l := m.layout()
	if target, ok := m.forwardTarget(msg, l); ok {
		return m.forwardMouse(target, msg, l)
	}
	switch {
	case msg.Action == tea.MouseActionPress && (msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown):
		h := l.hitTest(m, msg.X, msg.Y)
		if h.kind == hitAssistant || h.kind == hitAssistantEdge {
			return m.wheelAssistant(msg, l)
		}
		if h.kind == hitPreview || h.kind == hitPreviewEdge {
			if name, ok := m.wheelTarget(); ok {
				x, y := l.previewCell(msg)
				m.keys.send(wheelJob(name, msg.Button == tea.MouseButtonWheelUp, x, y))
				cmd := m.requestPreview(true)
				return m, cmd
			}
			if msg.Button == tea.MouseButtonWheelUp {
				return m.scrollPreview(3)
			}
			return m.scrollPreview(-3)
		}
		if m.mode == modeLive || m.mode == modeAssistant || (h.kind != hitCard && h.kind != hitColumn) {
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
		m.sel = selection{}
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
		case hitLaneEdge:
			m.drag = dragState{kind: dragLane}
		case hitAssistantEdge:
			m.drag = dragState{kind: dragAssistant}
		case hitPreview:
			if t := m.current(); t != nil && m.showsCapture(t) {
				if p, ok := m.selPointAt(l, msg.X, msg.Y); ok {
					m.drag = dragState{kind: dragSelect}
					m.sel.anchor, m.sel.head = p, p
				}
			}
			if m.mode != modeLive {
				return m.startLive()
			}
		case hitAssistant:
			if m.mode != modeAssistant {
				cmd := m.focusAssistant()
				return m, cmd
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
			m.prefs.ColumnWidths[id] = clamp(msg.X-m.drag.colX+2, minResizedWidth, m.boardWidth())
		case dragPreview:
			m.prefs.PreviewHeight = clamp(l.bodyY+l.bodyH-msg.Y, minPreviewHeight, l.bodyH-minBoardHeight)
		case dragLane:
			if lo, hi := laneBounds(l.boardH); lo <= hi {
				m.prefs.ReviewLaneHeight = clamp(l.bodyY+l.boardH-msg.Y, lo, hi)
			}
		case dragAssistant:
			m.prefs.AssistantWidth = m.clampAssistantWidth(m.width - msg.X)
		case dragSelect:
			return m.extendSelection(l, msg.X, msg.Y)
		}

	case msg.Action == tea.MouseActionRelease:
		d := m.drag
		m.drag = dragState{clickAt: d.clickAt, clickFor: d.clickFor}
		switch d.kind {
		case dragCard:
			m.drag = dragState{}
			h := l.hitTest(m, d.x, d.y)
			t := m.task(d.task)
			if t == nil || (h.kind != hitCard && h.kind != hitColumn && h.kind != hitColumnEdge) || h.col == d.fromCol {
				return m, nil
			}
			target := m.columns()[h.col]
			m.col = h.col
			m.selected[target.ID] = t.ID
			return m, m.moveCmd(t, target)
		case dragColumn, dragPreview, dragLane, dragAssistant:
			m.savePrefs()
		case dragSelect:
			if text := m.selectedText(); strings.TrimSpace(text) != "" {
				return m, copyText(text)
			}
		}
	}
	return m, nil
}

// mouseTarget is a pane whose agent reads mouse reports.
type mouseTarget struct {
	session   string
	assistant bool
}

// forwardTarget is the pane msg belongs to: a press inside the live preview
// or the focused assistant panel, or the motion and release that follow such
// a press.
func (m Model) forwardTarget(msg tea.MouseMsg, l layout) (mouseTarget, bool) {
	if msg.Action != tea.MouseActionPress {
		return m.drag.target, m.drag.kind == dragForward
	}
	switch msg.Button {
	case tea.MouseButtonLeft, tea.MouseButtonMiddle, tea.MouseButtonRight:
		preview, previewOK := m.clickTarget()
		assistant, assistantOK := m.assistantClickTarget()
		if !previewOK && !assistantOK {
			return mouseTarget{}, false
		}
		switch l.hitTest(m, msg.X, msg.Y).kind {
		case hitPreview:
			return mouseTarget{session: preview}, previewOK
		case hitAssistant:
			return mouseTarget{session: assistant, assistant: true}, assistantOK
		}
	}
	return mouseTarget{}, false
}

func (m Model) forwardMouse(t mouseTarget, msg tea.MouseMsg, l layout) (tea.Model, tea.Cmd) {
	x, y := l.previewCell(msg)
	focused := m.mode == modeLive
	refresh := func() tea.Cmd { return m.requestPreview(true) }
	if t.assistant {
		x, y = l.assistantCell(msg)
		focused = m.mode == modeAssistant
		refresh = func() tea.Cmd { return afterKey(m.captureAssistantCmd()) }
	}
	switch msg.Action {
	case tea.MouseActionPress:
		m.drag = dragState{kind: dragForward, target: t}
		m.sel = selection{}
	case tea.MouseActionMotion:
		if focused {
			m.keys.send(mouseJob(t.session, msg, x, y))
		}
		return m, nil
	case tea.MouseActionRelease:
		m.drag = dragState{}
	}
	m.keys.send(mouseJob(t.session, msg, x, y))
	return m, refresh()
}

// extendSelection moves the selection's end to the cell under the pointer,
// scrolling the preview when the pointer is on or past its border.
func (m Model) extendSelection(l layout, x, y int) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if d := edgeScroll(l, y); d != 0 {
		var next tea.Model
		next, cmd = m.scrollPreview(d)
		m = next.(Model)
	}
	if p, ok := m.selPointAt(l, x, y); ok {
		m.sel.head = p
	}
	return m, cmd
}

func edgeScroll(l layout, y int) int {
	switch {
	case y <= l.previewY:
		return 1
	case y >= l.previewY+l.previewH-1:
		return -1
	}
	return 0
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
