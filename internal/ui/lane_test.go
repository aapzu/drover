package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

func ptr[T any](v T) *T { return &v }

// laneModel has two main columns, c0 and c1, and a review lane of c2 and c3.
// c2 holds one card.
func laneModel(t *testing.T) Model {
	m := testModel(120, 40, 4, 0)
	m.eng = &engine.Engine{Root: t.TempDir()}
	m.snap.Board.Columns[2].Lane = "review"
	m.snap.Board.Columns[3].Lane = "review"
	m.snap.Tasks = []*store.Task{
		{ID: "main", Number: 1, Title: "main", ColumnID: "c0"},
		{ID: "rev", Number: 2, Title: "rev", ColumnID: "c2"},
	}
	return m
}

func TestReviewLaneShowsWhileItHoldsCards(t *testing.T) {
	m := laneModel(t)
	if !m.reviewLaneShown() || len(m.lanes()) != 2 {
		t.Fatalf("lane with a card: shown=%v lanes=%v", m.reviewLaneShown(), m.lanes())
	}
	m.snap.Tasks = m.snap.Tasks[:1]
	if m.reviewLaneShown() || len(m.lanes()) != 1 {
		t.Errorf("empty lane: shown=%v lanes=%v", m.reviewLaneShown(), m.lanes())
	}
	m.prefs.ReviewLane = ptr(true)
	if !m.reviewLaneShown() {
		t.Error("an empty lane is hidden even though R showed it")
	}
}

