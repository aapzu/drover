package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	next, _ := m.updatePalette(special(k))
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
		key    string
		mode   mode
		cmd    bool
		quits  bool
	}{
		{"filter", "/", modeFilter, true, false},
		{"help", "?", modeHelp, false, false},
		{"column…", "m", modeMove, false, false},
		{"delete task", "x", modeConfirm, false, false},
		{"next column", "L", modeBoard, true, false},
		{"quit", "q", modeBoard, true, true},
		{"zzz", "", modePalette, false, false},
	}
	for _, tt := range tests {
		m := typeInPalette(openedPalette(t), tt.search)
		if tt.key != "" {
			m = pickByKey(t, m, tt.key)
		}
		next, cmd := m.updatePalette(tea.KeyMsg{Type: tea.KeyEnter})
		if got := next.(Model).mode; got != tt.mode {
			t.Errorf("%q: mode = %v, want %v", tt.search, got, tt.mode)
		}
		if (cmd != nil) != tt.cmd {
			t.Errorf("%q: got a cmd = %v, want %v", tt.search, cmd != nil, tt.cmd)
		}
		if tt.quits {
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("Quit returned %T, want tea.QuitMsg", cmd())
			}
		}
	}
}

func pickByKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	for range len(m.paletteMatches()) {
		if m.paletteMatches()[m.paletteSel].key == key {
			return m
		}
		m = pressInPalette(m, tea.KeyDown)
	}
	t.Fatalf("no command on %q among the matches", key)
	return m
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

func TestEveryPaletteCommandIsListedInTheHelp(t *testing.T) {
	m := paletteModel()
	m.width = 60
	listed := map[string]bool{}
	for _, line := range strings.Split(ansi.Strip(m.renderHelp()), "\n") {
		cell, _, _ := strings.Cut(strings.TrimLeft(line, "│ "), "  ")
		for _, alt := range strings.Split(cell, " / ") {
			for _, k := range strings.FieldsFunc(alt, func(r rune) bool { return r == ',' || r == ' ' }) {
				listed[k] = true
			}
		}
	}
	for _, c := range paletteCommands {
		if c.key == "?" {
			continue
		}
		if !listed[c.key] {
			t.Errorf("the palette runs %q on %q but the help doesn't list that key", c.name, c.key)
		}
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
