package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/store"
)

func titles(tasks []*store.Task) string {
	var out []string
	for _, t := range tasks {
		out = append(out, t.Title)
	}
	return strings.Join(out, ",")
}

func TestBoardKeysMoveTheSelection(t *testing.T) {
	tests := []struct {
		name string
		keys []tea.KeyMsg
		col  int
		card string
	}{
		{"j", []tea.KeyMsg{key("j")}, 0, "b"},
		{"down", []tea.KeyMsg{special(tea.KeyDown)}, 0, "b"},
		{"j stops at the last card", []tea.KeyMsg{key("j"), key("j"), key("j")}, 0, "c"},
		{"k", []tea.KeyMsg{key("j"), key("j"), key("k")}, 0, "b"},
		{"up stops at the first card", []tea.KeyMsg{special(tea.KeyUp), special(tea.KeyUp)}, 0, "a"},
		{"G", []tea.KeyMsg{key("G")}, 0, "c"},
		{"g", []tea.KeyMsg{key("G"), key("g")}, 0, "a"},
		{"l", []tea.KeyMsg{key("l")}, 1, ""},
		{"right", []tea.KeyMsg{special(tea.KeyRight)}, 1, ""},
		{"h", []tea.KeyMsg{key("l"), key("h")}, 0, "a"},
		{"left stops at the first column", []tea.KeyMsg{special(tea.KeyLeft)}, 0, "a"},
		{"l stops at the last column", []tea.KeyMsg{key("l"), key("l"), key("l"), key("l"), key("l"), key("l")}, 4, ""},
		{"j in an empty column", []tea.KeyMsg{key("l"), key("j"), key("G"), key("g")}, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := realModel(t, "a", "b", "c")
			for _, k := range tt.keys {
				m, _ = update(m, k)
			}
			cur := m.current()
			switch {
			case m.col != tt.col:
				t.Errorf("col = %d, want %d", m.col, tt.col)
			case tt.card == "" && cur != nil:
				t.Errorf("selected %q in an empty column", cur.Title)
			case tt.card != "" && (cur == nil || cur.Title != tt.card):
				t.Errorf("selected %v, want %q", cur, tt.card)
			}
		})
	}
}

func TestBoardKeysMoveTheCardBetweenColumns(t *testing.T) {
	m := realModel(t, "a")
	card := m.current()

	m, cmd := update(m, key("L"))
	if cmd == nil || m.col != 1 || m.busy[card.ID].column != "executing" || m.selected["executing"] != card.ID {
		t.Fatalf("L: col %d busy %v cmd nil %v", m.col, m.busy, cmd == nil)
	}
	if m.current() == nil || m.current().ID != card.ID {
		t.Error("the selection did not follow the card")
	}

	moved := m
	moved.snap.Tasks[0].ColumnID = "executing"
	for _, k := range []tea.KeyMsg{key("H"), special(tea.KeyShiftLeft)} {
		moved.busy = map[string]op{}
		next, cmd := update(moved, k)
		if cmd == nil || next.col != 0 || next.busy[card.ID].column != "planning" {
			t.Errorf("%q: col %d busy %v cmd nil %v", k.String(), next.col, next.busy, cmd == nil)
		}
	}
	moved.busy = map[string]op{}
	next, cmd := update(moved, special(tea.KeyShiftRight))
	if cmd == nil || next.busy[card.ID].column != "waiting-for-me" {
		t.Errorf("shift+right: busy %v", next.busy)
	}

	first := realModel(t, "a")
	if next, cmd := update(first, key("H")); cmd != nil || len(next.busy) != 0 {
		t.Error("H moved a card out of the first column")
	}
	last := realModel(t, "a")
	last.snap.Tasks[0].ColumnID = "done"
	last.col = 4
	if next, cmd := update(last, key("L")); cmd != nil || len(next.busy) != 0 {
		t.Error("L moved a card out of the last column")
	}
	if _, cmd := update(realModel(t), key("L")); cmd != nil {
		t.Error("L with no card did something")
	}
}

