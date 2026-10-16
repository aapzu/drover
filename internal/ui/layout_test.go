package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapelihaanpuu/kangentic-tui/internal/config"
	"github.com/aapelihaanpuu/kangentic-tui/internal/engine"
	"github.com/aapelihaanpuu/kangentic-tui/internal/store"
)

func testModel(width, height, columns int, tasksPerColumn int) Model {
	board := &config.Board{}
	var tasks []*store.Task
	for i := range columns {
		id := fmt.Sprintf("c%d", i)
		board.Columns = append(board.Columns, config.Column{ID: id, Name: fmt.Sprintf("Col %d", i)})
		for j := range tasksPerColumn {
			tasks = append(tasks, &store.Task{ID: fmt.Sprintf("%s-t%d", id, j), Number: len(tasks) + 1, Title: "task", ColumnID: id})
		}
	}
	return Model{
		snap:     engine.Snapshot{Board: board, Tasks: tasks},
		width:    width,
		height:   height,
		selected: map[string]string{},
		prefs:    prefs{ColumnWidths: map[string]int{}},
	}
}

func sum(ws []int) int {
	total := 0
	for _, w := range ws {
		total += w
	}
	return total
}

func TestColumnWindowFillsWidth(t *testing.T) {
	m := testModel(120, 40, 4, 0)
	start, widths := m.columnWindow()
	if start != 0 || len(widths) != 4 || sum(widths) != 120 {
		t.Fatalf("got start %d widths %v", start, widths)
	}
}

func TestColumnWindowKeepsResizedWidth(t *testing.T) {
	m := testModel(120, 40, 4, 0)
	m.prefs.ColumnWidths["c1"] = 50
	_, widths := m.columnWindow()
	if widths[1] != 50 || sum(widths) != 120 {
		t.Fatalf("got widths %v", widths)
	}
}

func TestColumnWindowScrollsToSelection(t *testing.T) {
	m := testModel(60, 40, 6, 0)
	m.col = 5
	start, widths := m.columnWindow()
	if start+len(widths) != 6 || sum(widths) != 60 {
		t.Fatalf("got start %d widths %v", start, widths)
	}
}

func TestLayoutPreviewHeightIsClamped(t *testing.T) {
	m := testModel(120, 40, 3, 0)
	m.prefs.PreviewHeight = 100
	l := m.layout()
	if l.boardH != minBoardHeight || l.previewY != l.bodyY+minBoardHeight {
		t.Fatalf("got board %d preview %d", l.boardH, l.previewH)
	}
	m.prefs.HidePreview = true
	if l := m.layout(); l.previewH != 0 || l.boardH != l.bodyH {
		t.Fatalf("hidden preview still takes %d rows", l.previewH)
	}
}

func TestHitTestMatchesRenderedBoard(t *testing.T) {
	for _, d := range []density{densityNormal, densityCompact, densityTall} {
		m := testModel(120, 50, 3, 3)
		m.prefs.Density = d
		l := m.layout()
		lines := strings.Split(m.renderBoard(l), "\n")
		for _, c := range l.cols {
			for k, task := range m.tasksForColumn(c.index) {
				y := l.bodyY + columnHeaderHeight + k*d.cardHeight() + 1
				h := l.hitTest(m, c.x+3, y)
				if h.kind != hitCard || h.task.ID != task.ID {
					t.Fatalf("density %d: hit at %d,%d = %+v, want %s", d, c.x+3, y, h, task.ID)
				}
				row := ansi.Strip(lines[y-l.bodyY])
				if !strings.Contains(ansi.Cut(row, c.x, c.x+c.width), fmt.Sprintf("#%d", task.Number)) {
					t.Fatalf("density %d: row %d %q has no #%d in column %d", d, y, row, task.Number, c.index)
				}
			}
			if h := l.hitTest(m, c.x+c.width-1, l.bodyY+3); h.kind != hitColumnEdge || h.col != c.index {
				t.Fatalf("edge of column %d: %+v", c.index, h)
			}
		}
		if h := l.hitTest(m, 5, l.previewY); h.kind != hitPreviewEdge {
			t.Fatalf("preview border: %+v", h)
		}
		if h := l.hitTest(m, 5, l.previewY+2); h.kind != hitPreview {
			t.Fatalf("preview body: %+v", h)
		}
	}
}

func TestCardHeightMatchesDensity(t *testing.T) {
	m := testModel(120, 40, 1, 1)
	task := m.snap.Tasks[0]
	task.Title = strings.Repeat("long title ", 10)
	for _, d := range []density{densityNormal, densityCompact, densityTall} {
		m.prefs.Density = d
		if got := lipgloss.Height(m.renderCard(task, 30, false, false)); got != d.cardHeight() {
			t.Fatalf("density %d: card is %d rows, want %d", d, got, d.cardHeight())
		}
	}
}

func TestOverlay(t *testing.T) {
	got := overlay("aaaaaa\nbbbbbb\ncccccc", "XY\nZW", 2, 1)
	want := "aaaaaa\nbbXYbb\nccZWcc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := overlay("ab", "XY", 4, 0); got != "ab  XY" {
		t.Fatalf("past line end: got %q", got)
	}
}

func TestTmuxKey(t *testing.T) {
	cases := []struct {
		msg          tea.KeyMsg
		literal, key string
	}{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hé")}, "hé", ""},
		{tea.KeyMsg{Type: tea.KeySpace}, " ", ""},
		{tea.KeyMsg{Type: tea.KeyEnter}, "", "Enter"},
		{tea.KeyMsg{Type: tea.KeyEsc}, "", "Escape"},
		{tea.KeyMsg{Type: tea.KeyBackspace}, "", "BSpace"},
		{tea.KeyMsg{Type: tea.KeyShiftTab}, "", "BTab"},
		{tea.KeyMsg{Type: tea.KeyCtrlC}, "", "C-c"},
		{tea.KeyMsg{Type: tea.KeyCtrlUp}, "", "C-Up"},
		{tea.KeyMsg{Type: tea.KeyShiftLeft}, "", "S-Left"},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p"), Alt: true}, "", "M-p"},
		{tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, "", "M-Enter"},
		{tea.KeyMsg{Type: tea.KeyF5}, "", "F5"},
		{tea.KeyMsg{Type: tea.KeyPgDown}, "", "NPage"},
	}
	for _, c := range cases {
		literal, key := tmuxKey(c.msg)
		if literal != c.literal || key != c.key {
			t.Errorf("%q: got (%q, %q) want (%q, %q)", c.msg.String(), literal, key, c.literal, c.key)
		}
	}
}
