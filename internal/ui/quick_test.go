package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
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
	before := m.col
	m = pressBoard(t, m, keyPress("l"))
	if m.col != before || m.mode != modeQuick {
		t.Errorf("l moved the column to %d or left the panel: mode %v", m.col, m.mode)
	}
}

func TestTabFromTheQuickPanelTypesIntoTheQuickTask(t *testing.T) {
	m := quickModel(t)
	m.snap.Live = map[string]string{"q1": "tmux-q1"}
	m.selected["c0"] = "c0-t0"
	m = pressBoard(t, m, keyPress(quickKey))
	m = pressBoard(t, m, keyPress("tab"))
	if m.mode != modeLive || m.current() == nil || m.current().ID != "q1" {
		t.Fatalf("mode = %v, current = %v; want live mode on the quick task", m.mode, m.current())
	}
	m = pressBoard(t, m, keyPress("esc"))
	if m.mode != modeQuick {
		t.Errorf("leaving live mode went to mode %v, want the quick panel", m.mode)
	}
}

func TestQuickPanelKeysActOnTheQuickTaskNotTheBoardCard(t *testing.T) {
	m := quickModel(t)
	m.selected["c0"] = "c0-t0"
	m.promptInput, m.titleInput, m.renameInput = textarea.New(), textinput.New(), textinput.New()
	m = pressBoard(t, m, keyPress(quickKey))

	delete := pressBoard(t, m, keyPress("x"))
	if delete.mode != modeConfirm || !strings.Contains(delete.confirmText, "quick task #10") {
		t.Errorf("x: mode %v, confirm %q; want a confirmation for quick task #10", delete.mode, delete.confirmText)
	}
	edit := pressBoard(t, m, keyPress("e"))
	if edit.mode != modeForm || edit.editingID != "q1" {
		t.Errorf("e: mode %v, editing %q; want the form for q1", edit.mode, edit.editingID)
	}
	rename := pressBoard(t, m, keyPress("E"))
	if rename.mode != modeRename || rename.modalTask != "q1" {
		t.Errorf("E: mode %v, task %q; want the rename prompt for q1", rename.mode, rename.modalTask)
	}
}

func TestQuickPanelSelectionJumpsToTheEnds(t *testing.T) {
	m := quickModel(t)
	m = pressBoard(t, m, keyPress(quickKey))
	m = pressBoard(t, m, keyPress("G"))
	if m.current().ID != "q2" {
		t.Errorf("G selected %s", m.current().ID)
	}
	m = pressBoard(t, m, keyPress("g"))
	if m.current().ID != "q1" {
		t.Errorf("g selected %s", m.current().ID)
	}
}

func TestQuickPanelOpensNothingOnATerminalTooNarrowForIt(t *testing.T) {
	m := quickModel(t)
	m.width = minColumnWidth + quickPanelWidth - 1
	m = pressBoard(t, m, keyPress(quickKey))
	if m.mode != modeBoard || m.flash == "" {
		t.Errorf("mode = %v, flash = %q; want the board and a notice", m.mode, m.flash)
	}
	if loadPrefs(m.eng.Root).ShowQuick {
		t.Error("the refused panel was saved as showing")
	}
}

func TestShrinkingTheTerminalLeavesTheQuickPanelWhenItDisappears(t *testing.T) {
	m := quickModel(t)
	m.promptInput, m.columnPrompt = textarea.New(), textarea.New()
	m = pressBoard(t, m, keyPress(quickKey))
	next, _ := m.Update(tea.WindowSizeMsg{Width: minColumnWidth + quickPanelWidth - 1, Height: 40})
	if got := next.(Model).mode; got != modeBoard {
		t.Errorf("mode = %v after the panel vanished, want the board", got)
	}
}

