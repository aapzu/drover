package ui

import (
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

func TestArchivedCardsAreHiddenUntilShown(t *testing.T) {
	m := archiveModel(t)

	if got := m.tasksIn("c1"); len(got) != 1 {
		t.Errorf("column lists %d cards, want the 1 unarchived", len(got))
	}
	if got := m.offBoardIn("c1"); got != 2 {
		t.Errorf("offBoardIn = %d, want 2", got)
	}
	if out := m.renderColumn(colBox{index: 1, width: 40, slots: 5}, 20, false); !strings.Contains(out, " 1 +2") {
		t.Errorf("header doesn't show the hidden count:\n%s", out)
	}

	m.prefs.ShowArchived = true
	if got := m.tasksIn("c1"); len(got) != 3 {
		t.Errorf("column lists %d cards with archived shown, want 3", len(got))
	}
	if got := m.offBoardIn("c1"); got != 0 {
		t.Errorf("offBoardIn = %d while archived cards are shown, want 0", got)
	}
	if out := m.renderColumn(colBox{index: 1, width: 40, slots: 5}, 20, false); strings.Contains(out, "+2") {
		t.Errorf("header still shows a hidden count:\n%s", out)
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

func TestHiddenCardsAreHiddenUntilShown(t *testing.T) {
	m := archiveModel(t)
	m.snap.Tasks[0].Hidden = true

	col := m.columns()[0].ID
	if got := m.tasksIn(col); len(got) != 2 {
		t.Errorf("column lists %d cards, want the 2 visible", len(got))
	}
	if got := m.offBoardIn(col); got != 1 {
		t.Errorf("offBoardIn = %d, want 1", got)
	}
	if out := m.renderColumn(colBox{index: 0, width: 40, slots: 5}, 20, false); !strings.Contains(out, " 2 +1") {
		t.Errorf("header doesn't show the hidden count:\n%s", out)
	}

	next, _ := m.updateBoard(key("B"))
	m = next.(Model)
	if got := m.tasksIn(col); len(got) != 3 {
		t.Errorf("column lists %d cards with hidden shown, want 3", len(got))
	}
	if got := m.offBoardIn(col); got != 0 {
		t.Errorf("offBoardIn = %d while hidden cards are shown, want 0", got)
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