func TestBoardKeysTogglePrefsAndSaveThem(t *testing.T) {
	tests := []struct {
		key  string
		get  func(prefs) any
		want any
	}{
		{"p", func(p prefs) any { return p.HidePreview }, true},
		{"B", func(p prefs) any { return p.ShowHidden }, true},
		{"v", func(p prefs) any { return p.ShowArchived }, true},
		{"t", func(p prefs) any { return p.ShowSettings }, true},
		{"z", func(p prefs) any { return p.Density }, densityCompact},
		{"M", func(p prefs) any { return p.NoMouse }, true},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m := realModel(t, "a")
			m, _ = update(m, key(tt.key))
			if got := tt.get(m.prefs); got != tt.want {
				t.Errorf("prefs = %v, want %v", got, tt.want)
			}
			if got := tt.get(loadPrefs(m.eng.Root)); got != tt.want {
				t.Errorf("saved prefs = %v, want %v", got, tt.want)
			}
			m, _ = update(m, key(tt.key))
			if tt.key == "z" {
				if got := tt.get(m.prefs); got != densityTall {
					t.Errorf("second z: density %v", got)
				}
				return
			}
			if got := tt.get(m.prefs); got == tt.want {
				t.Errorf("second %s did not toggle back", tt.key)
			}
		})
	}
}

func TestDensityCyclesBackToNormal(t *testing.T) {
	m := realModel(t, "a")
	for range 3 {
		m, _ = update(m, key("z"))
	}
	if m.prefs.Density != densityNormal {
		t.Errorf("density = %v after three presses", m.prefs.Density)
	}
}

func TestMouseKeyFlashesAndTogglesTheTerminalsMouse(t *testing.T) {
	m := realModel(t, "a")
	m, cmd := update(m, key("M"))
	if cmd == nil || !strings.Contains(m.flash, "Mouse off") {
		t.Errorf("M: cmd nil %v flash %q", cmd == nil, m.flash)
	}
	m, cmd = update(m, key("M"))
	if cmd == nil || !strings.Contains(m.flash, "Mouse on") {
		t.Errorf("M again: cmd nil %v flash %q", cmd == nil, m.flash)
	}
}

func TestBoardKeysResizeAndResetTheLayout(t *testing.T) {
	m := realModel(t, "a")
	preview := m.layout().previewH
	width := m.layout().cols[0].width

	for _, tt := range []struct {
		key  string
		want int
	}{{"+", preview + 2}, {"=", preview + 2}, {"-", preview - 2}, {"_", preview - 2}} {
		next, _ := update(m, key(tt.key))
		if got := next.layout().previewH; got != tt.want || next.prefs.PreviewHeight != tt.want {
			t.Errorf("%s: preview %d (pref %d), want %d", tt.key, got, next.prefs.PreviewHeight, tt.want)
		}
		if got := loadPrefs(m.eng.Root).PreviewHeight; got != tt.want {
			t.Errorf("%s: saved preview %d, want %d", tt.key, got, tt.want)
		}
	}
	for _, tt := range []struct {
		key  string
		want int
	}{{">", width + 4}, {"<", width - 4}} {
		m.prefs.ColumnWidths = map[string]int{}
		next, _ := update(m, key(tt.key))
		if got := next.prefs.ColumnWidths["planning"]; got != tt.want {
			t.Errorf("%s: column width %d, want %d", tt.key, got, tt.want)
		}
		if got := loadPrefs(m.eng.Root).ColumnWidths["planning"]; got != tt.want {
			t.Errorf("%s: saved column width %d, want %d", tt.key, got, tt.want)
		}
	}

	m.prefs = prefs{PreviewHeight: 10, AssistantWidth: 40, Density: densityTall, ColumnWidths: map[string]int{"planning": 20}}
	m, _ = update(m, key("0"))
	if m.prefs.PreviewHeight != 0 || m.prefs.AssistantWidth != 0 || m.prefs.Density != densityNormal || len(m.prefs.ColumnWidths) != 0 {
		t.Errorf("0 left %+v", m.prefs)
	}
	if got := loadPrefs(m.eng.Root); got.PreviewHeight != 0 || got.Density != densityNormal {
		t.Errorf("saved %+v", got)
	}
}

