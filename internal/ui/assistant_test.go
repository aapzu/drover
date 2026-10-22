package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/engine"
)

type fakeAssistantPanel struct {
	starts, restarts int
	err              error
	screen           string
	running          bool
}

func fakePanel(t *testing.T) *fakeAssistantPanel {
	t.Helper()
	f := &fakeAssistantPanel{running: true}
	origStart, origRestart, origCapture := startAssistant, restartAssistant, captureAssistant
	t.Cleanup(func() { startAssistant, restartAssistant, captureAssistant = origStart, origRestart, origCapture })
	startAssistant = func(string, string, *config.Board) error { f.starts++; return f.err }
	restartAssistant = func(string, string, *config.Board) error { f.restarts++; return f.err }
	captureAssistant = func(string, int, int, int) (string, int, bool, error) {
		if !f.running {
			return "", 0, false, errors.New("no session")
		}
		return f.screen, 0, false, nil
	}
	return f
}

func assistantModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := testModel(120, 30, 2, 1)
	m.eng = &engine.Engine{Root: t.TempDir()}
	m.keys = &keySender{jobs: make(chan keyJob, 16)}
	m.promptInput = textarea.New()
	return m
}

// stamped marks f as captured from the session the panel is on.
func (m Model) stamped(f assistantFrameMsg) assistantFrameMsg {
	f.epoch = m.assist.epoch
	return f
}

func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// runCmds runs cmd and every command a batch returns, and returns the messages.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, runCmds(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

// open presses the assistant key and runs the start the key asked for.
func open(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(m, key(askKey))
	if cmd == nil {
		t.Fatal("opening the panel started nothing")
	}
	m, _ = update(m, assistantStartedMsg{epoch: m.assist.epoch})
	return m
}

func TestAssistantKeyOpensAndFocusesThePanelBesideTheBoard(t *testing.T) {
	f := fakePanel(t)
	m := assistantModel(t)
	m, cmd := update(m, key(askKey))
	if m.mode != modeAssistant || !m.assist.open || !m.assist.starting() {
		t.Fatalf("mode %v, assist %+v", m.mode, m.assist)
	}
	runCmds(cmd)
	if f.starts != 1 {
		t.Errorf("starts = %d", f.starts)
	}
	l := m.layout()
	if l.assistantW < minAssistantWidth || l.boardW+l.assistantW != m.width || sum(func() []int { _, w := m.columnWindow(); return w }()) != l.boardW {
		t.Errorf("board %d + panel %d of %d", l.boardW, l.assistantW, m.width)
	}
	if h := l.hitTest(m, l.boardW, l.bodyY+1); h.kind != hitAssistant {
		t.Errorf("hit = %v", h.kind)
	}
	if h := l.hitTest(m, l.boardW-1, l.bodyY+1); h.kind == hitAssistant {
		t.Error("the board's last column is part of the panel")
	}
}

