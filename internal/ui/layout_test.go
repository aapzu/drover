package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

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

func TestPreviewWindow(t *testing.T) {
	text := "1\n2\n3\n4\n5\n6\n"
	cases := []struct {
		scroll int
		want   string
	}{
		{0, "4\n5\n6"},
		{2, "2\n3\n4"},
		{3, "1\n2\n3"},
		{50, "1\n2\n3"},
	}
	for _, c := range cases {
		if got := previewWindow(text, 3, c.scroll); got != c.want {
			t.Errorf("scroll %d: got %q want %q", c.scroll, got, c.want)
		}
	}
}

func TestPreviewScrollStaysAnchored(t *testing.T) {
	m := testModel(120, 40, 1, 1)
	id := m.snap.Tasks[0].ID
	next, _ := m.Update(previewMsg{taskID: id, history: 100, at: time.Now()})
	m = next.(Model)
	m.previewScroll = 10
	next, _ = m.Update(previewMsg{taskID: id, history: 104, at: time.Now()})
	if got := next.(Model).previewScroll; got != 14 {
		t.Fatalf("scroll is %d after 4 new lines, want 14", got)
	}
	next, _ = next.(Model).Update(previewMsg{taskID: "other", history: 104, at: time.Now()})
	if got := next.(Model).previewScroll; got != 0 {
		t.Fatalf("scroll is %d after switching task, want 0", got)
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

func TestGroupCardsShareSessionAndShowMarker(t *testing.T) {
	m := testModel(120, 40, 2, 0)
	m.snap.Board.Columns[0].AutoSpawn = true
	owner := &store.Task{ID: "owner", Number: 1, Title: "Refactor", ColumnID: "c1", ActiveCard: "split",
		Branch: "refactor-1", PR: &store.PR{Number: 10},
		Sessions: map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityIdle}}}
	split := &store.Task{ID: "split", Number: 2, Title: "PR two", ColumnID: "c0", GroupID: "owner",
		PR: &store.PR{Number: 11, Branch: "two", State: "MERGED"}}
	m.snap.Tasks = []*store.Task{owner, split}

	if m.sessionFor(split) != owner.Sessions[store.MainSession] {
		t.Fatal("split card doesn't resolve its group's session")
	}
	splitCard := ansi.Strip(m.renderCard(split, 60, false, false))
	for _, want := range []string{"⧉ 2/2", "● idle", "PR #11 merged two"} {
		if !strings.Contains(splitCard, want) {
			t.Errorf("split card misses %q:\n%s", want, splitCard)
		}
	}
	// The owner sits in a plain column while the agent works for the split
	// card, so it doesn't show the agent's activity.
	ownerCard := ansi.Strip(m.renderCard(owner, 60, false, false))
	if !strings.Contains(ownerCard, "⧉ 1/2") || strings.Contains(ownerCard, "idle") || !strings.Contains(ownerCard, "PR #10") {
		t.Errorf("owner card:\n%s", ownerCard)
	}
	for _, d := range []density{densityNormal, densityCompact, densityTall} {
		m.prefs.Density = d
		if got := lipgloss.Height(m.renderCard(split, 30, false, false)); got != d.cardHeight() {
			t.Fatalf("density %d: card is %d rows, want %d", d, got, d.cardHeight())
		}
	}
}
