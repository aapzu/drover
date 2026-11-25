package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/gh"
	"github.com/aapzu/drover/internal/store"
)

// importedReview is the board with one imported review card, selected.
func importedReview(t *testing.T) (Model, *store.Task) {
	t.Helper()
	m := importOn(t)
	m.eng.ImportReviews([]engine.ReviewPR{{Found: gh.Found{Number: 7, Title: "Theirs", URL: "https://github.com/o/r/pull/7"}}})
	m.snap = m.eng.Snapshot()
	m.mode = modeBoard
	card := m.snap.Tasks[0]
	m.selectTask(card)
	return m, card
}

func boardPress(t *testing.T, m Model, k string) (Model, bool) {
	t.Helper()
	next, cmd := m.updateBoard(key(k))
	return next.(Model), cmd != nil
}

func TestStartingAnImportedReviewAsksForTheLevel(t *testing.T) {
	for _, k := range []string{"enter", "r"} {
		m, card := importedReview(t)
		m, started := boardPress(t, m, k)
		if m.mode != modeReviewLevel || started || m.level.task != card.ID {
			t.Fatalf("%s: mode %v, started %v", k, m.mode, started)
		}
		if got := ansi.Strip(m.View()); !strings.Contains(got, "Review #1 at which /code-review level?") || !strings.Contains(got, "▸ 3 high") {
			t.Errorf("%s: picker not shown with high preselected:\n%s", k, got)
		}

		next, cmd := m.updateReviewLevel(key("4"))
		m = next.(Model)
		if m.mode != modeBoard || cmd == nil {
			t.Fatalf("%s: picking didn't start the card: mode %v", k, m.mode)
		}
		if got := m.snap.Tasks[0].Prompt; got != "/code-review xhigh https://github.com/o/r/pull/7" {
			t.Errorf("%s: prompt = %q", k, got)
		}
	}
}

func TestEscCancelsTheReviewLevelPicker(t *testing.T) {
	m, card := importedReview(t)
	m, _ = boardPress(t, m, "enter")
	next, cmd := m.updateReviewLevel(key("esc"))
	m = next.(Model)
	if m.mode != modeBoard || cmd != nil || m.eng.Snapshot().Tasks[0].Prompt != card.Prompt {
		t.Errorf("esc: mode %v, cmd nil %v, prompt %q", m.mode, cmd == nil, m.eng.Snapshot().Tasks[0].Prompt)
	}
}

func TestMovingAnImportedReviewIntoAnAgentColumnAsksForTheLevel(t *testing.T) {
	m, _ := importedReview(t)
	m, moved := boardPress(t, m, "L")
	if m.mode != modeReviewLevel || moved {
		t.Fatalf("moving into Reviewing: mode %v, moved %v", m.mode, moved)
	}
	next, cmd := m.updateReviewLevel(key("enter"))
	if next.(Model).snap.Tasks[0].Prompt != "/code-review high https://github.com/o/r/pull/7" || cmd == nil {
		t.Errorf("enter picked %q", next.(Model).snap.Tasks[0].Prompt)
	}

	m, card := importedReview(t)
	if cmd := m.moveCmd(card, m.eng.Board().ReviewImport(true)); m.mode != modeBoard || cmd == nil {
		t.Errorf("a move into a column without an agent asked: mode %v", m.mode)
	}
}

func TestALeveledReviewStartsWithoutAsking(t *testing.T) {
	m, card := importedReview(t)
	m.eng.EditTask(card.ID, card.Title, "/code-review low https://github.com/o/r/pull/7")
	m.snap = m.eng.Snapshot()
	m.selectTask(m.snap.Tasks[0])
	if m, started := boardPress(t, m, "r"); m.mode != modeBoard || !started {
		t.Errorf("mode %v, started %v", m.mode, started)
	}
}