func TestStartedQuickTaskIsSelectedAndAnErrorClearsTheGuard(t *testing.T) {
	m := quickModel(t)
	m.startingQuick = true
	next, _ := m.Update(sessionStartedMsg{err: errors.New("boom"), snap: m.snap, quick: true})
	failed := next.(Model)
	if failed.startingQuick || failed.mode != modeBoard || !strings.Contains(failed.flash, "boom") {
		t.Errorf("after an error: starting %v, mode %v, flash %q", failed.startingQuick, failed.mode, failed.flash)
	}

	m.startingQuick = true
	next, _ = m.Update(sessionStartedMsg{task: m.snap.Tasks[len(m.snap.Tasks)-1], snap: m.snap, quick: true})
	started := next.(Model)
	if started.startingQuick || started.quickSel != "q2" || started.mode != modeQuick {
		t.Errorf("after a start: starting %v, selected %q, mode %v", started.startingQuick, started.quickSel, started.mode)
	}
}

func TestStartingAQuickTaskTwiceIsRefused(t *testing.T) {
	m := quickModel(t)
	m = pressBoard(t, m, keyPress(quickKey))
	m.startingQuick = true
	next, cmd := m.updateQuick(keyPress("n"))
	if cmd != nil || !strings.Contains(next.(Model).flash, "Already starting") {
		t.Errorf("a second n started another quick task or didn't say why: cmd %v, flash %q", cmd != nil, next.(Model).flash)
	}
}

func TestClickingAQuickRowSelectsItAndDoubleClickAttaches(t *testing.T) {
	m := quickModel(t)
	m = pressBoard(t, m, keyPress(quickKey))
	l := m.layout()
	click := tea.MouseMsg{X: l.columnsW + 2, Y: l.bodyY + 1 + quickRowHeight, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	next, _ := m.Update(click)
	m = next.(Model)
	if m.mode != modeQuick || m.quickSel != "q2" {
		t.Fatalf("click: mode %v, selected %q; want the second quick task", m.mode, m.quickSel)
	}
	next, cmd := m.Update(click)
	if cmd == nil || next.(Model).busy["q2"].label != "starting agent" {
		t.Errorf("double click: busy %+v, cmd %v; want q2's agent started to attach to it", next.(Model).busy, cmd != nil)
	}
}

func TestQuickPanelScrollsToKeepTheSelectionVisible(t *testing.T) {
	m := testModel(140, 14, 3, 0)
	m.eng = &engine.Engine{Root: t.TempDir()}
	for i := range 8 {
		m.snap.Tasks = append(m.snap.Tasks, &store.Task{ID: fmt.Sprintf("q%d", i), Number: i + 1, Title: fmt.Sprintf("task %d", i), Quick: true})
	}
	m = pressBoard(t, m, keyPress(quickKey))
	m = pressBoard(t, m, keyPress("G"))
	l := m.layout()
	if !strings.Contains(ansi.Strip(m.renderQuick(l.quickW, l.boardH)), "#8 task 7") {
		t.Error("the last quick task is not visible after G")
	}
	rows := quickRows(l.boardH)
	first := m.quickStart(rows)
	if h := l.hitTest(m, l.columnsW+2, l.bodyY+1); h.task == nil || h.task.Number != first+1 {
		t.Errorf("the first visible row hit %+v, want task %d", h.task, first+1)
	}
	if h := l.hitTest(m, l.columnsW+2, l.bodyY+l.boardH-1); h.task != nil {
		t.Errorf("the bottom border hit task %d", h.task.Number)
	}
}

func TestQuickPanelKeepsTheBoardColumnScrolled(t *testing.T) {
	m := testModel(140, 14, 3, 20)
	m.eng = &engine.Engine{Root: t.TempDir()}
	m.snap.Tasks = append(m.snap.Tasks, &store.Task{ID: "q1", Number: 100, Title: "quick", Quick: true})
	tasks := m.tasksForColumn(0)
	m.selected["c0"] = tasks[len(tasks)-1].ID
	before := m.layout().cols[0].first
	if before == 0 {
		t.Fatal("the fixture's column doesn't scroll")
	}
	m = pressBoard(t, m, keyPress(quickKey))
	if got := m.layout().cols[0].first; got != before {
		t.Errorf("the column shows from card %d with the quick panel focused, was %d", got, before)
	}
}
