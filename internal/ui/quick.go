package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/store"
)

const (
	quickKey        = "w"
	quickPanelWidth = 34
	// quickRowHeight is a quick task's title line and its status line.
	quickRowHeight = 2
)

// quickTasks lists the quick tasks, oldest first so a row keeps its place.
func (m Model) quickTasks() []*store.Task {
	var out []*store.Task
	for _, t := range m.snap.Tasks {
		if t.Quick {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b *store.Task) int { return a.Number - b.Number })
	return out
}

// quickCurrent is the selected quick task, the first one when none is.
func (m Model) quickCurrent() *store.Task {
	tasks := m.quickTasks()
	if len(tasks) == 0 {
		return nil
	}
	for _, t := range tasks {
		if t.ID == m.quickSel {
			return t
		}
	}
	return tasks[0]
}

// quickWidth is the width of the quick-task panel beside the board, 0 while
// it is hidden or the board would get too narrow.
func (m Model) quickWidth() int {
	if !m.prefs.ShowQuick || m.boardWidth()-quickPanelWidth < minColumnWidth {
		return 0
	}
	return quickPanelWidth
}

// columnsWidth is the width of the board's columns: what the quick-task panel
// leaves of the board.
func (m Model) columnsWidth() int { return m.boardWidth() - m.quickWidth() }

// quickRows is how many quick tasks fit in a panel of the given height.
func quickRows(height int) int { return max(1, (height-2)/quickRowHeight) }

// quickStart is the index of the first quick task shown, keeping the selected
// one in view.
func (m Model) quickStart(rows int) int {
	sel := slices.IndexFunc(m.quickTasks(), func(t *store.Task) bool { return t == m.quickCurrent() })
	return max(0, sel-rows+1)
}

// openQuick shows the panel and moves the keyboard to it.
func (m *Model) openQuick() {
	m.prefs.ShowQuick = true
	m.savePrefs()
	if m.quickWidth() == 0 {
		m.prefs.ShowQuick = false
		m.setFlash("The terminal is too narrow for the quick tasks panel")
		return
	}
	m.mode = modeQuick
}

func (m Model) updateQuick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tasks := m.quickTasks()
	sel := slices.IndexFunc(tasks, func(t *store.Task) bool { return t == m.quickCurrent() })
	switch msg.String() {
	case "esc":
		m.mode = modeBoard
	case quickKey:
		m.prefs.ShowQuick = false
		m.savePrefs()
		m.mode = modeBoard
	case "up", "k", "down", "j", "g", "G":
		if len(tasks) == 0 {
			break
		}
		switch msg.String() {
		case "up", "k":
			sel = max(0, sel-1)
		case "down", "j":
			sel = min(len(tasks)-1, sel+1)
		case "g":
			sel = 0
		case "G":
			sel = len(tasks) - 1
		}
		m.quickSel = tasks[sel].ID
	case "n":
		if m.startingQuick {
			m.setFlash("Already starting a quick task")
			break
		}
		m.startingQuick = true
		m.setFlash("Starting a quick task…")
		eng := m.eng
		return m, func() tea.Msg {
			t, err := eng.NewQuick()
			return sessionStartedMsg{task: t, err: err, snap: eng.Snapshot(), quick: true}
		}
	case "enter", "a", "s", "r", "S", "e", "E", "x", "D", "delete", "tab", "pgup", "pgdown", "p", "+", "=", "-", "_", "c", "u", "i",
		"q", "ctrl+c", "?", paletteKey, askKey, newAskKey, zoomKey, liveExitKey:
		return m.updateBoard(msg)
	}
	return m, nil
}

func (m Model) renderQuick(width, height int) string {
	focused := m.mode == modeQuick
	tasks := m.quickTasks()
	inner := width - 2
	var lines []string
	if len(tasks) == 0 {
		hint := "Press " + quickKey + ", then n, to start one"
		if focused {
			hint = "Press n to start one"
		}
		lines = strings.Split(ansi.Wordwrap(dim.Render("No quick tasks. "+hint+": a session with no card, column or PR."), inner, " "), "\n")
	}
	cur := m.quickCurrent()
	rows := quickRows(height)
	start := m.quickStart(rows)
	for i := start; i < min(len(tasks), start+rows); i++ {
		t := tasks[i]
		marker, title := "  ", fmt.Sprintf("#%d %s", t.Number, taskTitle(t))
		if t == cur && focused {
			marker = "▸ "
			title = activeText.Render(ansi.Truncate(title, inner-2, "…"))
		} else {
			title = ansi.Truncate(title, inner-2, "…")
		}
		lines = append(lines, marker+title, "  "+ansi.Truncate(m.status(t), inner-2, "…"))
	}
	style := panelStyle
	if focused {
		style = style.BorderForeground(accent)
	}
	title := fmt.Sprintf(" quick tasks %d ", len(tasks))
	return titledBox(style, title, strings.Join(lines, "\n"), width, height)
}
