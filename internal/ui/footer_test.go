package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/store"
)

func TestBoardFooterShowsSelectedCard(t *testing.T) {
	m := testModel(100, 30, 1, 1)
	m.mode = modeBoard
	task := m.snap.Tasks[0]
	task.Branch = "aapeli/dx-1-foo"
	task.Sessions = map[string]*store.Session{store.MainSession: {
		Status: store.StatusRunning, Model: "opus", Effort: "high", PermissionMode: "acceptEdits",
	}}

	got := ansi.Strip(m.renderFooter())
	for _, want := range []string{"#1", "opus", "high", "acceptEdits", "aapeli/dx-1-foo", "ctrl+k commands · ? help"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer misses %q: %q", want, got)
		}
	}

	task.Sessions[store.MainSession].ModeInherited = true
	if got := ansi.Strip(m.renderFooter()); strings.Contains(got, "acceptEdits") {
		t.Errorf("footer shows an inherited permission mode: %q", got)
	}
}

func TestBoardFooterWithoutCardKeepsHint(t *testing.T) {
	m := testModel(100, 30, 1, 0)
	m.mode = modeBoard
	if got := ansi.Strip(m.renderFooter()); !strings.Contains(got, "ctrl+k commands · ? help") {
		t.Errorf("footer = %q", got)
	}
}

func TestBoardFooterStaysOneRowAndKeepsHintWhenNarrow(t *testing.T) {
	m := testModel(40, 30, 1, 1)
	m.mode = modeBoard
	m.snap.Tasks[0].Branch = "aapeli/dx-1234-a-very-long-branch-name-indeed"
	footer := m.renderFooter()
	if w := lipgloss.Width(footer); w > m.width {
		t.Errorf("footer is %d wide, terminal is %d", w, m.width)
	}
	plain := ansi.Strip(footer)
	for _, want := range []string{"#1", "…", "ctrl+k commands · ? help"} {
		if !strings.Contains(plain, want) {
			t.Errorf("footer misses %q: %q", want, plain)
		}
	}
}
