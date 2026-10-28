package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/engine"
)

func wheelDown(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
}

func TestWheelOverBoard(t *testing.T) {
	tests := []struct {
		name   string
		live   bool
		button tea.MouseButton
		col    int
		card   string
	}{
		{"moves the selection", false, tea.MouseButtonWheelDown, 0, "c0-t1"},
		{"keeps the live card selected", true, tea.MouseButtonWheelDown, 1, "c1-t0"},
		{"left keeps the selection", false, tea.MouseButtonWheelLeft, 1, "c1-t0"},
		{"right keeps the selection", false, tea.MouseButtonWheelRight, 1, "c1-t0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(120, 40, 2, 3)
			m.col = 1
			if tt.live {
				m.snap.Live = map[string]string{"c1-t0": "sess"}
				m.mode = modeLive
			}
			y := m.layout().bodyY + columnHeaderHeight + 1

			next, _ := m.Update(tea.MouseMsg{X: 1, Y: y, Action: tea.MouseActionPress, Button: tt.button})
			got := next.(Model)
			if got.col != tt.col || got.current().ID != tt.card || got.mode != m.mode {
				t.Fatalf("mode %v col %d card %s", got.mode, got.col, got.current().ID)
			}
		})
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

func sentJob(m Model) (keyJob, bool) {
	select {
	case job := <-m.keys.jobs:
		return job, true
	default:
		return keyJob{}, false
	}
}

func TestWheelGoesToAgentThatTracksMouse(t *testing.T) {
	setup := func() Model {
		m := testModel(120, 40, 1, 1)
		id := m.snap.Tasks[0].ID
		m.snap.Live = map[string]string{id: "sess"}
		m.keys = &keySender{jobs: make(chan keyJob, 4)}
		m.previewFor, m.previewMouse = id, true
		return m
	}
	l := setup().layout()
	cases := []struct {
		name   string
		button tea.MouseButton
		x, y   int
		want   string
	}{
		{"up", tea.MouseButtonWheelUp, 10, l.previewY + 3, "\x1b[<64;10;3M"},
		{"down", tea.MouseButtonWheelDown, 10, l.previewY + 3, "\x1b[<65;10;3M"},
		{"left border", tea.MouseButtonWheelUp, 0, l.previewY + 3, "\x1b[<64;1;3M"},
		{"right border", tea.MouseButtonWheelUp, 119, l.previewY + 3, "\x1b[<64;118;3M"},
		{"top border", tea.MouseButtonWheelUp, 10, l.previewY, "\x1b[<64;10;1M"},
		{"bottom border", tea.MouseButtonWheelUp, 10, l.previewY + l.previewH - 1, fmt.Sprintf("\x1b[<64;10;%dM", l.previewH-2)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := setup()
			m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: c.button, X: c.x, Y: c.y})
			job, ok := sentJob(m)
			if !ok || job.session != "sess" || job.literal != c.want {
				t.Fatalf("sent %+v (sent=%v), want %q to sess", job, ok, c.want)
			}
		})
	}

	scrolls := map[string]func(*Model){
		"pane has scrollback": func(m *Model) { m.previewHistory = 50 },
		"agent ignores mouse": func(m *Model) { m.previewMouse = false },
		"stale selection":     func(m *Model) { m.previewFor = "other" },
	}
	for name, mutate := range scrolls {
		t.Run(name, func(t *testing.T) {
			m := setup()
			mutate(&m)
			m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: 10, Y: l.previewY + 3})
			if job, ok := sentJob(m); ok {
				t.Fatalf("sent %+v, want the preview to scroll instead", job)
			}
		})
	}
}

// mouseModel is three columns of two cards with a saved-layout directory.
func mouseModel(t *testing.T) Model {
	t.Helper()
	m := testModel(120, 40, 3, 2)
	m.eng = &engine.Engine{Root: t.TempDir()}
	return m
}

func mouse(action tea.MouseAction, button tea.MouseButton, x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: action, Button: button}
}

func mousePress(x, y int) tea.MouseMsg { return mouse(tea.MouseActionPress, tea.MouseButtonLeft, x, y) }
func mouseMotion(x, y int) tea.MouseMsg {
	return mouse(tea.MouseActionMotion, tea.MouseButtonNone, x, y)
}
func mouseRelease(x, y int) tea.MouseMsg {
	return mouse(tea.MouseActionRelease, tea.MouseButtonLeft, x, y)
}

// firstCardY is a row inside the first card of every column.
func firstCardY(m Model) int { return m.layout().bodyY + columnHeaderHeight + 1 }

