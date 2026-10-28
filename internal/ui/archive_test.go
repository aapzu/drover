package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/engine"
)

func archiveModel(t *testing.T) Model {
	t.Helper()
	m := testModel(120, 40, 2, 3)
	m.snap.Board.Columns[1].Role = "done"
	m.snap.Tasks[3].Archived = true
	m.snap.Tasks[4].Archived = true
	m.eng = &engine.Engine{Root: t.TempDir()}
	return m
}
func TestOffBoardCardsAreHiddenUntilShown(t *testing.T) {
	tests := []struct {
		name    string
		col     int
		setup   func(m *Model)
		reveal  func(m Model) Model
		visible int
		off     int
		header  string
	}{
		{"archived", 1, func(*Model) {}, func(m Model) Model {
			m.prefs.ShowArchived = true
			return m
		}, 1, 2, " 1 +2"},
		{"hidden", 0, func(m *Model) { m.snap.Tasks[0].Hidden = true }, func(m Model) Model {
			next, _ := m.updateBoard(key("B"))
			return next.(Model)
		}, 2, 1, " 2 +1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := archiveModel(t)
			tt.setup(&m)
			col := m.columns()[tt.col].ID
			box := colBox{index: tt.col, width: 40, slots: 5}

			if got := m.tasksIn(col); len(got) != tt.visible {
				t.Errorf("column lists %d cards, want the %d on the board", len(got), tt.visible)
			}
			if got := m.offBoardIn(col); got != tt.off {
				t.Errorf("offBoardIn = %d, want %d", got, tt.off)
			}
			if out := m.renderColumn(box, 20, false); !strings.Contains(out, tt.header) {
				t.Errorf("header doesn't show the hidden count:\n%s", out)
			}

			m = tt.reveal(m)
			if got := m.tasksIn(col); len(got) != 3 {
				t.Errorf("column lists %d cards with them shown, want 3", len(got))
			}
			if got := m.offBoardIn(col); got != 0 {
				t.Errorf("offBoardIn = %d while they are shown, want 0", got)
			}
			if out := m.renderColumn(box, 20, false); strings.Contains(out, fmt.Sprintf("+%d", tt.off)) {
				t.Errorf("header still shows a hidden count:\n%s", out)
			}
		})
	}
}

func TestArchivedCardsStayVisibleOutsideDone(t *testing.T) {
	m := archiveModel(t)
	m.snap.Board.Columns[1].Role = ""
	if got := m.tasksIn("c1"); len(got) != 3 {
		t.Errorf("column lists %d cards, want all 3 once it isn't Done", len(got))
	}
}

func TestArchiveKeys(t *testing.T) {
	m := archiveModel(t)

	next, _ := m.updateBoard(key("C"))
	if got := next.(Model).flash; !strings.Contains(got, "Only the Done column") {
		t.Errorf("C outside Done: flash = %q", got)
	}

	m.col = 1
	next, _ = m.updateBoard(key("C"))
	got := next.(Model)
	if got.mode != modeConfirm || !strings.Contains(got.confirmText, "Archive the card in") {
		t.Errorf("C with one card: mode=%v text=%q", got.mode, got.confirmText)
	}

	m.snap.Tasks[3].Archived, m.snap.Tasks[4].Archived = false, false
	next, _ = m.updateBoard(key("C"))
	if got := next.(Model).confirmText; !strings.Contains(got, "all 3 cards") {
		t.Errorf("C with three cards: text = %q", got)
	}

	for _, task := range m.snap.Tasks[3:] {
		task.Archived = true
	}
	next, _ = m.updateBoard(key("C"))
	if got := next.(Model); got.mode == modeConfirm || !strings.Contains(got.flash, "Nothing to archive") {
		t.Errorf("C with nothing to archive: mode=%v flash=%q", got.mode, got.flash)
	}

	next, _ = m.updateBoard(key("v"))
	if !next.(Model).prefs.ShowArchived {
		t.Error("v didn't show archived cards")
	}
}
func TestOffBoardCombinations(t *testing.T) {
	for _, c := range []struct {
		archived, hidden, showArchived, showHidden, want bool
	}{
		{archived: true, want: true},
		{archived: true, showArchived: true},
		{hidden: true, want: true},
		{hidden: true, showHidden: true},
		{hidden: true, showArchived: true, want: true},
		{archived: true, hidden: true, showArchived: true, want: true},
		{archived: true, hidden: true, showHidden: true, want: true},
		{archived: true, hidden: true, showArchived: true, showHidden: true},
	} {
		m := archiveModel(t)
		m.prefs.ShowHidden = c.showHidden
		task := m.snap.Tasks[0]
		task.Archived, task.Hidden = c.archived, c.hidden
		if got := m.offBoard(task, !c.showArchived); got != c.want {
			t.Errorf("%+v: offBoard = %v", c, got)
		}
	}
}

func TestArchiveAllIgnoresHiddenCards(t *testing.T) {
	m := archiveModel(t)
	m.col = 1
	m.prefs.ShowHidden = true
	m.snap.Tasks[3].Archived, m.snap.Tasks[4].Archived = false, false
	m.snap.Tasks[4].Hidden = true

	next, _ := m.updateBoard(key("C"))
	if got := next.(Model).confirmText; !strings.Contains(got, "all 2 cards") {
		t.Errorf("C with one hidden card: text = %q", got)
	}

	m.snap.Tasks[3].Hidden = true
	m.snap.Tasks[5].Hidden = true
	next, _ = m.updateBoard(key("C"))
	if got := next.(Model); got.mode == modeConfirm || !strings.Contains(got.flash, "Nothing to archive") {
		t.Errorf("C with only hidden cards: mode=%v flash=%q", got.mode, got.flash)
	}
}

func TestHideKeys(t *testing.T) {
	m := archiveModel(t)

	if _, cmd := m.updateBoard(key("b")); cmd == nil {
		t.Error("b on a card didn't start hiding it")
	}

	next, _ := m.updateBoard(key("B"))
	if !next.(Model).prefs.ShowHidden {
		t.Error("B didn't show hidden cards")
	}
	next, _ = next.(Model).updateBoard(key("B"))
	if next.(Model).prefs.ShowHidden {
		t.Error("second B didn't hide them again")
	}

	m.snap.Tasks = nil
	if _, cmd := m.updateBoard(key("b")); cmd != nil {
		t.Error("b with no card selected did something")
	}
}
