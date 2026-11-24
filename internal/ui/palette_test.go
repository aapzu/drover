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
	if out := m.renderPalette(40); !strings.Contains(out, "No matches") || strings.Contains(out, " of ") {
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

func searchModel(t *testing.T, titles ...string) Model {
	t.Helper()
	m, _ := update(realModel(t, titles...), tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.mode != modePalette {
		t.Fatalf("ctrl+k: mode = %v, want the palette", m.mode)
	}
	return m
}

func TestPaletteListsMatchingCardsBeforeCommands(t *testing.T) {
	m := searchModel(t, "fix login", "add fonts", "other")
	if got := m.paletteCards(); len(got) != 0 {
		t.Fatalf("empty search listed %d cards, want commands only", len(got))
	}
	m = typeInPalette(m, "fo")
	cards := m.paletteCards()
	if len(cards) != 1 || cards[0].Title != "add fonts" {
		t.Fatalf("fo matched %v, want only the fonts card", cards)
	}
	if out := m.renderPalette(40); !strings.Contains(out, "▸ #2 add fonts") {
		t.Errorf("the card should be the selected first row:\n%s", out)
	}
}

func TestPaletteSearchSkipsHiddenCardsUnlessShown(t *testing.T) {
	m := searchModel(t, "fix login", "fix logout")
	m.snap.Tasks[0].Hidden = true
	m = typeInPalette(m, "fix")
	if got := m.paletteCards(); len(got) != 1 || got[0].Title != "fix logout" {
		t.Fatalf("matched %v, want only the visible card", got)
	}
	m.prefs.ShowHidden = true
	if got := m.paletteCards(); len(got) != 2 {
		t.Errorf("matched %d cards with hidden cards shown, want 2", len(got))
	}
}

func TestPaletteEnterOnACardAttachesToIt(t *testing.T) {
	m := searchModel(t, "one", "two")
	target := m.snap.Tasks[1]
	m = withLive(m, target.ID)
	m.filter = "one"
	m = typeInPalette(m, "two")
	next, cmd := m.updatePalette(special(tea.KeyEnter))
	got := next.(Model)
	if got.mode != modeBoard || got.attached != target.ID || cmd == nil {
		t.Errorf("mode %v attached %q cmd %v, want the board attached to %q", got.mode, got.attached, cmd != nil, target.ID)
	}
	if got.filter != "" {
		t.Errorf("filter = %q, want it cleared so the card is selectable", got.filter)
	}
}

func TestPaletteEnterOnACardSelectsItsColumn(t *testing.T) {
	m := searchModel(t, "one", "two")
	m.snap.Tasks[1].ColumnID = m.columns()[1].ID
	target := m.snap.Tasks[1]
	m = withLive(m, target.ID)
	m = typeInPalette(m, "two")
	next, _ := m.updatePalette(special(tea.KeyEnter))
	if cur := next.(Model).current(); cur == nil || cur.ID != target.ID {
		t.Errorf("current = %v, want the card in the second column", cur)
	}
}

func TestPaletteListsCardsNewestFirst(t *testing.T) {
	m := typeInPalette(searchModel(t, "quit a", "quit b", "quit c"), "quit")
	var got []int
	for _, c := range m.paletteCards() {
		got = append(got, c.Number)
	}
	if fmt.Sprint(got) != "[3 2 1]" {
		t.Errorf("cards numbered %v, want newest first", got)
	}
	if m.snap.Tasks[0].Number != 1 {
		t.Error("sorting the matches reordered the snapshot")
	}
}

func TestPaletteSelectionSpansCardsAndCommands(t *testing.T) {
	m := typeInPalette(searchModel(t, "quit one", "quit two"), "quit")
	cards, commands := len(m.paletteCards()), len(m.paletteMatches())
	if cards != 2 || commands != 2 {
		t.Fatalf("%d cards and %d commands matched, want 2 and 2", cards, commands)
	}
	for range cards + commands + 3 {
		m = pressInPalette(m, tea.KeyDown)
	}
	if m.paletteSel != cards+commands-1 {
		t.Fatalf("sel = %d, want clamped to the last command", m.paletteSel)
	}
	for range commands {
		m = pressInPalette(m, tea.KeyUp)
	}
	if m.paletteSel != cards-1 {
		t.Fatalf("sel = %d, want the last card", m.paletteSel)
	}
	m = pressInPalette(m, tea.KeyDown)
	m = pressInPalette(m, tea.KeyDown)
	next, cmd := m.updatePalette(special(tea.KeyEnter))
	if _, ok := cmd().(tea.QuitMsg); !ok || next.(Model).attached != "" {
		t.Errorf("enter on the Quit row returned %T with attached %q, want tea.QuitMsg and no attach", cmd(), next.(Model).attached)
	}
}

func TestPaletteSearchSkipsArchivedCardsInDoneColumn(t *testing.T) {
	m := searchModel(t, "fix login")
	for _, c := range m.columns() {
		if c.IsDone() {
			m.snap.Tasks[0].ColumnID = c.ID
		}
	}
	m.snap.Tasks[0].Archived = true
	m = typeInPalette(m, "fix")
	if got := m.paletteCards(); len(got) != 0 {
		t.Fatalf("matched %v, want archived cards in Done left out", got)
	}
	m.prefs.ShowArchived = true
	if got := m.paletteCards(); len(got) != 1 {
		t.Errorf("matched %d cards with archived cards shown, want 1", len(got))
	}
}

func TestPaletteSearchSkipsCardsInAHiddenReviewLane(t *testing.T) {
	m := searchModel(t, "fix login")
	for _, c := range m.columns() {
		if c.InReviewLane() {
			m.snap.Tasks[0].ColumnID = c.ID
		}
	}
	if m.snap.Tasks[0].ColumnID == m.columns()[0].ID {
		t.Fatal("the default board has no review lane column")
	}
	shown := false
	m.prefs.ReviewLane = &shown
	m = typeInPalette(m, "fix")
	if got := m.paletteCards(); len(got) != 0 {
		t.Fatalf("matched %v, want cards in a hidden review lane left out", got)
	}
	shown = true
	if got := m.paletteCards(); len(got) != 1 {
		t.Errorf("matched %d cards with the review lane shown, want 1", len(got))
	}
}
