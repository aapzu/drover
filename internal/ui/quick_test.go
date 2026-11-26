package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

func quickModel(t *testing.T) Model {
	t.Helper()
	m := testModel(140, 40, 3, 1)
	m.eng = &engine.Engine{Root: t.TempDir()}
	m.snap.Tasks = append(m.snap.Tasks,
		&store.Task{ID: "q1", Number: 10, Title: "try the parser", Quick: true},
		&store.Task{ID: "q2", Number: 11, Title: "explain the diff", Quick: true},
	)
	return m
}

func pressBoard(t *testing.T, m Model, k tea.KeyMsg) Model {
	t.Helper()
	next, _ := m.Update(k)
	return next.(Model)
}

func TestQuickTasksAreNotOnTheBoard(t *testing.T) {
	m := quickModel(t)
	for i := range m.columns() {
		for _, task := range m.tasksForColumn(i) {
			if task.Quick {
				t.Errorf("quick task #%d shows in column %d", task.Number, i)
			}
		}
	}
	if got := len(m.paletteCards()); got != 0 {
		t.Errorf("palette lists %d cards for an empty search", got)
	}
}

func TestQuickKeyShowsThePanelAndEscReturnsToTheBoard(t *testing.T) {
	m := quickModel(t)
	if m.quickWidth() != 0 {
		t.Fatal("the panel shows before it is asked for")
	}
	m = pressBoard(t, m, keyPress(quickKey))
	if m.mode != modeQuick || m.quickWidth() == 0 {
		t.Fatalf("mode = %v, quick width = %d after the quick key", m.mode, m.quickWidth())
	}
	if cur := m.current(); cur == nil || cur.ID != "q1" {
		t.Errorf("current = %v, want the first quick task", cur)
	}
	m = pressBoard(t, m, keyPress("esc"))
	if m.mode != modeBoard || m.quickWidth() == 0 {
		t.Errorf("esc: mode = %v, quick width = %d; want the board with the panel still showing", m.mode, m.quickWidth())
	}
	m = pressBoard(t, m, keyPress(quickKey))
	m = pressBoard(t, m, keyPress(quickKey))
	if m.mode != modeBoard || m.quickWidth() != 0 {
		t.Errorf("second quick key: mode = %v, quick width = %d; want the panel hidden", m.mode, m.quickWidth())
	}
}

func TestQuickPanelSelectionMoves(t *testing.T) {
	m := quickModel(t)
	m = pressBoard(t, m, keyPress(quickKey))
	m = pressBoard(t, m, keyPress("j"))
	if m.current().ID != "q2" {
		t.Errorf("j selected %s", m.current().ID)
	}
	m = pressBoard(t, m, keyPress("j"))
	if m.current().ID != "q2" {
		t.Errorf("j past the end selected %s", m.current().ID)
	}
	m = pressBoard(t, m, keyPress("k"))
	if m.current().ID != "q1" {
		t.Errorf("k selected %s", m.current().ID)
	}
}

func TestQuickPanelSharesTheBoardWidthWithoutOverflowing(t *testing.T) {
	m := quickModel(t)
	m = pressBoard(t, m, keyPress(quickKey))
	l := m.layout()
	if l.quickW != quickPanelWidth || l.columnsW+l.quickW != l.boardW {
		t.Fatalf("columnsW=%d quickW=%d boardW=%d", l.columnsW, l.quickW, l.boardW)
	}
	for _, c := range l.cols {
		if c.x+c.width > l.columnsW {
			t.Errorf("column %d ends at %d, past the columns' %d", c.index, c.x+c.width, l.columnsW)
		}
	}
	view := m.View()
	for i, line := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(line); w > m.width {
			t.Fatalf("line %d is %d wide in a %d wide terminal", i, w, m.width)
		}
	}
	for _, want := range []string{"quick tasks 2", "#10 try the parser", "#11 explain the diff"} {
		if !strings.Contains(ansi.Strip(view), want) {
			t.Errorf("the view lacks %q", want)
		}
	}
}

func TestQuickPanelHidesWhenTheBoardWouldGetTooNarrow(t *testing.T) {
	m := quickModel(t)
	m.width = minColumnWidth + quickPanelWidth - 1
	m.prefs.ShowQuick = true
	if m.quickWidth() != 0 || m.columnsWidth() != m.boardWidth() {
		t.Errorf("quick width = %d, columns width = %d, board width = %d", m.quickWidth(), m.columnsWidth(), m.boardWidth())
	}
}

func TestQuickPanelRowsAreClickable(t *testing.T) {
	m := quickModel(t)
	m = pressBoard(t, m, keyPress(quickKey))
	l := m.layout()
	x := l.columnsW + 2
	for row, want := range []string{"q1", "q2"} {
		h := l.hitTest(m, x, l.bodyY+1+row*quickRowHeight)
		if h.kind != hitQuick || h.task == nil || h.task.ID != want {
			t.Errorf("row %d: hit = %+v, want quick task %s", row, h, want)
		}
	}
	if h := l.hitTest(m, x, l.bodyY); h.kind != hitQuick || h.task != nil {
		t.Errorf("the panel's top border hit %+v, want the panel without a task", h)
	}
}

func TestQuickTasksStayOutOfTheBoardKeys(t *testing.T) {
	m := quickModel(t)
	m = pressBoard(t, m, keyPress(quickKey))
	// A key the panel doesn't know must not reach the board and act on a column's card.
	before := m.col
	m = pressBoard(t, m, keyPress("l"))
	if m.col != before || m.mode != modeQuick {
		t.Errorf("l moved the column to %d or left the panel: mode %v", m.col, m.mode)
	}
}