func TestDraggingACardMovesItToTheColumnItIsDroppedOn(t *testing.T) {
	m := mouseModel(t)
	y := firstCardY(m)

	m, _ = update(m, mousePress(5, y))
	if m.drag.kind != dragPending || m.drag.task != "c0-t0" || m.selected["c0"] != "c0-t0" {
		t.Fatalf("press: drag %+v selected %v", m.drag, m.selected)
	}
	m, _ = update(m, mouseMotion(6, y))
	if m.drag.kind != dragPending {
		t.Fatalf("a one cell wobble started a drag: %v", m.drag.kind)
	}
	m, _ = update(m, mouseMotion(45, y))
	if m.drag.kind != dragCard || m.dropTarget(m.layout()) != 1 {
		t.Fatalf("motion: drag %v drop target %d", m.drag.kind, m.dropTarget(m.layout()))
	}
	if view := m.View(); !strings.Contains(view, "drop to move here") {
		t.Errorf("the target column doesn't invite a drop:\n%s", view)
	}

	m, cmd := update(m, mouseRelease(45, y))
	if cmd == nil || m.drag.kind != dragNone || m.col != 1 || m.selected["c1"] != "c0-t0" {
		t.Fatalf("release: cmd nil %v drag %v col %d selected %v", cmd == nil, m.drag.kind, m.col, m.selected)
	}
	if got := m.busy["c0-t0"]; got.label != "moving" || got.column != "c1" {
		t.Errorf("busy = %+v, want the card moving to c1", got)
	}
}

func TestDraggingStartsAfterAVerticalWobble(t *testing.T) {
	m := mouseModel(t)
	y := firstCardY(m)
	m, _ = update(m, mousePress(5, y))
	m, _ = update(m, mouseMotion(5, y+1))
	if m.drag.kind != dragCard {
		t.Errorf("drag = %v after moving a row", m.drag.kind)
	}
}

func TestReleasingACardWhereNothingTakesItMovesNothing(t *testing.T) {
	tests := []struct {
		name string
		x, y func(l layout) int
		move bool
	}{
		{"the same column", func(l layout) int { return 20 }, func(l layout) int { return l.bodyY + 5 }, false},
		{"the same column's edge", func(l layout) int { return 39 }, func(l layout) int { return l.bodyY + 5 }, false},
		{"the header row", func(l layout) int { return 45 }, func(l layout) int { return 0 }, false},
		{"the preview", func(l layout) int { return 45 }, func(l layout) int { return l.previewY + 3 }, false},
		{"another column's header", func(l layout) int { return 45 }, func(l layout) int { return l.bodyY }, true},
		{"another column's empty space", func(l layout) int { return 85 }, func(l layout) int { return l.bodyY + l.boardH - 1 }, true},
		{"another column's edge", func(l layout) int { return 78 }, func(l layout) int { return l.bodyY + 5 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mouseModel(t)
			l := m.layout()
			m, _ = update(m, mousePress(5, firstCardY(m)))
			m, _ = update(m, mouseMotion(tt.x(l), tt.y(l)))
			if got := m.dropTarget(l) >= 0; got != tt.move {
				t.Errorf("drop target %d, moves = %v", m.dropTarget(l), tt.move)
			}
			m, cmd := update(m, mouseRelease(tt.x(l), tt.y(l)))
			if (cmd != nil) != tt.move || (len(m.busy) != 0) != tt.move || m.drag.kind != dragNone {
				t.Errorf("cmd nil %v busy %v drag %v", cmd == nil, m.busy, m.drag.kind)
			}
		})
	}
}

func TestClickingACardSelectsItWithoutMovingIt(t *testing.T) {
	m := mouseModel(t)
	y := firstCardY(m) + 6
	m, _ = update(m, mousePress(5, y))
	m, cmd := update(m, mouseRelease(5, y))
	if cmd != nil || len(m.busy) != 0 || m.drag.kind != dragNone || m.selected["c0"] != "c0-t1" {
		t.Errorf("cmd nil %v busy %v drag %v selected %v", cmd == nil, m.busy, m.drag.kind, m.selected)
	}
	if m.drag.clickFor != "c0-t1" {
		t.Errorf("the click was forgotten: %+v", m.drag)
	}
}

func TestClickingAColumnHeaderSelectsTheColumn(t *testing.T) {
	m := mouseModel(t)
	m.mode = modeFilter
	m, _ = update(m, mousePress(45, m.layout().bodyY))
	if m.col != 1 || m.mode != modeBoard {
		t.Errorf("col %d mode %v", m.col, m.mode)
	}
}

