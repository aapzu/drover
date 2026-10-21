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
	if got := m.archivedIn("c1"); got != 2 {
		t.Errorf("archivedIn = %d, want 2", got)
	}
	if out := m.renderColumn(colBox{index: 1, width: 40, slots: 5}, 20, false); !strings.Contains(out, " 1 +2") {
		t.Errorf("header doesn't show the hidden count:\n%s", out)
	}

	m.prefs.ShowArchived = true
	if got := m.tasksIn("c1"); len(got) != 3 {
		t.Errorf("column lists %d cards with archived shown, want 3", len(got))
	}
	if got := m.archivedIn("c1"); got != 0 {
		t.Errorf("archivedIn = %d while archived cards are shown, want 0", got)
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