func TestTypingInThePanelReachesClaudeNotTheBoard(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	col := m.col

	m, _ = update(m, key("l"))
	job, ok := sentJob(m)
	if !ok || job.literal != "l" || job.session == "" {
		t.Fatalf("job = %+v, %v", job, ok)
	}
	if m.col != col {
		t.Error("the key also moved the board")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if job, _ := sentJob(m); job.key != "Escape" || m.mode != modeAssistant {
		t.Errorf("esc should go to claude: %+v, mode %v", job, m.mode)
	}
}

func TestCtrlQLeavesThePanelOpenAndThenHidesIt(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if m.mode != modeBoard || !m.assist.open {
		t.Fatalf("mode %v, open %v; the panel should stay beside the board", m.mode, m.assist.open)
	}
	m, _ = update(m, key("l"))
	if m.col != 1 {
		t.Error("the board should have its keys back")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if m.assist.open || m.layout().assistantW != 0 || m.layout().boardW != m.width {
		t.Error("ctrl+q on the board should hide the panel")
	}
}

func TestReopeningAVisiblePanelDoesNotRestartClaude(t *testing.T) {
	f := fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	m, _ = update(m, key(askKey))
	if m.mode != modeAssistant || f.starts > 1 {
		t.Errorf("mode %v, starts %d", m.mode, f.starts)
	}
}

func TestClickingThePanelFocusesIt(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	l := m.layout()

	m, _ = update(m, tea.MouseMsg{X: l.boardW + 3, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.mode != modeAssistant {
		t.Errorf("mode = %v", m.mode)
	}
	m, _ = update(m, tea.MouseMsg{X: 2, Y: l.bodyY + columnHeaderHeight + 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.mode != modeBoard {
		t.Errorf("clicking a card should give the board its keys back, mode = %v", m.mode)
	}
}

func TestWheelOverThePanelScrollsItsHistory(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m.assist.session, m.assist.history = sessionRunning, 100
	l := m.layout()

	m, _ = update(m, tea.MouseMsg{X: l.boardW + 3, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.assist.scroll != 3 {
		t.Errorf("scroll = %d", m.assist.scroll)
	}
	m.assist.scroll, m.assist.history, m.assist.mouse = 0, 0, true
	m, _ = update(m, tea.MouseMsg{X: l.boardW + 3, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if job, ok := sentJob(m); !ok || job.literal[:4] != "\x1b[<6" {
		t.Errorf("a mouse-tracking claude should get the wheel: %+v, %v", job, ok)
	}
}

func TestPanelShowsClaudesScreenAndAStoppedSession(t *testing.T) {
	f := fakePanel(t)
	m := open(t, assistantModel(t))

	f.screen = "Welcome to Claude Code"
	m, _ = update(m, m.captureAssistantCmd()())
	if view := m.View(); !strings.Contains(view, "Welcome to Claude Code") || !strings.Contains(view, "ASSISTANT") {
		t.Errorf("the panel doesn't show claude's screen:\n%s", view)
	}

	f.running = false
	m, _ = update(m, m.stamped(assistantFrameMsg{err: errors.New("no session"), at: time.Now().Add(time.Second)}))
	if !strings.Contains(m.View(), "Press enter to start it again") {
		t.Error("a stopped session should offer a restart")
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.assist.starting() {
		t.Fatal("enter should start the assistant again")
	}
	cmd()
	if f.starts != 1 || f.restarts != 0 {
		t.Errorf("starts %d restarts %d; a stopped panel resumes its conversation", f.starts, f.restarts)
	}
}

func TestStartFailureIsFlashedNotHidden(t *testing.T) {
	f := fakePanel(t)
	f.err = errors.New("tmux broke")
	m := assistantModel(t)
	m, _ = update(m, key(askKey))
	m, _ = update(m, assistantStartedMsg{m.assist.epoch, f.err})
	if !strings.Contains(m.flash, "tmux broke") || m.assist.alive() || m.assist.starting() {
		t.Errorf("flash %q, assist %+v", m.flash, m.assist)
	}
}

func TestNewConversationRestartsThePanel(t *testing.T) {
	f := fakePanel(t)
	m := assistantModel(t)
	m, cmd := update(m, key(newAskKey))
	if !m.assist.open || m.mode != modeAssistant {
		t.Fatalf("mode %v, open %v", m.mode, m.assist.open)
	}
	runCmds(cmd)
	if f.restarts != 1 {
		t.Errorf("restarts = %d, starts = %d", f.restarts, f.starts)
	}
}

func TestPanelNeedsRoomForTheBoard(t *testing.T) {
	fakePanel(t)
	m := assistantModel(t)
	m.width = 90
	m, cmd := update(m, key(askKey))
	if m.assist.open || cmd != nil || !strings.Contains(m.flash, "too small") {
		t.Errorf("open %v, flash %q", m.assist.open, m.flash)
	}

	m.width = 120
	m = open(t, m)
	m, _ = update(m, tea.WindowSizeMsg{Width: 90, Height: 30})
	if m.assist.open || m.mode != modeBoard {
		t.Errorf("shrinking the terminal should hide the panel: open %v, mode %v", m.assist.open, m.mode)
	}
}

func TestPaletteHasTheAssistantCommands(t *testing.T) {
	m := assistantModel(t)
	m.paletteInput = newPaletteInput()
	m.paletteInput.Focus()
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlK})
	for _, r := range "assistant" {
		m = typeInPalette(m, string(r))
	}
	if n := len(m.paletteMatches()); n != 3 {
		t.Fatalf("palette matches = %+v", m.paletteMatches())
	}
	fakePanel(t)
	m = pressInPalette(m, tea.KeyEnter)
	if m.mode != modeAssistant {
		t.Errorf("mode = %v after choosing the first assistant command", m.mode)
	}
}

func TestNewConversationStartsTheSessionOnce(t *testing.T) {
	for name, alive := range map[string]bool{"cold": false, "running": true} {
		t.Run(name, func(t *testing.T) {
			f := fakePanel(t)
			m := assistantModel(t)
			m.assist.session = sessionStopped
			if alive {
				m.assist.session = sessionRunning
			}
			m, cmd := update(m, key(newAskKey))
			runCmds(cmd)
			if f.starts != 0 || f.restarts != 1 {
				t.Errorf("starts %d, restarts %d; want one restart", f.starts, f.restarts)
			}
			if !m.assist.starting() || m.assist.alive() {
				t.Errorf("assist = %+v", m.assist)
			}
		})
	}
}

func TestShortTerminalNeverShowsOrCrashesOnThePanel(t *testing.T) {
	fakePanel(t)
	m := assistantModel(t)
	m = open(t, m)
	for height := 1; height <= 12; height++ {
		m.height = height
		m.View()
	}
	m, _ = update(m, tea.WindowSizeMsg{Width: 120, Height: 5})
	if m.assist.open || m.mode != modeBoard {
		t.Errorf("a terminal too short for the panel should hide it: open %v, mode %v", m.assist.open, m.mode)
	}
	m.height = 5
	if _, cmd := update(m, key(askKey)); cmd != nil {
		t.Error("the panel opened on a terminal too short for it")
	}
}

func TestStaleAndStartingFramesAreIgnored(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	now := time.Now()

	m, _ = update(m, m.stamped(assistantFrameMsg{txt: "new", at: now}))
	m, _ = update(m, m.stamped(assistantFrameMsg{txt: "old", at: now.Add(-time.Second)}))
	if m.assist.txt != "new" {
		t.Errorf("an older frame replaced the newer one: %q", m.assist.txt)
	}

	m.assist.session = sessionStarting
	m, _ = update(m, m.stamped(assistantFrameMsg{err: errors.New("gone"), at: now.Add(time.Second)}))
	if m.assist.alive() || m.assist.lastErr != nil {
		t.Error("a frame from the old session changed a restarting panel")
	}
}

func TestScrollStaysAnchoredAsOutputArrives(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	now := time.Now()
	m, _ = update(m, m.stamped(assistantFrameMsg{history: 100, at: now}))
	m.assist.scroll = 5
	m, _ = update(m, m.stamped(assistantFrameMsg{history: 110, at: now.Add(time.Second)}))
	if m.assist.scroll != 15 {
		t.Errorf("scroll = %d after 10 new lines, want 15", m.assist.scroll)
	}
	m, _ = update(m, m.stamped(assistantFrameMsg{history: 4, at: now.Add(2 * time.Second)}))
	if m.assist.scroll < 0 || m.assist.scroll > 4 {
		t.Errorf("scroll = %d with 4 lines of history", m.assist.scroll)
	}
}

func TestCaptureLoopRunsOnceAndOnlyWhileFocused(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	gen := m.assist.gen

	if _, cmd := update(m, assistantTickMsg{gen: gen - 1}); cmd != nil {
		t.Error("a tick from an older loop kept running")
	}
	if _, cmd := update(m, assistantTickMsg{gen: gen}); cmd == nil {
		t.Error("the focused panel stopped capturing")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if _, cmd := update(m, assistantTickMsg{gen: gen}); cmd != nil {
		t.Error("the loop kept running after the panel lost focus")
	}
	l := m.layout()
	m, _ = update(m, tea.MouseMsg{X: l.boardW + 3, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.assist.gen != gen+1 {
		t.Errorf("focusing by click should start one new loop: gen %d, was %d", m.assist.gen, gen)
	}
}

func TestStoppedPanelIsNotCaptured(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	m.assist.session = sessionStopped
	m.frame = 1
	if _, cmd := update(m, tickMsg(time.Now())); cmd == nil {
		t.Fatal("the board should keep ticking")
	}
	if m.assistantCapturing() {
		t.Error("a stopped assistant is still polled")
	}
}

func TestKeysToAStoppedPanelSayWhy(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m.assist.session = sessionStopped
	m, cmd := update(m, key("x"))
	if cmd != nil || !strings.Contains(m.flash, "isn't running") {
		t.Errorf("cmd %v, flash %q", cmd, m.flash)
	}
	if _, ok := sentJob(m); ok {
		t.Error("a key reached a stopped session")
	}
}

func TestStoppedPanelShowsWhyItStopped(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, m.stamped(assistantFrameMsg{err: errors.New("can't find session"), at: time.Now().Add(time.Second)}))
	if view := m.View(); !strings.Contains(view, "can't find session") {
		t.Errorf("the panel hides the capture error:\n%s", view)
	}
}

func TestWheelOverThePanelSendsExactReports(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m.assist.session, m.assist.mouse, m.assist.history = sessionRunning, true, 0
	l := m.layout()
	at := func(button tea.MouseButton, x, y int) string {
		t.Helper()
		next, _ := update(m, tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: button})
		job, ok := sentJob(next)
		if !ok {
			t.Fatal("no report sent")
		}
		return job.literal
	}
	if got := at(tea.MouseButtonWheelUp, l.boardW+5, l.bodyY+4); got != "\x1b[<64;5;4M" {
		t.Errorf("wheel up = %q", got)
	}
	if got := at(tea.MouseButtonWheelDown, l.boardW+5, l.bodyY+4); got != "\x1b[<65;5;4M" {
		t.Errorf("wheel down = %q", got)
	}
	if got := at(tea.MouseButtonWheelDown, l.boardW, l.bodyY); got != "\x1b[<65;1;1M" {
		t.Errorf("wheel on the border = %q", got)
	}

	m.assist.history = 50
	next, _ := update(m, tea.MouseMsg{X: l.boardW + 5, Y: l.bodyY + 4, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if _, ok := sentJob(next); ok || next.assist.scroll != 3 {
		t.Errorf("with history to scroll the wheel belongs to the panel: scroll %d", next.assist.scroll)
	}
}

func TestPanelPassesPastesAndDropsUnmappableKeys(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb"), Paste: true})
	if job, ok := sentJob(m); !ok || job.paste != "a\nb" {
		t.Errorf("job = %+v, %v", job, ok)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab"), Alt: true})
	if job, ok := sentJob(m); ok {
		t.Errorf("an unmappable key was sent: %+v", job)
	}
}

func TestDroppingACardOnThePanelDoesNothing(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	l := m.layout()
	m.drag = dragState{kind: dragCard, task: m.snap.Tasks[0].ID, fromCol: 0, x: l.boardW + 2, y: l.bodyY + 2}
	_, cmd := update(m, tea.MouseMsg{X: l.boardW + 2, Y: l.bodyY + 2, Action: tea.MouseActionRelease})
	if cmd != nil {
		t.Error("dropping a card on the assistant moved it")
	}
}

func TestQuittingStopsOnlyAnAssistantThisBoardStarted(t *testing.T) {
	fakePanel(t)
	var stopped []string
	orig := stopAssistant
	t.Cleanup(func() { stopAssistant = orig })
	stopAssistant = func(root string) error { stopped = append(stopped, root); return nil }

	m := assistantModel(t)
	_ = m.StopAssistant()
	if len(stopped) != 0 {
		t.Fatal("a board that never opened the panel stopped another board's assistant")
	}
	m = open(t, m)
	_ = m.StopAssistant()
	if len(stopped) != 1 || stopped[0] != m.eng.Root {
		t.Errorf("stopped = %q", stopped)
	}
}

func TestMessagesFromAnEarlierStartAreIgnored(t *testing.T) {
	f := fakePanel(t)
	m := assistantModel(t)
	m, _ = update(m, key(askKey))
	first := m.assist.epoch
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	m, cmd := update(m, key(newAskKey))
	if m.assist.epoch == first {
		t.Fatal("a new conversation should be a new start")
	}
	runCmds(cmd)

	m, _ = update(m, assistantStartedMsg{epoch: first})
	if !m.assist.starting() {
		t.Error("the first start finished the restart")
	}
	m, _ = update(m, assistantFrameMsg{epoch: first, txt: "old session", at: time.Now()})
	if m.assist.txt != "" {
		t.Errorf("a frame from the old session was shown: %q", m.assist.txt)
	}
	m, _ = update(m, assistantStartedMsg{epoch: m.assist.epoch, err: f.err})
	if !m.assist.alive() {
		t.Error("the current start should finish")
	}
}
