package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func wheelDown(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
}

func TestWheelOverBoardKeepsLiveCardSelected(t *testing.T) {
	m := testModel(120, 40, 2, 3)
	m.col = 1
	m.snap.Live = map[string]string{"c1-t0": "sess"}
	m.mode = modeLive
	y := m.layout().bodyY + columnHeaderHeight + 1

	next, _ := m.Update(wheelDown(1, y))
	got := next.(Model)
	if got.mode != modeLive || got.col != 1 || got.current().ID != "c1-t0" {
		t.Fatalf("mode %v col %d card %s", got.mode, got.col, got.current().ID)
	}
}

func TestWheelOverPreviewScrollsInLiveMode(t *testing.T) {
	m := testModel(120, 40, 2, 3)
	m.snap.Live = map[string]string{"c0-t0": "sess"}
	m.mode = modeLive
	m.previewHistory = 100
	l := m.layout()

	up := tea.MouseMsg{X: 5, Y: l.previewY + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}
	next, _ := m.Update(up)
	got := next.(Model)
	if got.mode != modeLive || got.previewScroll != 3 {
		t.Fatalf("mode %v scroll %d", got.mode, got.previewScroll)
	}
}

func TestHorizontalWheelOverPreviewDoesNotScroll(t *testing.T) {
	m := testModel(120, 40, 2, 3)
	m.snap.Live = map[string]string{"c0-t0": "sess"}
	m.mode = modeLive
	m.previewHistory = 100
	m.previewScroll = 3
	l := m.layout()

	for _, button := range []tea.MouseButton{tea.MouseButtonWheelLeft, tea.MouseButtonWheelRight} {
		msg := tea.MouseMsg{X: 5, Y: l.previewY + 2, Action: tea.MouseActionPress, Button: button}
		next, _ := m.Update(msg)
		if got := next.(Model).previewScroll; got != 3 {
			t.Fatalf("button %v moved scroll to %d", button, got)
		}
	}
}

func TestWheelOverBoardMovesSelection(t *testing.T) {
	m := testModel(120, 40, 2, 3)
	m.col = 1
	y := m.layout().bodyY + columnHeaderHeight + 1

	next, _ := m.Update(wheelDown(1, y))
	got := next.(Model)
	if got.col != 0 || got.current().ID != "c0-t1" {
		t.Fatalf("col %d card %s", got.col, got.current().ID)
	}
}
