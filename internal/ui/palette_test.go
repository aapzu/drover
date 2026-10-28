package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func paletteModel() Model {
	m := testModel(120, 40, 3, 1)
	m.paletteInput = newPaletteInput()
	m.paletteInput.Focus()
	m.filterInput = textinput.New()
	return m
}

func openedPalette(t *testing.T) Model {
	t.Helper()
	next, _ := paletteModel().updateBoard(tea.KeyMsg{Type: tea.KeyCtrlK})
	m := next.(Model)
	if m.mode != modePalette {
		t.Fatalf("ctrl+k: mode = %v, want the palette", m.mode)
	}
	return m
}

func typeInPalette(m Model, s string) Model {
	next, _ := m.updatePalette(key(s))
	return next.(Model)
}

func pressInPalette(m Model, k tea.KeyType) Model {
	next, _ := m.updatePalette(tea.KeyMsg{Type: k})
	return next.(Model)
}

func TestPaletteMatchesEveryWordInNameOrKey(t *testing.T) {
	m := paletteModel()
	if got := len(m.paletteMatches()); got != len(paletteCommands) {
		t.Fatalf("empty search matched %d of %d commands", got, len(paletteCommands))
	}
	m = typeInPalette(m, "ARCHIVE restore")
	got := m.paletteMatches()
	if len(got) != 1 || got[0].key != "A" {
		t.Errorf("archive restore matched %v, want only the archive command", got)
	}
	m.paletteInput.SetValue("zzz")
	if got := m.paletteMatches(); len(got) != 0 {
		t.Errorf("nonsense matched %v", got)
	}
	m.paletteInput.SetValue("enter")
	if got := m.paletteMatches(); len(got) != 1 || got[0].key != "enter" {
		t.Errorf("searching a key name matched %v, want the attach command", got)
	}
}

func TestPaletteRunsTheChosenCommand(t *testing.T) {
	tests := []struct {
		search string
		pick   int
		mode   mode
		cmd    bool
	}{
		{"filter", 0, modeFilter, true},
		{"help", 0, modeHelp, false},
		{"column…", 0, modeMove, false},
		{"delete task", 0, modeConfirm, false},
		{"next column", 0, modeBoard, true},
		{"quit", 1, modeBoard, true},
		{"zzz", 0, modePalette, false},
	}
	for _, tt := range tests {
		m := typeInPalette(openedPalette(t), tt.search)
		for range tt.pick {
			m = pressInPalette(m, tea.KeyDown)
		}
		next, cmd := m.updatePalette(tea.KeyMsg{Type: tea.KeyEnter})
		if got := next.(Model).mode; got != tt.mode {
			t.Errorf("%q: mode = %v, want %v", tt.search, got, tt.mode)
		}
		if (cmd != nil) != tt.cmd {
			t.Errorf("%q: got a cmd = %v, want %v", tt.search, cmd != nil, tt.cmd)
		}
	}

	m := typeInPalette(openedPalette(t), "quit")
	m = pressInPalette(m, tea.KeyDown)
	_, cmd := m.updatePalette(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("Quit returned %T, want tea.QuitMsg", cmd())
	}
}

func TestPaletteIsReachedThroughUpdate(t *testing.T) {
	next, _ := paletteModel().Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	next, _ = next.Update(key("quit"))
	m := next.(Model)
	if m.mode != modePalette || len(m.paletteMatches()) != 2 {
		t.Errorf("mode %v with %d matches, want the palette narrowed to the two quit commands", m.mode, len(m.paletteMatches()))
	}
}