func TestReviewLaneNeedsColumns(t *testing.T) {
	m := testModel(120, 40, 2, 1)
	m.prefs.ReviewLane = ptr(true)
	if m.reviewLaneShown() || len(m.lanes()) != 1 {
		t.Errorf("a board without lane columns draws a lane: %v", m.lanes())
	}
	m, _ = update(m, key("R"))
	if !strings.Contains(m.flash, "No review lane") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestReviewLaneSitsBelowTheMainColumns(t *testing.T) {
	m := laneModel(t)
	l := m.layout()
	if len(l.cols) != 4 {
		t.Fatalf("%d columns laid out, want 4", len(l.cols))
	}
	var mainH, laneH int
	for _, c := range l.cols {
		switch {
		case c.index < 2 && c.y != 0, c.index >= 2 && c.y != mainH+laneDividerHeight:
			t.Errorf("column %d at y=%d", c.index, c.y)
		case c.index < 2:
			mainH = c.height
		default:
			laneH = c.height
		}
	}
	if mainH+laneDividerHeight+laneH != l.boardH || laneH == 0 || mainH <= laneH {
		t.Errorf("heights %d + %d, board %d", mainH, laneH, l.boardH)
	}
	if c := l.cols[2]; c.x != 0 {
		t.Errorf("the lane starts at x=%d, want 0", c.x)
	}

	board := ansi.Strip(m.renderBoard(l))
	if lines := strings.Count(board, "\n") + 1; lines != l.boardH {
		t.Errorf("board is %d lines, want %d", lines, l.boardH)
	}

	h := l.hitTest(m, 1, l.bodyY+l.cols[2].y+columnHeaderHeight)
	if h.kind != hitCard || h.task == nil || h.task.ID != "rev" {
		t.Errorf("clicking the lane's card hit %+v", h)
	}
	if h := l.hitTest(m, 1, l.bodyY+columnHeaderHeight); h.kind != hitCard || h.task.ID != "main" {
		t.Errorf("clicking the main card hit %+v", h)
	}
}

func TestReviewLaneToggleMovesTheSelectionOut(t *testing.T) {
	m := laneModel(t)
	m.col = 2
	m, _ = update(m, key("R"))
	if m.prefs.ReviewLane == nil || *m.prefs.ReviewLane || m.col != 1 {
		t.Fatalf("after R: shown=%v col=%d, want hidden and column 1", m.reviewLaneShown(), m.col)
	}
	if header := ansi.Strip(m.renderHeader()); !strings.Contains(header, "1 in review lane (R)") {
		t.Errorf("header doesn't count the hidden cards: %q", header)
	}
	if len(m.layout().cols) != 2 {
		t.Errorf("hidden lane still laid out: %d columns", len(m.layout().cols))
	}
	m, _ = update(m, key("R"))
	if m.prefs.ReviewLane == nil || !*m.prefs.ReviewLane || len(m.layout().cols) != 4 {
		t.Errorf("after R again: shown=%v, %d columns", m.reviewLaneShown(), len(m.layout().cols))
	}
}

func TestNavigationWalksIntoTheLaneAndSkipsItWhenHidden(t *testing.T) {
	m := laneModel(t)
	m.col = 1
	m, _ = update(m, key("l"))
	if m.col != 2 {
		t.Errorf("l from the last main column went to %d, want the lane's first", m.col)
	}
	m, _ = update(m, key("h"))
	if m.col != 1 {
		t.Errorf("h from the lane's first column went to %d, want 1", m.col)
	}

	m.prefs.ReviewLane = ptr(false)
	m, _ = update(m, key("l"))
	if m.col != 1 {
		t.Errorf("l into a hidden lane went to %d", m.col)
	}
}

func TestSnapshotMovesTheSelectionOutOfAHidingLane(t *testing.T) {
	m := laneModel(t)
	m.col = 2
	snap := m.snap
	snap.Tasks = snap.Tasks[:1]
	m.applySnap(snap)
	if m.col != 1 {
		t.Errorf("selection stayed in a lane that went away: column %d", m.col)
	}
}

func TestSelectionNeverLandsInAHiddenLane(t *testing.T) {
	m := laneModel(t)
	m.prefs.ReviewLane = ptr(false)
	m.selectTask(m.task("rev"))
	if m.col != 1 || m.selected["c2"] != "rev" {
		t.Errorf("following a card into a hidden lane: col=%d selected=%v", m.col, m.selected)
	}
}

func TestMovingCardsStaysWithinItsLane(t *testing.T) {
	m := laneModel(t)
	m.snap.Tasks = append(m.snap.Tasks, &store.Task{ID: "last", Number: 3, Title: "last", ColumnID: "c1"})

	m.col = 1
	m, cmd := update(m, key("L"))
	if cmd != nil || m.col != 1 {
		t.Errorf("L on the last main column's card crossed into the lane: col=%d", m.col)
	}
	m.col = 2
	m, cmd = update(m, key("L"))
	if cmd == nil || m.col != 3 {
		t.Errorf("L inside the lane: col=%d, want 3 and a move", m.col)
	}
}

func TestMovingIntoAHiddenLaneKeepsTheSelectionVisible(t *testing.T) {
	m := laneModel(t)
	m.prefs.ReviewLane = ptr(false)
	m.modalTask = "main"
	m.mode = modeMove

	m, cmd := update(m, key("3"))
	if cmd == nil || m.col != 1 || m.selected["c2"] != "main" {
		t.Errorf("move into the hidden lane: cmd=%v col=%d selected=%v", cmd != nil, m.col, m.selected)
	}

	m.snap.Tasks[0].ColumnID = "c2"
	m.col = 0
	m.selectTask(m.task("main"))
	if m.col != 1 {
		t.Errorf("a snapshot that moved the card into the hidden lane left col=%d", m.col)
	}
}

func TestReviewLaneRowsDoNotOverlapInHitTesting(t *testing.T) {
	m := laneModel(t)
	l := m.layout()
	mainH := l.cols[0].height
	for _, tc := range []struct {
		name string
		y    int
		col  int
	}{
		{"last main row", l.bodyY + mainH - 1, 0},
		{"lane header", l.bodyY + mainH + laneDividerHeight, 2},
	} {
		if h := l.hitTest(m, 1, tc.y); h.col != tc.col {
			t.Errorf("%s: hit column %d, want %d", tc.name, h.col, tc.col)
		}
	}

	edge := l.cols[2].x + l.cols[2].width - 1
	if h := l.hitTest(m, edge, l.bodyY+l.cols[2].y); h.kind != hitColumnEdge || h.col != 2 {
		t.Errorf("lane column edge hit %+v", h)
	}
	if h := l.hitTest(m, edge, l.bodyY); h.kind != hitColumnEdge || h.col != 0 {
		t.Errorf("main column edge hit %+v", h)
	}
}

func TestLaneHeights(t *testing.T) {
	if got := laneHeights(30, 1, 6, 10); len(got) != 1 || got[0] != 30 {
		t.Errorf("one lane: %v", got)
	}
	for total := 0; total <= 60; total++ {
		got := laneHeights(total, 2, 6, 0)
		if got[0] < 0 || got[1] < 0 || got[0]+got[1] != total || got[1] > max(total/2, total*2/5) {
			t.Errorf("laneHeights(%d) = %v", total, got)
		}
	}
}

func TestLaneHeightsHonorTheSetHeightWithinBounds(t *testing.T) {
	lo, hi := laneBounds(40)
	for _, tt := range []struct{ pref, want int }{{15, 15}, {1, lo}, {100, hi}, {-5, laneHeights(40, 2, 6, 0)[1]}} {
		got := laneHeights(40, 2, 6, tt.pref)
		if got[1] != tt.want || got[0]+got[1] != 40 {
			t.Errorf("pref %d: %v, want a lane of %d", tt.pref, got, tt.want)
		}
	}
	for total := 0; total <= 60; total++ {
		got := laneHeights(total, 2, 6, 25)
		if got[0] < 0 || got[1] < 0 || got[0]+got[1] != total {
			t.Errorf("laneHeights(%d, pref 25) = %v", total, got)
		}
	}
}

func TestReviewLaneDividerSitsAboveTheLaneAndIsTheDragHandle(t *testing.T) {
	m := laneModel(t)
	m.prefs.ReviewLaneHeight = 15
	l := m.layout()
	if l.laneH != 15 || l.laneY+l.laneH != l.boardH {
		t.Fatalf("lane at %d, %d rows, board %d", l.laneY, l.laneH, l.boardH)
	}
	if c := l.cols[2]; c.y != l.laneY+laneDividerHeight || c.height != 15-laneDividerHeight {
		t.Errorf("lane column at y=%d, %d rows", c.y, c.height)
	}
	if c := l.cols[0]; c.height != l.laneY {
		t.Errorf("main column is %d rows, want %d", c.height, l.laneY)
	}
	if h := l.hitTest(m, 5, l.bodyY+l.laneY); h.kind != hitLaneEdge {
		t.Errorf("divider hit %+v", h)
	}
	if h := l.hitTest(m, 5, l.bodyY+l.laneY-1); h.kind == hitLaneEdge || h.col != 0 {
		t.Errorf("row above the divider hit %+v", h)
	}
	if h := l.hitTest(m, 5, l.bodyY+l.laneY+1); h.kind == hitLaneEdge || h.col != 2 {
		t.Errorf("lane header hit %+v", h)
	}
	lines := strings.Split(ansi.Strip(m.renderBoard(l)), "\n")
	if len(lines) != l.boardH {
		t.Errorf("board is %d lines, want %d", len(lines), l.boardH)
	} else if row := lines[l.laneY]; !strings.Contains(row, "review lane") || ansi.StringWidth(row) != l.boardW {
		t.Errorf("row %d is %q, want the divider across %d columns", l.laneY, row, l.boardW)
	}

	m, _ = update(m, key("R"))
	if l := m.layout(); l.laneH != 0 || l.hitTest(m, 5, l.bodyY+l.boardH-1).kind == hitLaneEdge {
		t.Errorf("hidden lane still has a divider: %+v", l)
	}
}

func TestBoardFillsItsHeightWithTheLaneAtAnySize(t *testing.T) {
	for _, d := range []density{densityCompact, densityNormal, densityTall} {
		for height := 10; height <= 50; height += 5 {
			m := laneModel(t)
			m.height = height
			m.prefs.Density = d
			for _, pref := range []int{0, 1, 12, 1000} {
				m.prefs.ReviewLaneHeight = pref
				l := m.layout()
				if lines := strings.Count(ansi.Strip(m.renderBoard(l)), "\n") + 1; lines != l.boardH {
					t.Errorf("height %d density %d lane %d: board is %d lines, want %d", height, d, pref, lines, l.boardH)
				}
			}
		}
	}
}

func TestReviewLaneAutoShowsOnFirstR(t *testing.T) {
	m := laneModel(t)
	m.snap.Tasks = m.snap.Tasks[:1]
	m, _ = update(m, key("R"))
	if m.prefs.ReviewLane == nil || !*m.prefs.ReviewLane || len(m.lanes()) != 2 {
		t.Errorf("R on an empty lane in auto mode: pref=%v lanes=%v", m.prefs.ReviewLane, m.lanes())
	}
	if got := loadPrefs(m.eng.Root); got.ReviewLane == nil || !*got.ReviewLane {
		t.Errorf("R didn't save the choice: %+v", got.ReviewLane)
	}
}

func TestReviewLaneIgnoresArchivedAndHiddenCards(t *testing.T) {
	m := laneModel(t)
	m.task("rev").Hidden = true
	if m.reviewLaneShown() {
		t.Error("a lane holding only a hidden card is shown")
	}
	m.prefs.ShowHidden = true
	if !m.reviewLaneShown() {
		t.Error("B didn't bring the lane back")
	}
}
