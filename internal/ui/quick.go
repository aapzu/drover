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
	quickRowHeight  = 2
)

// quickTasks lists the quick tasks in creation order, so a row keeps its place.
func (m Model) quickTasks() []*store.Task {
	var out []*store.Task
	for _, t := range m.snap.Tasks {
		if t.Quick {
			out = append(out, t)
		}
	}
	return out
}

// quickIndex is the position of the selected quick task in tasks, the first
// when none is selected, and -1 when there are none.
func (m Model) quickIndex(tasks []*store.Task) int {
	if len(tasks) == 0 {
		return -1
	}
	return max(0, slices.IndexFunc(tasks, func(t *store.Task) bool { return t.ID == m.quickSel }))
}

// quickCurrent is the selected quick task, the first one when none is.
func (m Model) quickCurrent() *store.Task {
	tasks := m.quickTasks()
	if i := m.quickIndex(tasks); i >= 0 {
		return tasks[i]
	}
	return nil
}

// quickWidth is the width of the quick-task panel beside the board, 0 while
// it is hidden or the board would get too narrow.
func (m Model) quickWidth() int {
	if !m.prefs.ShowQuick || !m.quickFits() {
		return 0
	}
	return quickPanelWidth
}

// quickFits reports whether the board stays wide enough with the panel beside it.
func (m Model) quickFits() bool { return m.boardWidth()-quickPanelWidth >= minColumnWidth }

// columnsWidth is the width of the board's columns: what the quick-task panel
// leaves of the board.
func (m Model) columnsWidth() int { return m.boardWidth() - m.quickWidth() }

// quickRows is how many quick tasks fit in a panel of the given height.
func quickRows(height int) int { return max(1, (height-2)/quickRowHeight) }

// quickStart is the index of the first quick task shown, keeping the selected
// one in view.
func (m Model) quickStart(rows int) int {
	return max(0, m.quickIndex(m.quickTasks())-rows+1)
}

// openQuick shows the panel and moves the keyboard to it, or flashes a notice
// when the terminal is too narrow for it.
func (m *Model) openQuick() {
	if !m.quickFits() {
		m.setFlash("The terminal is too narrow for the quick tasks panel")
		return
	}
	m.prefs.ShowQuick = true
	m.savePrefs()
	m.mode = modeQuick
}

// updateQuick handles the panel's own keys. The keys listed last go to
// updateBoard, which acts on the selected quick task because current() returns
// it in modeQuick; any other key is ignored so it can't reach a board card.
func (m Model) updateQuick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tasks := m.quickTasks()
	sel := m.quickIndex(tasks)
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
	cur := m.quickIndex(tasks)
	rows := quickRows(height)
	start := m.quickStart(rows)
	for i := start; i < min(len(tasks), start+rows); i++ {
		t := tasks[i]
		marker, title := "  ", ansi.Truncate(fmt.Sprintf("#%d %s", t.Number, taskTitle(t)), inner-2, "…")
		if i == cur && focused {
			marker, title = "▸ ", activeText.Render(title)
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
