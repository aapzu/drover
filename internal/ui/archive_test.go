package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

type fakeArchiver struct {
	fakeEngine
	err   error
	calls []archiveCall
}

type archiveCall struct{ archive, force bool }

func (f *fakeArchiver) ArchiveTask(id string, archive, force bool) error {
	f.calls = append(f.calls, archiveCall{archive, force})
	if f.err != nil && !force {
		return f.err
	}
	return nil
}

// archived runs the command and feeds its result back to the model.
func archived(t *testing.T, m Model, cmd tea.Cmd) (Model, actionDoneMsg) {
	t.Helper()
	done, ok := cmd().(actionDoneMsg)
	if !ok {
		t.Fatalf("archive returned %T", cmd())
	}
	m, _ = update(m, done)
	return m, done
}

func TestArchiveKeyArchivesAndRestoresADoneCard(t *testing.T) {
	m := realModel(t, "a")
	card := m.snap.Tasks[0]
	if err := m.eng.Move(card.ID, "done", engine.MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	m.snap = m.eng.Snapshot()
	m.col = 4

	m, cmd := update(m, key("A"))
	m, done := archived(t, m, cmd)
	if done.err != nil || m.flash != "Archived #1. v shows archived cards." || len(m.tasksForColumn(4)) != 0 {
		t.Fatalf("archive: err %v flash %q cards left %d", done.err, m.flash, len(m.tasksForColumn(4)))
	}

	m, _ = update(m, key("v"))
	m, cmd = update(m, key("A"))
	m, done = archived(t, m, cmd)
	if done.err != nil || m.flash != "Restored #1" {
		t.Fatalf("restore: err %v flash %q", done.err, m.flash)
	}
	m, _ = update(m, key("v"))
	if got := m.tasksForColumn(4); len(got) != 1 || got[0].Archived {
		t.Errorf("the restored card is not back on the board: %v", titles(got))
	}
}

func TestArchivingAWorktreeWithUncommittedChangesAsksBeforeDeletingIt(t *testing.T) {
	press := func(t *testing.T, k string) (calls []archiveCall, cardBusy bool) {
		t.Helper()
		m := testModel(120, 40, 1, 1)
		eng := &fakeArchiver{err: engine.ErrDirty}
		cmd := archiveCard(eng, m.busy, "c0-t0", 1, true, false)
		m, _ = archived(t, m, cmd)
		if m.mode != modeConfirm || !strings.Contains(m.confirmText, "uncommitted changes") {
			t.Fatalf("mode %v text %q, want the confirmation", m.mode, m.confirmText)
		}
		m, cmd = update(m, key(k))
		if cmd != nil {
			m, _ = archived(t, m, cmd)
		}
		return eng.calls, m.mode != modeBoard
	}

	calls, stillOpen := press(t, "y")
	if want := []archiveCall{{true, false}, {true, true}}; !slices.Equal(calls, want) || stillOpen {
		t.Errorf("y: engine calls %v, want a forced retry %v", calls, want)
	}
	calls, _ = press(t, "n")
	if want := []archiveCall{{true, false}}; !slices.Equal(calls, want) {
		t.Errorf("n: engine calls %v, want only the first, unforced one", calls)
	}
}

func TestArchiveAllInDoneAsksThenArchivesEveryCard(t *testing.T) {
	m := realModel(t, "a", "b")
	for _, c := range m.snap.Tasks {
		if err := m.eng.Move(c.ID, "done", engine.MoveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	m.snap = m.eng.Snapshot()
	m.col = 4

	m, _ = update(m, key("C"))
	if m.mode != modeConfirm || !strings.Contains(m.confirmText, "all 2 cards") {
		t.Fatalf("mode %v text %q", m.mode, m.confirmText)
	}
	m, cmd := update(m, key("y"))
	snap, ok := cmd().(snapMsg)
	if !ok {
		t.Fatalf("y returned %T, want the new snapshot", cmd())
	}
	m, _ = update(m, snap)
	if got := m.tasksForColumn(4); len(got) != 0 {
		t.Errorf("%d cards still on the board after archiving the column", len(got))
	}
}