func TestPaletteArrowsMoveWithinMatches(t *testing.T) {
	m := openedPalette(t)
	if m = pressInPalette(m, tea.KeyUp); m.paletteSel != 0 {
		t.Fatalf("sel = %d after up on the first row, want 0", m.paletteSel)
	}
	for range 3 {
		m = pressInPalette(m, tea.KeyDown)
	}
	if m.paletteSel != 3 {
		t.Fatalf("sel = %d after three downs, want 3", m.paletteSel)
	}
	if m = pressInPalette(m, tea.KeyCtrlP); m.paletteSel != 2 {
		t.Errorf("sel = %d after ctrl+p, want 2", m.paletteSel)
	}
	if m = pressInPalette(m, tea.KeyCtrlN); m.paletteSel != 3 {
		t.Errorf("sel = %d after ctrl+n, want 3", m.paletteSel)
	}
	m = typeInPalette(m, "quit")
	if m.paletteSel != 0 {
		t.Errorf("sel = %d after typing, want reset to the first match", m.paletteSel)
	}
	for range 5 {
		m = pressInPalette(m, tea.KeyDown)
	}
	if m.paletteSel != len(m.paletteMatches())-1 {
		t.Errorf("sel = %d, want clamped to the last match", m.paletteSel)
	}
	m = typeInPalette(m, "zzz")
	if m = pressInPalette(m, tea.KeyDown); m.paletteSel != 0 {
		t.Errorf("sel = %d with no matches, want 0", m.paletteSel)
	}
}

func TestPaletteClosesAndReopensEmpty(t *testing.T) {
	for _, k := range []tea.KeyType{tea.KeyEsc, tea.KeyCtrlK} {
		m := typeInPalette(openedPalette(t), "quit")
		m = pressInPalette(m, tea.KeyDown)
		if m = pressInPalette(m, k); m.mode != modeBoard {
			t.Fatalf("%v: mode = %v, want the palette closed", k, m.mode)
		}
		next, _ := m.updateBoard(tea.KeyMsg{Type: tea.KeyCtrlK})
		m = next.(Model)
		if m.paletteInput.Value() != "" || m.paletteSel != 0 {
			t.Errorf("%v: reopened with %q at %d, want a blank search", k, m.paletteInput.Value(), m.paletteSel)
		}
	}
}
func TestPaletteKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range paletteCommands {
		if seen[c.key] {
			t.Errorf("two commands press %q", c.key)
		}
		seen[c.key] = true
	}
}

func TestPaletteRendering(t *testing.T) {
	first, last := paletteCommands[0].name, paletteCommands[len(paletteCommands)-1].name
	for _, h := range []int{9, 20, 40} {
		m := openedPalette(t)
		out := m.renderPalette(h)
		if lines := strings.Count(out, "\n") + 1; lines > h {
			t.Errorf("height %d: palette is %d lines tall", h, lines)
		}
		if !strings.Contains(out, "▸ "+first) || strings.Contains(out, last) {
			t.Errorf("height %d: want the first command selected and the last scrolled out:\n%s", h, out)
		}
		m.paletteSel = len(paletteCommands) - 1
		if out = m.renderPalette(h); !strings.Contains(out, "▸ "+last) || strings.Contains(out, first) {
			t.Errorf("height %d: want the last command selected and the first scrolled out:\n%s", h, out)
		}
	}

	m := typeInPalette(openedPalette(t), "zzz")
	if out := m.renderPalette(40); !strings.Contains(out, "No matching commands") || strings.Contains(out, " of ") {
		t.Errorf("no match should say so without a position:\n%s", out)
	}
	m = typeInPalette(openedPalette(t), "quit")
	if out := m.renderPalette(40); strings.Contains(out, " of ") {
		t.Errorf("a list that fits needs no position:\n%s", out)
	}
	if out := openedPalette(t).renderPalette(9); !strings.Contains(out, fmt.Sprintf("1 of %d", len(paletteCommands))) {
		t.Errorf("a scrolling list shows the position:\n%s", out)
	}

	m = openedPalette(t)
	m.width = 30
	if w := lipgloss.Width(m.renderPalette(40)); w > m.width {
		t.Errorf("palette is %d wide on a %d wide screen", w, m.width)
	}
}
