package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

// collapseModel has a session of three cards (owner in c0, the others in c1),
// a second session of two cards in c1, and one plain card in c1.
func collapseModel(t *testing.T) Model {
	m := testModel(120, 40, 2, 0)
	m.eng = &engine.Engine{Root: t.TempDir()}
	m.snap.Board.Columns[1].Role = "done"
	card := func(id string, num int, col, group string) *store.Task {
		return &store.Task{ID: id, Number: num, Title: id, ColumnID: col, GroupID: group,
			PR: &store.PR{Number: 100 + num, State: "MERGED"}}
	}
	m.snap.Tasks = []*store.Task{
		card("a", 1, "c0", ""), card("a2", 2, "c1", "a"), card("a3", 3, "c1", "a"),
		card("b", 4, "c1", ""), card("b2", 5, "c1", "b"),
		card("solo", 6, "c1", ""),
	}
	return m
}

func ids(tasks []*store.Task) string {
	var out []string
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return strings.Join(out, " ")
}

func TestCollapseGroupsShowsOneCardPerSession(t *testing.T) {
	m := collapseModel(t)
	if got := ids(m.tasksForColumn(1)); got != "a2 a3 b b2 solo" {
		t.Fatalf("expanded column = %q", got)
	}

	m.prefs.CollapseGroups = true
	if got := ids(m.tasksForColumn(1)); got != "a2 b solo" {
		t.Errorf("collapsed column = %q, want %q", got, "a2 b solo")
	}
	if got := ids(m.tasksForColumn(0)); got != "a" {
		t.Errorf("a session's card in another column stays: %q", got)
	}

	card := ansi.Strip(m.renderCard(m.task("a2"), 60, false, false, 1))
	if !strings.Contains(card, "⧉ 2/3 +1") {
		t.Errorf("kept card doesn't show what it stands for:\n%s", card)
	}
	if card := ansi.Strip(m.renderCard(m.task("solo"), 60, false, false, 0)); strings.Contains(card, "+") {
		t.Errorf("an ungrouped card shows a folded count:\n%s", card)
	}
	col := ansi.Strip(m.renderColumn(colBox{index: 1, width: 40, slots: 5}, 30, false))
	if !strings.Contains(col, " 3 +2") {
		t.Errorf("header doesn't count the folded cards:\n%s", col)
	}
}

func TestCollapseGroupsAppliesAfterTheFilter(t *testing.T) {
	m := collapseModel(t)
	m.prefs.CollapseGroups = true
	m.filter = "#103"
	if got := ids(m.tasksForColumn(1)); got != "a3" {
		t.Errorf("filtering for a folded card shows %q, want a3", got)
	}
}

func TestCollapseKeepsTheSelectedCardShown(t *testing.T) {
	m := collapseModel(t)
	m.col = 1
	m.selected["c1"] = "a3"

	m, _ = update(m, key("f"))
	if !m.prefs.CollapseGroups {
		t.Fatal("f didn't collapse")
	}
	if got := ids(m.tasksForColumn(1)); got != "a3 b solo" {
		t.Errorf("collapsed column = %q, want the selected a3 to stand for its session", got)
	}
	if got := m.current(); got == nil || got.ID != "a3" {
		t.Errorf("cursor on %v after collapsing, want a3", got)
	}

	m, _ = update(m, key("f"))
	if got := m.current(); got == nil || got.ID != "a3" || len(m.tasksForColumn(1)) != 5 {
		t.Errorf("cursor on %v with %d cards after expanding", got, len(m.tasksForColumn(1)))
	}
}

func TestMovedCardStaysSelectedWhenItsSiblingIsShown(t *testing.T) {
	m := collapseModel(t)
	m.prefs.CollapseGroups = true
	m.snap.Tasks[0].ColumnID = "c1"
	m.snap.Tasks[1].ColumnID = "c0"
	m.snap.Tasks[2].ColumnID = "c0"
	m.busy["a3"] = op{column: "c1"}
	m.selectTask(m.task("a3"))
	if m.col != 1 {
		t.Fatalf("column %d, want the one a3 moved to", m.col)
	}
	if got := m.current(); got == nil || got.ID != "a3" {
		t.Errorf("cursor on %v, want the moved card", got)
	}
}

func TestArchivingDoneCountsFoldedCards(t *testing.T) {
	m := collapseModel(t)
	m.prefs.CollapseGroups = true
	m.col = 1
	m, _ = update(m, key("C"))
	if m.mode != modeConfirm || !strings.Contains(m.confirmText, "all 5 cards") {
		t.Errorf("mode %v, confirm %q: want the real card count", m.mode, m.confirmText)
	}
}

func TestArchivingDoneIgnoresTheFilter(t *testing.T) {
	m := collapseModel(t)
	m.prefs.CollapseGroups = true
	m.filter = "#106"
	m.col = 1
	m, _ = update(m, key("C"))
	if !strings.Contains(m.confirmText, "all 5 cards") {
		t.Errorf("confirm %q: want every card ArchiveColumn will archive", m.confirmText)
	}
}