func TestPreviewResizeKeysStayWithinTheScreen(t *testing.T) {
	m := realModel(t, "a")
	m.prefs.PreviewHeight = minPreviewHeight
	m, _ = update(m, key("-"))
	if m.prefs.PreviewHeight != minPreviewHeight {
		t.Errorf("preview shrank past the minimum: %d", m.prefs.PreviewHeight)
	}
	m.prefs.PreviewHeight = m.layout().bodyH - minBoardHeight
	m, _ = update(m, key("+"))
	if got := m.prefs.PreviewHeight; got != m.layout().bodyH-minBoardHeight {
		t.Errorf("preview grew past the board: %d", got)
	}

	m.prefs = prefs{ColumnWidths: map[string]int{}, HidePreview: true}
	if m, _ = update(m, key("+")); m.prefs.PreviewHeight != 0 {
		t.Errorf("+ with no preview set a height: %d", m.prefs.PreviewHeight)
	}
}

func TestPageKeysScrollThePreview(t *testing.T) {
	m := realModel(t, "a")
	m.previewHistory = 100
	page := m.layout().previewH - 3

	m, cmd := update(m, special(tea.KeyPgUp))
	if m.previewScroll != page || cmd == nil {
		t.Errorf("pgup: scroll %d cmd nil %v, want %d", m.previewScroll, cmd == nil, page)
	}
	m, _ = update(m, special(tea.KeyPgDown))
	if m.previewScroll != 0 {
		t.Errorf("pgdown: scroll %d", m.previewScroll)
	}
	m, _ = update(m, special(tea.KeyPgDown))
	if m.previewScroll != 0 {
		t.Errorf("pgdown past the bottom: scroll %d", m.previewScroll)
	}
	m.previewHistory = 5
	if m, _ = update(m, special(tea.KeyPgUp)); m.previewScroll != 5 {
		t.Errorf("pgup past the history: scroll %d", m.previewScroll)
	}
}

func TestBoardKeysOpenTheirModes(t *testing.T) {
	tests := []struct {
		key  string
		mode mode
	}{
		{"?", modeHelp},
		{"i", modeDetail},
		{"e", modeForm},
		{"m", modeMove},
		{"x", modeConfirm},
		{"D", modeConfirm},
		{"delete", modeConfirm},
		{"/", modeFilter},
		{"o", modeConfig},
		{"I", modeImport},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m := realModel(t, "a")
			msg := key(tt.key)
			if tt.key == "delete" {
				msg = special(tea.KeyDelete)
			}
			m, _ = update(m, msg)
			if m.mode != tt.mode {
				t.Errorf("mode = %v, want %v", m.mode, tt.mode)
			}
		})
	}
}

func TestBoardKeysNeedACard(t *testing.T) {
	for _, k := range []string{"m", "e", "x", "s", "r", "S", "u", "c", "i", "A", "b", "enter", "a", "H", "L", "tab"} {
		m := realModel(t)
		msg := key(k)
		switch k {
		case "enter":
			msg = special(tea.KeyEnter)
		case "tab":
			msg = special(tea.KeyTab)
		}
		next, cmd := update(m, msg)
		if next.mode != modeBoard || cmd != nil || len(next.busy) != 0 {
			t.Errorf("%s on an empty board: mode %v cmd nil %v busy %v", k, next.mode, cmd == nil, next.busy)
		}
	}
}

func TestMoveKeyStartsOnTheCardsColumn(t *testing.T) {
	m := realModel(t, "a")
	m.snap.Tasks[0].ColumnID = "waiting-for-me"
	m.col = 2
	m, _ = update(m, key("m"))
	if m.modalTask != m.snap.Tasks[0].ID || m.moveSel != 2 {
		t.Errorf("modal task %q moveSel %d", m.modalTask, m.moveSel)
	}
}

