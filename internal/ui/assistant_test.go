package ui

import (
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/assistant"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/engine"
)

type fakeAssistantPanel struct {
	starts, restarts int
	err              error
	attaches         bool
	screen           string
	running          bool
	captures         int
	capturedName     string
	capturedW        int
	capturedH        int
	capturedBack     int
}

func fakePanel(t *testing.T) *fakeAssistantPanel {
	t.Helper()
	f := &fakeAssistantPanel{running: true}
	origStart, origRestart, origCapture := startAssistant, restartAssistant, captureAssistant
	t.Cleanup(func() { startAssistant, restartAssistant, captureAssistant = origStart, origRestart, origCapture })
	startAssistant = func(string, string, *config.Board) (bool, error) {
		f.starts++
		return !f.attaches && f.err == nil, f.err
	}
	restartAssistant = func(string, string, *config.Board) error { f.restarts++; return f.err }
	captureAssistant = func(name string, w, h, scrollback int) (string, int, bool, error) {
		f.captures++
		f.capturedName, f.capturedW, f.capturedH, f.capturedBack = name, w, h, scrollback
		if !f.running {
			return "", 0, false, errSessionEnded
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

func (m Model) stamped(f assistantFrameMsg) assistantFrameMsg {
	f.epoch = m.assist.epoch
	return f
}

func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

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

func open(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(m, key(askKey))
	if cmd == nil {
		t.Fatal("opening the panel started nothing")
	}
	m, _ = update(m, assistantStartedMsg{epoch: m.assist.epoch, created: true})
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
	if h := l.hitTest(m, l.boardW, l.bodyY+1); h.kind != hitAssistantEdge {
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
func TestCtrlQHidesThePanel(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *Model)
	}{
		{"focused", func(*Model) {}},
		{"unfocused, from the board", func(m *Model) { m.mode = modeBoard }},
		{"stopped", func(m *Model) { m.assist.fail(errSessionEnded) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakePanel(t)
			m := open(t, assistantModel(t))
			tt.setup(&m)

			m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
			if m.mode != modeBoard || m.assist.open || m.layout().assistantW != 0 || m.layout().boardW != m.width {
				t.Fatalf("mode %v, open %v; one ctrl+q should hide the panel and return to the board", m.mode, m.assist.open)
			}
			if _, ok := sentJob(m); ok {
				t.Error("ctrl+q was sent to the session")
			}
			m, _ = update(m, key("l"))
			if m.col != 1 {
				t.Error("the board should have its keys back")
			}
		})
	}
}
func TestReopeningARunningPanelDoesNotRestartClaude(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(m Model) Model
		wantCaptures int
	}{
		{"visible", func(m Model) Model { m.mode = modeBoard; return m }, -1},
		{"hidden", func(m Model) Model {
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
			return m
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fakePanel(t)
			m := tt.setup(open(t, assistantModel(t)))
			before := f.captures
			m, cmd := update(m, key(askKey))
			runCmds(cmd)
			if m.mode != modeAssistant || f.starts != 0 || f.restarts != 0 {
				t.Errorf("mode %v, starts %d, restarts %d; reopening a running panel must not start claude", m.mode, f.starts, f.restarts)
			}
			if tt.wantCaptures >= 0 && f.captures != before+tt.wantCaptures {
				t.Errorf("captured %d times on reopening; the stale frame should be refreshed at once", f.captures-before)
			}
		})
	}
}

func TestClickingThePanelFocusesIt(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m.mode = modeBoard
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

func TestDraggingThePanelEdgeResizesAndSavesIt(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	l := m.layout()
	press := tea.MouseMsg{X: l.boardW, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	m, _ = update(m, press)
	m, _ = update(m, tea.MouseMsg{X: 70, Y: l.bodyY + 3, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m, _ = update(m, tea.MouseMsg{X: 70, Y: l.bodyY + 3, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if w := m.layout().assistantW; w != m.width-70 {
		t.Errorf("panel width = %d, want %d", w, m.width-70)
	}
	if got := loadPrefs(m.eng.Root).AssistantWidth; got != m.width-70 {
		t.Errorf("saved width = %d, want %d", got, m.width-70)
	}

	edge := m.layout().boardW
	m, _ = update(m, tea.MouseMsg{X: edge, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m, _ = update(m, tea.MouseMsg{X: 0, Y: l.bodyY + 3, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	if w := m.layout().assistantW; w != m.width-minBoardWidth {
		t.Errorf("dragged past the board's minimum: panel width = %d, want %d", w, m.width-minBoardWidth)
	}
}

func TestSavedPanelWidthIsAppliedOnStart(t *testing.T) {
	fakePanel(t)
	m := assistantModel(t)
	if err := (prefs{AssistantWidth: 50}).save(m.eng.Root); err != nil {
		t.Fatal(err)
	}
	m.prefs = loadPrefs(m.eng.Root)
	m = open(t, m)
	if w := m.layout().assistantW; w != 50 {
		t.Errorf("panel width = %d, want 50", w)
	}
}

func TestZeroResetsThePanelWidth(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m.prefs.AssistantWidth = 50
	m.savePrefs()
	m.mode = modeBoard
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("0")})
	if w := m.layout().assistantW; w != m.width*2/5 {
		t.Errorf("panel width = %d, want the default %d", w, m.width*2/5)
	}
	if got := loadPrefs(m.eng.Root).AssistantWidth; got != 0 {
		t.Errorf("saved width = %d, want 0", got)
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
	m, _ = update(m, m.stamped(assistantFrameMsg{err: errSessionEnded, at: time.Now().Add(time.Second)}))
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
	m, _ = update(m, assistantStartedMsg{epoch: m.assist.epoch, err: f.err})
	if !strings.Contains(m.flash, "tmux broke") || m.assist.running() || m.assist.starting() {
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
	if m.mode != modeBoard || m.layout().assistantW != 0 || m.layout().boardW != 90 {
		t.Errorf("shrinking the terminal should take focus and room from the panel: mode %v, layout %+v", m.mode, m.layout())
	}
	m, _ = update(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if !m.assist.open || m.layout().assistantW == 0 {
		t.Error("the panel should come back when the terminal grows again")
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
			if !m.assist.starting() || m.assist.running() {
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
	if m.mode != modeBoard || m.layout().assistantW != 0 {
		t.Errorf("a terminal too short for the panel should drop it: mode %v, layout %+v", m.mode, m.layout())
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
	if m.assist.running() || m.assist.lastErr != nil {
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
	gen := m.assist.tickGen

	if _, cmd := update(m, assistantTickMsg{gen: gen - 1}); cmd != nil {
		t.Error("a tick from an older loop kept running")
	}
	if _, cmd := update(m, assistantTickMsg{gen: gen}); cmd == nil {
		t.Error("the focused panel stopped capturing")
	}
	m.mode = modeBoard
	if _, cmd := update(m, assistantTickMsg{gen: gen}); cmd != nil {
		t.Error("the loop kept running after the panel lost focus")
	}
	l := m.layout()
	m, _ = update(m, tea.MouseMsg{X: l.boardW + 3, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.assist.tickGen != gen+1 {
		t.Errorf("focusing by click should start one new loop: gen %d, was %d", m.assist.tickGen, gen)
	}
}
func TestBoardTickCapturesAnUnfocusedPanelOnly(t *testing.T) {
	f := fakePanel(t)
	m := open(t, assistantModel(t))
	m.mode = modeBoard
	frames := func(m Model) int {
		m.frame, m.snapBusy, m.prefs.HidePreview = 1, true, true
		_, cmd := update(m, tickMsg(time.Now()))
		n := 0
		for _, msg := range runCmds(cmd) {
			if _, ok := msg.(assistantFrameMsg); ok {
				n++
			}
		}
		return n
	}
	if n := frames(m); n != 1 {
		t.Errorf("an unfocused running panel captured %d times per board tick, want 1", n)
	}
	focused := m
	focused.mode = modeAssistant
	if n := frames(focused); n != 0 {
		t.Errorf("the focused panel is captured by its own loop, not the board's: %d", n)
	}
	stopped := m
	stopped.assist.session = sessionStopped
	if n := frames(stopped); n != 0 {
		t.Errorf("a stopped panel was captured %d times", n)
	}
	if f.captures == 0 {
		t.Error("the fake was never called")
	}
}

func TestCaptureSizesTheWindowToThePanel(t *testing.T) {
	f := fakePanel(t)
	m := open(t, assistantModel(t))
	l := m.layout()
	runCmds(m.captureAssistantCmd())
	if f.capturedName != assistant.SessionName(m.eng.Root) || f.capturedW != l.assistantW-2 || f.capturedH != l.bodyH-2 || f.capturedBack != 0 {
		t.Errorf("captured %q at %dx%d with %d scrollback", f.capturedName, f.capturedW, f.capturedH, f.capturedBack)
	}
	m.assist.scroll = 5
	runCmds(m.captureAssistantCmd())
	if want := 5 + 2*(l.bodyH-2); f.capturedBack != want {
		t.Errorf("scrollback = %d, want %d", f.capturedBack, want)
	}
}

func TestFrameStateFollowsTheSession(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, m.stamped(assistantFrameMsg{txt: "x", history: 30, mouse: true, at: time.Now()}))
	if !m.assist.mouse || m.assist.txt != "x" {
		t.Fatalf("frame not stored: %+v", m.assist)
	}
	m.assist.scroll = 7
	m, _ = update(m, key("a"))
	if m.assist.scroll != 0 {
		t.Errorf("typing left the panel %d lines up", m.assist.scroll)
	}
	m.assist.scroll = 4
	if !strings.Contains(m.View(), "4 lines up") {
		t.Error("a scrolled panel doesn't say so")
	}
	m.mode = modeBoard
	m.assist.scroll = 4
	if !strings.Contains(m.View(), "4 lines up") {
		t.Error("a scrolled, unfocused panel doesn't say so")
	}
	m, _ = update(m, key(newAskKey))
	if m.assist.txt != "" || m.assist.history != 0 || m.assist.scroll != 0 || m.assist.mouse {
		t.Errorf("a new conversation kept the old screen: %+v", m.assist)
	}
}

func TestATransientReadErrorKeepsThePanelRunning(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m, _ = update(m, m.stamped(assistantFrameMsg{txt: "kept", err: errors.New("tmux hiccup"), at: time.Now()}))
	if !m.assist.running() || !strings.Contains(m.flash, "tmux hiccup") {
		t.Errorf("session %v, flash %q", m.assist.session, m.flash)
	}
}

func TestOpeningBeforeTheBoardLoadsFailsWithoutStarting(t *testing.T) {
	f := fakePanel(t)
	m := assistantModel(t)
	m.snap.Board = nil
	cmd := m.openAssistant(false)
	for _, msg := range runCmds(cmd) {
		m, _ = update(m, msg)
	}
	if f.starts != 0 || m.assist.running() || !strings.Contains(m.flash, "isn't loaded yet") {
		t.Errorf("starts %d, session %v, flash %q", f.starts, m.assist.session, m.flash)
	}
}

func TestQuittingAfterANewConversationStopsIt(t *testing.T) {
	fakePanel(t)
	var stops int
	orig := stopAssistant
	stopAssistant = func(string) error { stops++; return nil }
	t.Cleanup(func() { stopAssistant = orig })
	m := assistantModel(t)
	m, cmd := update(m, key(newAskKey))
	for _, msg := range runCmds(cmd) {
		m, _ = update(m, msg)
	}
	m.StopAssistant()
	if stops != 1 {
		t.Errorf("stops = %d after N", stops)
	}
	m, _ = update(m, assistantStartedMsg{epoch: m.assist.epoch, created: false})
	m.StopAssistant()
	if stops != 2 {
		t.Errorf("a later attach made the board forget it owns the session: stops = %d", stops)
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

	m.assist.mouse, m.assist.history = false, 100
	next, _ = update(m, tea.MouseMsg{X: l.boardW + 3, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if _, ok := sentJob(next); ok || next.assist.scroll != 3 {
		t.Errorf("a claude that ignores the mouse gets no report, the panel scrolls: scroll %d", next.assist.scroll)
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
	m.mode = modeBoard
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
	m, _ = update(m, assistantStartedMsg{epoch: first, created: true})
	m.mode = modeBoard
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
	if !m.assist.running() {
		t.Error("the current start should finish")
	}
}

func TestAttachingToAnotherBoardsSessionLeavesItRunningOnQuit(t *testing.T) {
	f := fakePanel(t)
	f.attaches = true
	var stopped int
	orig := stopAssistant
	t.Cleanup(func() { stopAssistant = orig })
	stopAssistant = func(string) error { stopped++; return nil }

	m := assistantModel(t)
	m, cmd := update(m, key(askKey))
	_ = m.StopAssistant()
	if stopped != 1 {
		t.Fatalf("stopped %d times; a start in progress must be stopped, or quitting leaves its session behind", stopped)
	}
	for _, msg := range runCmds(cmd) {
		m, _ = update(m, msg)
	}
	_ = m.StopAssistant()
	if stopped != 1 || !m.assist.running() {
		t.Errorf("stopped %d times, running %v; the board only attached to another board's session", stopped, m.assist.running())
	}
}

func TestNewConversationWaitsForAStartInProgress(t *testing.T) {
	f := fakePanel(t)
	m := assistantModel(t)
	m, _ = update(m, key(askKey))
	m.mode = modeBoard
	epoch := m.assist.epoch
	m, cmd := update(m, key(newAskKey))
	runCmds(cmd)
	if m.assist.epoch != epoch || f.restarts != 0 || !strings.Contains(m.flash, "still starting") {
		t.Errorf("epoch %d (was %d), restarts %d, flash %q; N during a start must not start a second one", m.assist.epoch, epoch, f.restarts, m.flash)
	}
}

func TestFailedStartIsShownInThePanelAndWheelDoesNotRevive(t *testing.T) {
	f := fakePanel(t)
	f.err = errors.New("tmux broke")
	m := assistantModel(t)
	m, _ = update(m, key(askKey))
	m, _ = update(m, assistantStartedMsg{epoch: m.assist.epoch, err: f.err})
	if !strings.Contains(m.View(), "tmux broke") {
		t.Errorf("the failure only flashed:\n%s", m.View())
	}
	l := m.layout()
	m, cmd := update(m, tea.MouseMsg{X: l.boardW + 3, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if cmd != nil || m.assist.running() {
		t.Error("scrolling a stopped panel started captures")
	}
	m, _ = update(m, m.stamped(assistantFrameMsg{txt: "late", at: time.Now()}))
	if m.assist.running() || m.assist.txt != "" {
		t.Error("a frame revived a stopped panel")
	}
}

func TestCtrlCAndQInThePanelGoToClaude(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	for _, c := range []struct {
		key  tea.KeyMsg
		want string
	}{
		{tea.KeyMsg{Type: tea.KeyCtrlC}, "C-c"},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}, "q"},
	} {
		next, cmd := update(m, c.key)
		job, ok := sentJob(next)
		if !ok || (job.key != c.want && job.literal != c.want) || next.mode != modeAssistant {
			t.Errorf("%v: job %+v, mode %v", c.key, job, next.mode)
		}
		for _, msg := range runCmds(cmd) {
			if _, quit := msg.(tea.QuitMsg); quit {
				t.Errorf("%v quit the board", c.key)
			}
		}
	}
}

func TestViewFitsTheTerminalWithThePanelOpen(t *testing.T) {
	fakePanel(t)
	for _, width := range []int{100, 101, 133, 200} {
		for _, height := range []int{8, 14, 30} {
			for _, hidePreview := range []bool{false, true} {
				m := assistantModel(t)
				m.width, m.height = width, height
				m.prefs.HidePreview = hidePreview
				m = open(t, m)
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) != height {
					t.Errorf("%dx%d preview hidden %v: %d lines", width, height, hidePreview, len(lines))
				}
				for i, line := range lines {
					if w := lipgloss.Width(line); w > width {
						t.Errorf("%dx%d line %d is %d wide", width, height, i, w)
					}
				}
				if body := ansi.Strip(lines[headerHeight]); !strings.HasPrefix(string([]rune(body)[m.layout().boardW:]), "╭") {
					t.Errorf("%dx%d: the panel doesn't start at column %d: %q", width, height, m.layout().boardW, body)
				}
			}
		}
	}
}

func TestPanelSizeLimits(t *testing.T) {
	fakePanel(t)
	for _, c := range []struct {
		width, height int
		opens         bool
	}{{100, 30, true}, {99, 30, false}, {120, 8, true}, {120, 7, false}, {300, 30, true}} {
		m := assistantModel(t)
		m.width, m.height = c.width, c.height
		m, _ = update(m, key(askKey))
		if m.assist.open != c.opens {
			t.Errorf("%dx%d: open = %v", c.width, c.height, m.assist.open)
		}
		if c.opens {
			l := m.layout()
			if l.assistantW < minAssistantWidth || l.boardW < minBoardWidth || l.assistantW+l.boardW != c.width {
				t.Errorf("%dx%d: board %d, panel %d", c.width, c.height, l.boardW, l.assistantW)
			}
		}
	}
}

func TestHitTestAssistantBoundaries(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	l := m.layout()
	for _, c := range []struct {
		name string
		x, y int
		want hitKind
	}{
		{"right edge", m.width - 1, l.bodyY + 1, hitAssistant},
		{"header row", l.boardW + 1, l.bodyY - 1, hitNone},
		{"footer row", l.boardW + 1, l.bodyY + l.bodyH, hitNone},
		{"beside the preview's top edge", l.boardW + 1, l.previewY, hitAssistant},
		{"the preview's top edge", l.boardW - 1, l.previewY, hitPreviewEdge},
	} {
		if got := l.hitTest(m, c.x, c.y).kind; got != c.want {
			t.Errorf("%s: hit %v, want %v", c.name, got, c.want)
		}
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if got := m.layout().hitTest(m, m.width-1, l.bodyY+1).kind; got == hitAssistant {
		t.Error("a hidden panel still takes clicks")
	}
}

func TestPaletteAssistantCommandsEachDoTheirOwnThing(t *testing.T) {
	run := func(t *testing.T, m Model, name string) (Model, tea.Cmd) {
		t.Helper()
		for _, c := range paletteCommands {
			if c.name == name {
				next, cmd := m.updateBoard(keyPress(c.key))
				return next.(Model), cmd
			}
		}
		t.Fatalf("no palette command %q", name)
		return m, nil
	}
	f := fakePanel(t)
	m := open(t, assistantModel(t))
	m.mode = modeBoard

	m, _ = run(t, m, "Hide the assistant panel")
	if m.assist.open {
		t.Error("hide left the panel open")
	}
	m, cmd := run(t, m, "Start a new assistant conversation")
	runCmds(cmd)
	if !m.assist.open || f.restarts != 1 {
		t.Errorf("open %v, restarts %d", m.assist.open, f.restarts)
	}
	m, _ = update(m, assistantStartedMsg{epoch: m.assist.epoch, created: true})
	m.mode = modeBoard
	m, cmd = run(t, m, "Open the assistant panel and type into it")
	runCmds(cmd)
	if m.mode != modeAssistant || f.restarts != 1 {
		t.Errorf("mode %v, restarts %d; opening again must not start over", m.mode, f.restarts)
	}
}

func TestStartingPanelSwallowsKeysAndSaysSo(t *testing.T) {
	f := fakePanel(t)
	m := assistantModel(t)
	m, _ = update(m, key(askKey))
	if !strings.Contains(m.View(), "Starting the assistant") {
		t.Error("the panel doesn't say it is starting")
	}
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEnter}, key("x")} {
		next, cmd := update(m, k)
		if cmd != nil || next.assist.epoch != m.assist.epoch {
			t.Errorf("%v started something while starting", k)
		}
		if _, ok := sentJob(next); ok {
			t.Errorf("%v reached a session that isn't up yet", k)
		}
	}
	if f.starts != 0 {
		t.Errorf("starts = %d before the start command ran", f.starts)
	}
}

func TestWheelOverTheBoardLeavesItAloneWhileTypingToClaude(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	l := m.layout()
	col, sel := m.col, maps.Clone(m.selected)
	m, cmd := update(m, tea.MouseMsg{X: 2, Y: l.bodyY + columnHeaderHeight + 1, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if m.mode != modeAssistant || m.col != col || !maps.Equal(m.selected, sel) || cmd != nil {
		t.Errorf("mode %v, col %d, selected %v", m.mode, m.col, m.selected)
	}
	if _, ok := sentJob(m); ok {
		t.Error("the wheel over the board reached claude")
	}
}

func TestColumnsStayLeftOfThePanelWhenResized(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m.mode = modeBoard
	for range 80 {
		m, _ = update(m, key(">"))
	}
	l := m.layout()
	if sum(func() []int { _, w := m.columnWindow(); return w }()) != l.boardW {
		t.Errorf("columns no longer fit the board's %d columns", l.boardW)
	}
	for _, w := range m.prefs.ColumnWidths {
		if w > l.boardW {
			t.Errorf("column width %d is wider than the board's %d", w, l.boardW)
		}
	}
}

func TestVanishedSessionIsReportedWithItsCauseAndNotOwned(t *testing.T) {
	_, _, _, err := captureAssistant("drover-test-no-such-session", 10, 5, 0)
	if !errors.Is(err, errSessionEnded) {
		t.Fatalf("capturing a missing session = %v", err)
	}

	fakePanel(t)
	m := open(t, assistantModel(t))
	if !m.assist.owns {
		t.Fatal("the board that created the session doesn't own it")
	}
	m, _ = update(m, m.stamped(assistantFrameMsg{err: err, at: time.Now()}))
	if m.assist.owns || m.assist.running() {
		t.Errorf("a session that ended is still owned: %+v", m.assist)
	}
}

func TestTypingSnapsAScrolledPanelBackToTheBottom(t *testing.T) {
	f := fakePanel(t)
	m := open(t, assistantModel(t))
	m.assist.session, m.assist.history, m.assist.scroll = sessionRunning, 50, 7
	m, cmd := update(m, key("x"))
	if m.assist.scroll != 0 {
		t.Errorf("scroll = %d after typing", m.assist.scroll)
	}
	runCmds(cmd)
	if f.capturedBack != 0 {
		t.Errorf("captured %d lines of scrollback after typing", f.capturedBack)
	}
}
func TestHiddenOrStoppedPanelIsNotCaptured(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m Model) Model
	}{
		{"hidden", func(m Model) Model {
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
			if m.assist.open {
				t.Fatal("ctrl+q should hide the panel")
			}
			return m
		}},
		{"stopped", func(m Model) Model {
			m.mode = modeBoard
			m.assist.session = sessionStopped
			return m
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fakePanel(t)
			m := tt.setup(open(t, assistantModel(t)))
			m.frame, m.snapBusy, m.prefs.HidePreview = 1, true, true
			if m.assistantCapturing() && m.mode != modeAssistant {
				t.Error("the panel is still polled")
			}
			_, cmd := update(m, tickMsg(time.Now()))
			if cmd == nil {
				t.Fatal("the board should keep ticking")
			}
			before := f.captures
			runCmds(cmd)
			if f.captures != before {
				t.Errorf("the panel was captured %d times", f.captures-before)
			}
		})
	}
}

func TestFocusedCaptureLoopSurvivesTheStartingPhase(t *testing.T) {
	f := fakePanel(t)
	m := assistantModel(t)
	m, _ = update(m, key(askKey))
	gen := m.assist.tickGen

	_, cmd := update(m, assistantTickMsg{gen: gen})
	if cmd == nil {
		t.Fatal("the loop ended while the assistant was starting")
	}
	runCmds(cmd)
	if f.captures != 0 {
		t.Errorf("captured %d times before the session existed", f.captures)
	}
	m, _ = update(m, assistantStartedMsg{epoch: m.assist.epoch, created: true})
	_, cmd = update(m, assistantTickMsg{gen: gen})
	runCmds(cmd)
	if f.captures != 1 {
		t.Errorf("captures = %d after the start finished, want 1", f.captures)
	}
}
func TestWheelUpStopsAtTheTopOfTheHistory(t *testing.T) {
	fakePanel(t)
	m := open(t, assistantModel(t))
	m.assist.history = 4
	l := m.layout()
	for range 2 {
		m, _ = update(m, tea.MouseMsg{X: l.boardW + 3, Y: l.bodyY + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	}
	if m.assist.scroll != 4 {
		t.Errorf("scroll = %d with 4 lines of history", m.assist.scroll)
	}
}

func TestDroppingACardOnAColumnStillMovesItBesideThePanel(t *testing.T) {
	fakePanel(t)
	for _, panel := range []bool{false, true} {
		m := assistantModel(t)
		if panel {
			m = open(t, m)
			m.mode = modeBoard
		}
		l := m.layout()
		x, y := l.cols[1].x+2, l.bodyY+columnHeaderHeight+1
		m.drag = dragState{kind: dragCard, task: m.snap.Tasks[0].ID, fromCol: 0, x: x, y: y}
		m, cmd := update(m, tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease})
		if cmd == nil || m.col != 1 || m.drag.kind != dragNone {
			t.Errorf("panel %v: drop on a column: cmd nil %v, col %d, drag %v", panel, cmd == nil, m.col, m.drag.kind)
		}
	}
}
