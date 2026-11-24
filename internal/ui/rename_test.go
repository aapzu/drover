package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRenameOpensWithTheCurrentTitleAndSavesIt(t *testing.T) {
	m := realModel(t, "old")
	id := m.current().ID
	m.eng.EditTask(id, "old", "the prompt")
	m.snap = m.eng.Snapshot()
	m, _ = update(m, key("E"))
	if m.mode != modeRename || m.renameInput.Value() != "old" {
		t.Fatalf("mode %v input %q", m.mode, m.renameInput.Value())
	}

	m, _ = update(m, titleSuggestedMsg{id: id, original: "old", title: "Suggested"})
	if got := m.renameInput.Value(); got != "Suggested" {
		t.Fatalf("input = %q, want the suggestion", got)
	}

	m.renameInput.SetValue("  Mine ")
	m, _ = update(m, special(tea.KeyEnter))
	got := m.current()
	if m.mode != modeBoard || got.Title != "Mine" || got.Prompt != "the prompt" {
		t.Fatalf("mode %v title %q prompt %q", m.mode, got.Title, got.Prompt)
	}
}

func TestRenameKeepsWhatYouTypedOverASuggestion(t *testing.T) {
	m := realModel(t, "old")
	id := m.current().ID
	m, _ = update(m, key("E"))
	m, _ = update(m, key("er"))

	m, _ = update(m, titleSuggestedMsg{id: id, original: "old", title: "Suggested"})
	if got := m.renameInput.Value(); got != "older" {
		t.Errorf("input = %q, want what you typed", got)
	}

	m, _ = update(m, special(tea.KeyEsc))
	m, _ = update(m, titleSuggestedMsg{id: id, original: "older", title: "Late"})
	if m.mode != modeBoard || m.current().Title != "old" {
		t.Errorf("mode %v title %q, want the board and the old title", m.mode, m.current().Title)
	}
}

func TestRenameNeedsATitle(t *testing.T) {
	m := realModel(t, "old")
	m, _ = update(m, key("E"))
	m.renameInput.SetValue(" ")
	m, _ = update(m, special(tea.KeyEnter))
	if m.mode != modeRename || m.flash != "A task needs a title" || m.current().Title != "old" {
		t.Fatalf("mode %v flash %q title %q", m.mode, m.flash, m.current().Title)
	}
}

func TestPaletteRenameOpensThePrompt(t *testing.T) {
	m := realModel(t, "old")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlK})
	m = typeInPalette(m, "rename")
	m, _ = update(m, special(tea.KeyEnter))
	if m.mode != modeRename {
		t.Fatalf("mode = %v, want the rename prompt", m.mode)
	}
}