func TestFilterKeysNarrowThenClear(t *testing.T) {
	m := realModel(t, "alpha", "beta")
	m, _ = update(m, key("/"))
	m, _ = update(m, key("bet"))
	if titles(m.tasksForColumn(0)) != "beta" || m.filter != "bet" {
		t.Fatalf("filter %q shows %q", m.filter, titles(m.tasksForColumn(0)))
	}
	m, _ = update(m, special(tea.KeyEnter))
	if m.mode != modeBoard || m.filter != "bet" {
		t.Fatalf("enter: mode %v filter %q, want the filter kept", m.mode, m.filter)
	}
	m, _ = update(m, key("/"))
	if m.filterInput.Value() != "bet" {
		t.Errorf("reopened with %q, want the filter in the input", m.filterInput.Value())
	}
	m, _ = update(m, special(tea.KeyEsc))
	if m.mode != modeBoard || m.filter != "" || len(m.tasksForColumn(0)) != 2 {
		t.Errorf("esc in the filter: mode %v filter %q", m.mode, m.filter)
	}

	m, _ = update(m, key("/"))
	m, _ = update(m, key("a"))
	m, _ = update(m, special(tea.KeyEnter))
	m, _ = update(m, special(tea.KeyEsc))
	if m.filter != "" {
		t.Errorf("esc on the board kept the filter %q", m.filter)
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []tea.KeyMsg{key("q"), special(tea.KeyCtrlC)} {
		_, cmd := update(realModel(t, "a"), k)
		if cmd == nil {
			t.Fatalf("%q did not quit", k.String())
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%q returned %T, want tea.QuitMsg", k.String(), cmd())
		}
	}
}

func TestSuspendAllAndQuitCountsRunningAndQueuedAgents(t *testing.T) {
	m := realModel(t, "a", "b", "c")
	m, cmd := update(m, key("Q"))
	if m.mode != modeBoard || cmd != nil || m.flash != "No agents running. Press q to quit." {
		t.Fatalf("with no agents: mode %v flash %q", m.mode, m.flash)
	}

	session := func(s store.SessionStatus) map[string]*store.Session {
		return map[string]*store.Session{store.MainSession: {Status: s}}
	}
	m.snap.Tasks[0].Sessions = session(store.StatusRunning)
	m.snap.Tasks[1].Sessions = session(store.StatusQueued)
	m.snap.Tasks[2].Sessions = session(store.StatusSuspended)
	m, _ = update(m, key("Q"))
	if m.mode != modeConfirm || !strings.Contains(m.confirmText, "Suspend all 2 agents") {
		t.Fatalf("mode %v text %q", m.mode, m.confirmText)
	}
	m, cmd = update(m, key("y"))
	if _, ok := cmd().(suspendAllMsg); !ok {
		t.Errorf("y returned %T, want suspendAllMsg", cmd())
	}
}

func TestNewKeyStartsASessionAndSaysSo(t *testing.T) {
	m, cmd := update(realModel(t), key("n"))
	if cmd == nil || m.flash != "Starting a new session…" {
		t.Errorf("cmd nil %v flash %q", cmd == nil, m.flash)
	}
}

func TestCardActionKeysMarkTheCardBusy(t *testing.T) {
	for k, label := range map[string]string{"r": "starting agent", "S": "suspending", "u": "updating from base", "enter": "starting agent", "a": "starting agent"} {
		t.Run(k, func(t *testing.T) {
			m := realModel(t, "a")
			msg := key(k)
			if k == "enter" {
				msg = special(tea.KeyEnter)
			}
			m, cmd := update(m, msg)
			if cmd == nil || m.busy[m.current().ID].label != label {
				t.Errorf("cmd nil %v busy %v, want %q", cmd == nil, m.busy, label)
			}
			if !strings.Contains(m.status(m.current()), label) {
				t.Errorf("card status = %q", m.status(m.current()))
			}
		})
	}
}

func TestEnterAttachesToARunningAgent(t *testing.T) {
	m := liveCard(t, "a")
	m, cmd := update(m, special(tea.KeyEnter))
	if m.attached != m.current().ID || cmd == nil || len(m.busy) != 0 {
		t.Errorf("attached %q cmd nil %v busy %v", m.attached, cmd == nil, m.busy)
	}
}

func TestEnterOnAnExternalAgentLooksForItsTerminal(t *testing.T) {
	m := realModel(t, "a")
	card := m.snap.Tasks[0]
	card.Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusExternal, PID: 4242}}

	m, cmd := update(m, special(tea.KeyEnter))
	if cmd == nil || len(m.busy) != 0 {
		t.Fatalf("cmd nil %v busy %v", cmd == nil, m.busy)
	}
	msgs := runCmds(cmd)
	got, ok := msgs[0].(externalAttachMsg)
	if !ok || got.id != card.ID || got.number != 1 || got.pid != 4242 || got.cmd != nil {
		t.Errorf("msgs = %+v", msgs)
	}
}

func TestShellKeyReportsErrors(t *testing.T) {
	m := realModel(t, "a")
	m.snap.Tasks[0].ID = "not-in-the-engine"
	m, cmd := update(m, key("c"))
	if cmd != nil || m.flash != "Error: task not found" {
		t.Errorf("cmd nil %v flash %q", cmd == nil, m.flash)
	}
}

