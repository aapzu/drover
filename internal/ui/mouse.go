package ui

import (
	"strings"
	"time"

	"github.com/aapzu/drover/internal/assistant"
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
	dragAssistant
	dragSelect
	dragPane
)

type dragState struct {
	kind     dragKind
	pane     mousePane
	button   tea.MouseButton
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
	if m.mode != modeBoard && m.mode != modeLive && m.mode != modeFilter && m.mode != modeAssistant {
		m.drag = dragState{}
		return m, nil
	}
	if m.drag.kind == dragPane && msg.Action != tea.MouseActionPress {
		return m.forwardDrag(msg)
	}
	l := m.layout()
	var h hit
	if msg.Action == tea.MouseActionPress {
		h = l.hitTest(m, msg.X, msg.Y)
		if next, cmd, ok := m.forwardPress(msg, l, h); ok {
			return next, cmd
		}
	}
	switch {
	case msg.Action == tea.MouseActionPress && (msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown):
		if h.kind == hitAssistant || h.kind == hitAssistantEdge {
			return m.wheelAssistant(msg)
		}
		if h.kind == hitPreview || h.kind == hitPreviewEdge {
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
		case dragColumn, dragPreview, dragAssistant:
			m.savePrefs()
		case dragSelect:
			if text := m.selectedText(); strings.TrimSpace(text) != "" {
				return m, copyText(text)
			}
		}
	}
	return m, nil
}

// mousePane is a pane whose app reads mouse reports. x, y, w and h are its
// box on screen, border included.
type mousePane struct {
	session   string
	x, y      int
	w, h      int
	assistant bool
}

// cell is where msg lands inside the pane, 1-based and clamped to its
// interior so a drag that leaves the pane still reports its nearest edge.
func (p mousePane) cell(msg tea.MouseMsg) (int, int) {
	return clamp(msg.X-p.x, 1, p.w-2), clamp(msg.Y-p.y, 1, p.h-2)
}

// paneAt is the pane under a hit if its app wants the mouse. A pane that
// doesn't, or that is showing scrollback, keeps drover's own handling.
func (m Model) paneAt(l layout, h hit) (mousePane, bool) {
	switch h.kind {
	case hitPreview, hitPreviewEdge:
		name, ok := m.wheelTarget()
		return mousePane{session: name, y: l.previewY, w: l.boardW, h: l.previewH}, ok
	case hitAssistant, hitAssistantEdge:
		if !m.assist.running() || !m.assist.mouse || m.assist.history != 0 {
			return mousePane{}, false
		}
		return mousePane{session: assistant.SessionName(m.eng.Root), x: l.boardW, y: l.bodyY,
			w: l.assistantW, h: l.bodyH, assistant: true}, true
	}
	return mousePane{}, false
}

func (m Model) sendMouse(p mousePane, msg tea.MouseMsg) bool {
	x, y := p.cell(msg)
	job, ok := mouseJob(p.session, msg, x, y)
	if ok {
		m.keys.send(job)
	}
	return ok
}

func (m *Model) refreshPane(p mousePane) tea.Cmd {
	if p.assistant {
		return afterKey(m.captureAssistantCmd())
	}
	return m.requestPreview(true)
}

func (m Model) focusPane(p mousePane) (Model, tea.Cmd) {
	switch {
	case p.assistant && m.mode != modeAssistant:
		return m, m.focusAssistant()
	case !p.assistant && m.mode != modeLive:
		return m.startLive()
	}
	return m, nil
}

// forwardPress sends wheel turns and clicks over a pane to its app. The
// pane's resize edge and the board stay drover's, and so does everything over
// a pane that doesn't read the mouse.
func (m Model) forwardPress(msg tea.MouseMsg, l layout, h hit) (Model, tea.Cmd, bool) {
	wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
	if !wheel && h.kind != hitPreview && h.kind != hitAssistant {
		return m, nil, false
	}
	p, ok := m.paneAt(l, h)
	if !ok || !m.sendMouse(p, msg) {
		return m, nil, false
	}
	if wheel {
		return m, m.refreshPane(p), true
	}
	m.drag = dragState{kind: dragPane, pane: p, button: msg.Button}
	m.sel = selection{}
	var focus tea.Cmd
	if msg.Button == tea.MouseButtonLeft {
		m, focus = m.focusPane(p)
	}
	return m, focus, true
}

// forwardDrag sends the motion and release of a drag that began in a pane,
// whatever it is over now.
func (m Model) forwardDrag(msg tea.MouseMsg) (Model, tea.Cmd) {
	d := m.drag
	msg.Button = d.button
	m.sendMouse(d.pane, msg)
	if msg.Action == tea.MouseActionMotion {
		return m, nil
	}
	m.drag = dragState{}
	return m, m.refreshPane(d.pane)
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