func TestDoubleClickAttaches(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *Model)
		busy  bool
	}{
		{"two clicks in a row", func(*Model) {}, true},
		{"too slow", func(m *Model) { m.drag.clickAt = time.Now().Add(-time.Second) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mouseModel(t)
			y := firstCardY(m)
			m, _ = update(m, mousePress(5, y))
			m, _ = update(m, mouseRelease(5, y))
			tt.setup(&m)
			m, cmd := update(m, mousePress(5, y))
			got := m.busy["c0-t0"].label
			if (got == "starting agent") != tt.busy || (cmd != nil) != tt.busy {
				t.Errorf("busy %q cmd nil %v", got, cmd == nil)
			}
			if tt.busy && m.drag.kind != dragNone {
				t.Errorf("drag = %v after attaching", m.drag.kind)
			}
		})
	}

	m := mouseModel(t)
	y := firstCardY(m)
	m, _ = update(m, mousePress(5, y))
	m, _ = update(m, mouseRelease(5, y))
	m, _ = update(m, mousePress(45, y))
	if len(m.busy) != 0 || m.drag.kind != dragPending {
		t.Errorf("a click on another card attached: busy %v drag %v", m.busy, m.drag.kind)
	}
}

func TestDragFromTheColumnEdgeResizesTheColumnAndSavesIt(t *testing.T) {
	m := mouseModel(t)
	y := firstCardY(m)
	m, _ = update(m, mousePress(39, y))
	if m.drag.kind != dragColumn || m.drag.col != 0 {
		t.Fatalf("drag = %+v", m.drag)
	}
	m, _ = update(m, mouseMotion(60, y))
	if got := m.prefs.ColumnWidths["c0"]; got != 62 {
		t.Errorf("width = %d, want the edge following the pointer", got)
	}
	m, _ = update(m, mouseMotion(2, y))
	if got := m.prefs.ColumnWidths["c0"]; got != minResizedWidth {
		t.Errorf("width = %d, want clamped to %d", got, minResizedWidth)
	}
	m, _ = update(m, mouseMotion(60, y))
	if loadPrefs(m.eng.Root).ColumnWidths["c0"] != 0 {
		t.Error("the layout was saved before the drag ended")
	}
	m, _ = update(m, mouseRelease(60, y))
	if m.drag.kind != dragNone || loadPrefs(m.eng.Root).ColumnWidths["c0"] != 62 {
		t.Errorf("drag %v saved widths %v", m.drag.kind, loadPrefs(m.eng.Root).ColumnWidths)
	}
}

func TestDragFromThePreviewBorderResizesThePreviewAndSavesIt(t *testing.T) {
	m := mouseModel(t)
	l := m.layout()
	m, _ = update(m, mousePress(10, l.previewY))
	if m.drag.kind != dragPreview {
		t.Fatalf("drag = %v", m.drag.kind)
	}

	for _, tt := range []struct{ y, want int }{{20, 19}, {2, l.bodyH - minBoardHeight}, {38, minPreviewHeight}} {
		m, _ = update(m, mouseMotion(10, tt.y))
		if got := m.layout().previewH; got != tt.want {
			t.Errorf("pointer at row %d: preview is %d rows, want %d", tt.y, got, tt.want)
		}
	}
	m, _ = update(m, mouseMotion(10, 20))
	if loadPrefs(m.eng.Root).PreviewHeight != 0 {
		t.Error("the layout was saved before the drag ended")
	}
	m, _ = update(m, mouseRelease(10, 20))
	if got := loadPrefs(m.eng.Root).PreviewHeight; got != 19 || m.drag.kind != dragNone {
		t.Errorf("saved preview height %d, drag %v", got, m.drag.kind)
	}
}

func TestClickingThePreviewTypesIntoTheAgentOnlyWhenOneRuns(t *testing.T) {
	m := mouseModel(t)
	m, _ = update(m, mousePress(10, m.layout().previewY+3))
	if m.mode != modeBoard || !strings.Contains(m.flash, "No running agent to type into") {
		t.Errorf("mode %v flash %q", m.mode, m.flash)
	}
}

func TestDropTarget(t *testing.T) {
	m := mouseModel(t)
	l := m.layout()
	y := firstCardY(m)
	tests := []struct {
		name string
		drag dragState
		want int
	}{
		{"not dragging", dragState{kind: dragPending, x: 45, y: y}, -1},
		{"over another column", dragState{kind: dragCard, fromCol: 0, x: 45, y: y}, 1},
		{"over a column's empty space", dragState{kind: dragCard, fromCol: 0, x: 85, y: l.bodyY + 20}, 2},
		{"over the source column", dragState{kind: dragCard, fromCol: 0, x: 5, y: y}, -1},
		{"over the header row", dragState{kind: dragCard, fromCol: 0, x: 45, y: 0}, -1},
		{"over the preview", dragState{kind: dragCard, fromCol: 0, x: 45, y: l.previewY + 2}, -1},
	}
	for _, tt := range tests {
		m.drag = tt.drag
		if got := m.dropTarget(l); got != tt.want {
			t.Errorf("%s: drop target %d, want %d", tt.name, got, tt.want)
		}
	}
}