func TestTabTypesIntoTheAgentOnlyWhenOneRuns(t *testing.T) {
	m := realModel(t, "a")
	m, cmd := update(m, special(tea.KeyTab))
	if m.mode != modeBoard || cmd != nil || !strings.Contains(m.flash, "No running agent to type into") {
		t.Fatalf("mode %v cmd nil %v flash %q", m.mode, cmd == nil, m.flash)
	}

	m = liveCard(t, "a")
	m.prefs.HidePreview = true
	m, cmd = update(m, special(tea.KeyTab))
	if m.mode != modeLive || m.liveGen != 1 || m.prefs.HidePreview || cmd == nil {
		t.Errorf("mode %v gen %d hidePreview %v cmd nil %v", m.mode, m.liveGen, m.prefs.HidePreview, cmd == nil)
	}
	if loadPrefs(m.eng.Root).HidePreview {
		t.Error("the preview was left hidden in the saved layout")
	}
	if msgs := runCmds(cmd); len(msgs) != 2 {
		t.Errorf("tab started %d things, want the live tick and a capture", len(msgs))
	}
}

func TestHideKeyHidesTheCardUntilShown(t *testing.T) {
	m := realModel(t, "a", "b")
	m, cmd := update(m, key("b"))
	if cmd == nil || m.busy[m.current().ID].label != "hiding" {
		t.Fatalf("cmd nil %v busy %v", cmd == nil, m.busy)
	}
	for _, msg := range runCmds(cmd) {
		m, _ = update(m, msg)
	}
	if m.flash != "Hid #1. B shows hidden cards." || titles(m.tasksForColumn(0)) != "b" {
		t.Errorf("flash %q cards %q", m.flash, titles(m.tasksForColumn(0)))
	}
	m, _ = update(m, key("B"))
	if titles(m.tasksForColumn(0)) != "a,b" {
		t.Errorf("cards %q with hidden cards shown", titles(m.tasksForColumn(0)))
	}
}

func TestArchiveKeyRefusesWhereCardsCannotBeArchived(t *testing.T) {
	m := realModel(t, "a")
	m, cmd := update(m, key("A"))
	if cmd == nil || m.busy[m.current().ID].label != "archiving" {
		t.Fatalf("cmd nil %v busy %v", cmd == nil, m.busy)
	}
	msgs := runCmds(cmd)
	done, ok := msgs[0].(actionDoneMsg)
	if !ok || done.err == nil {
		t.Errorf("archiving a card outside Done returned %+v, want an error", msgs[0])
	}
}

func TestAssistantKeysReachTheBoardKeyHandler(t *testing.T) {
	fakePanel(t)
	m := realModel(t, "a")
	m, _ = update(m, key(askKey))
	if m.mode != modeAssistant {
		t.Fatalf("%s: mode %v", askKey, m.mode)
	}
	m.mode = modeBoard
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if m.assist.open {
		t.Error("ctrl+q on the board left the panel open")
	}
}

// paletteSetup returns a model where the command bound to k has something to act on.
func paletteSetup(t *testing.T, k string) Model {
	t.Helper()
	m := realModel(t, "a")
	m.snap.Tasks[0].ColumnID = "executing"
	m.col = 1
	m.prefs.PreviewHeight = 10
	switch k {
	case "c":
		m.snap.Tasks[0].ID = "not-in-the-engine"
	case liveExitKey:
		m.assist.open = true
	}
	return m
}

func TestEveryPaletteCommandDoesSomethingOnTheBoard(t *testing.T) {
	fakePanel(t)
	state := func(m Model) string {
		return fmt.Sprintf("%v|%q|%+v|%v|%d|%d|%v", m.mode, m.flash, m.prefs, m.col, len(m.busy), m.previewScroll, m.assist.open)
	}
	for _, c := range paletteCommands {
		t.Run(c.name, func(t *testing.T) {
			m := paletteSetup(t, c.key)
			before := state(m)
			next, cmd := m.updateBoard(keyPress(c.key))
			if after := state(next.(Model)); after == before && cmd == nil {
				t.Errorf("pressing %q on the board did nothing", c.key)
			}
		})
	}
}
