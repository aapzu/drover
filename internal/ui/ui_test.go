package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

// realModel is a board backed by a real engine in a scratch directory, with a
// card in the first column per title.
func realModel(t *testing.T, titles ...string) Model {
	t.Helper()
	eng, err := engine.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range titles {
		eng.CreateTask(title, "")
	}
	m := New(eng)
	m, _ = update(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.flash = ""
	return m
}

// withLive marks the card's agent as running, as a snapshot would.
func withLive(m Model, id string) Model {
	m.snap.Live = map[string]string{id: "sess-" + id}
	return m
}

// liveCard is realModel with the first card's agent running.
func liveCard(t *testing.T, titles ...string) Model {
	t.Helper()
	m := realModel(t, titles...)
	return withLive(m, m.current().ID)
}

func ctrl(k tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: k} }

func TestFormEditsTheSelectedCard(t *testing.T) {
	m := realModel(t, "old")
	m, _ = update(m, key("e"))
	if m.mode != modeForm || m.titleInput.Value() != "old" || m.formFocus != 0 {
		t.Fatalf("mode %v title %q focus %d", m.mode, m.titleInput.Value(), m.formFocus)
	}

	m, _ = update(m, key("er"))
	m, _ = update(m, ctrl(tea.KeyEnter))
	if m.formFocus != 1 {
		t.Fatalf("enter in the title left focus on %d, want the prompt", m.formFocus)
	}
	m, _ = update(m, key("do it"))
	m, _ = update(m, ctrl(tea.KeyEnter))
	if got := m.promptInput.Value(); got != "do it\n" {
		t.Errorf("enter in the prompt should add a line, got %q", got)
	}
	m, _ = update(m, ctrl(tea.KeyTab))
	if m.formFocus != 0 {
		t.Errorf("tab left focus on %d, want the title", m.formFocus)
	}
	m, _ = update(m, ctrl(tea.KeyShiftTab))
	if m.formFocus != 1 {
		t.Errorf("shift+tab left focus on %d, want the prompt", m.formFocus)
	}

	m, _ = update(m, ctrl(tea.KeyCtrlS))
	got := m.current()
	if m.mode != modeBoard || got.Title != "older" || got.Prompt != "do it" {
		t.Fatalf("mode %v title %q prompt %q", m.mode, got.Title, got.Prompt)
	}
}

func TestFormSaveNeedsATitleAndTrimsInput(t *testing.T) {
	m := realModel(t, "old")
	m, _ = update(m, key("e"))
	m.titleInput.SetValue("  ")
	m, _ = update(m, ctrl(tea.KeyCtrlS))
	if m.mode != modeForm || m.flash != "A task needs a title" {
		t.Fatalf("mode %v flash %q", m.mode, m.flash)
	}

	m.titleInput.SetValue("  new title ")
	m.promptInput.SetValue("\n  the prompt \n")
	m, _ = update(m, ctrl(tea.KeyCtrlS))
	got := m.current()
	if m.mode != modeBoard || got.Title != "new title" || got.Prompt != "the prompt" {
		t.Errorf("mode %v title %q prompt %q", m.mode, got.Title, got.Prompt)
	}
}

func TestFormEscDiscardsEdits(t *testing.T) {
	m := realModel(t, "old")
	m, _ = update(m, key("e"))
	m, _ = update(m, key("x"))
	m, _ = update(m, ctrl(tea.KeyEsc))
	if m.mode != modeBoard || m.current().Title != "old" {
		t.Errorf("mode %v title %q", m.mode, m.current().Title)
	}
}

func TestSendDeliversTheMessageToTheCardsAgent(t *testing.T) {
	m := realModel(t, "a")
	m, cmd := update(m, key("s"))
	if m.mode != modeBoard || cmd != nil || m.flash != "No running agent for this task" {
		t.Fatalf("without an agent: mode %v flash %q", m.mode, m.flash)
	}

	m = withLive(m, m.current().ID)
	m, _ = update(m, key("s"))
	if m.mode != modeSend || m.modalTask != m.current().ID {
		t.Fatalf("mode %v modal task %q", m.mode, m.modalTask)
	}
	m, _ = update(m, key("hello"))
	if got := m.sendInput.Value(); got != "hello" {
		t.Fatalf("typed %q", got)
	}
	m, cmd = update(m, ctrl(tea.KeyEnter))
	if m.mode != modeBoard || m.flash != "Sent to #1" || cmd == nil {
		t.Fatalf("mode %v flash %q cmd nil %v", m.mode, m.flash, cmd == nil)
	}
	if o := m.busy[m.current().ID]; o.label != "sending" {
		t.Errorf("busy = %+v", m.busy)
	}

	// The engine has no running agent for the card, so the send comes back as an error.
	for _, msg := range runCmds(cmd) {
		m, _ = update(m, msg)
	}
	if m.flash != "Error: no running agent for this task" || len(m.busy) != 0 {
		t.Errorf("flash %q busy %v", m.flash, m.busy)
	}
}

func TestSendIgnoresEmptyTextAndEscCancels(t *testing.T) {
	m := liveCard(t, "a")
	m, _ = update(m, key("s"))
	m, _ = update(m, key("  "))
	m, cmd := update(m, ctrl(tea.KeyEnter))
	if m.mode != modeBoard || cmd != nil || m.flash != "" {
		t.Errorf("empty send: mode %v cmd nil %v flash %q", m.mode, cmd == nil, m.flash)
	}

	m, _ = update(m, key("s"))
	m, _ = update(m, key("never"))
	m, cmd = update(m, ctrl(tea.KeyEsc))
	if m.mode != modeBoard || cmd != nil || len(m.busy) != 0 {
		t.Errorf("esc: mode %v cmd nil %v busy %v", m.mode, cmd == nil, m.busy)
	}
}

func TestSendForACardThatVanishedDoesNothing(t *testing.T) {
	m := liveCard(t, "a")
	m, _ = update(m, key("s"))
	m, _ = update(m, key("hi"))
	m.modalTask = "gone"
	m, cmd := update(m, ctrl(tea.KeyEnter))
	if m.mode != modeBoard || cmd != nil || m.flash != "" {
		t.Errorf("mode %v cmd nil %v flash %q", m.mode, cmd == nil, m.flash)
	}
}

func TestDetailShowsTheCardAndScrolls(t *testing.T) {
	m := realModel(t, "inspect me")
	m, _ = update(m, key("i"))
	if m.mode != modeDetail || !strings.Contains(m.detail.View(), "#1 inspect me") {
		t.Fatalf("mode %v detail:\n%s", m.mode, m.detail.View())
	}
	if !strings.Contains(m.View(), "inspect me") {
		t.Error("View doesn't show the detail")
	}

	m, _ = update(m, ctrl(tea.KeyDown))
	if m.mode != modeDetail {
		t.Fatalf("a scroll key left the detail view: mode %v", m.mode)
	}
	for _, k := range []tea.KeyMsg{ctrl(tea.KeyEsc), key("q"), key("i")} {
		next, _ := update(m, k)
		if next.mode != modeBoard {
			t.Errorf("%q: mode = %v, want the board", k.String(), next.mode)
		}
	}
}

func TestConfirmRunsTheActionOnlyForY(t *testing.T) {
	for _, k := range []string{"y", "Y"} {
		m := realModel(t, "a", "b")
		m, _ = update(m, key("x"))
		if m.mode != modeConfirm || !strings.Contains(m.confirmText, `Delete #1 "a"`) {
			t.Fatalf("mode %v text %q", m.mode, m.confirmText)
		}
		m, cmd := update(m, key(k))
		if m.mode != modeBoard || cmd == nil {
			t.Fatalf("%s: mode %v cmd nil %v", k, m.mode, cmd == nil)
		}
		for _, msg := range runCmds(cmd) {
			m, _ = update(m, msg)
		}
		if got := m.tasksForColumn(0); len(got) != 1 || got[0].Title != "b" {
			t.Errorf("%s: cards left %v", k, got)
		}
	}

	for _, k := range []tea.KeyMsg{key("N"), key("n"), ctrl(tea.KeyEnter), ctrl(tea.KeyEsc)} {
		m := realModel(t, "a")
		m, _ = update(m, key("x"))
		m, cmd := update(m, k)
		if m.mode != modeBoard || cmd != nil || len(m.tasksForColumn(0)) != 1 {
			t.Errorf("%q: mode %v cmd nil %v cards %d", k.String(), m.mode, cmd == nil, len(m.tasksForColumn(0)))
		}
	}
}

func TestDeletingACardInAGroupSaysTheAgentKeepsWorking(t *testing.T) {
	m := realModel(t, "a", "b")
	m.snap.Tasks[1].GroupID = m.snap.Tasks[0].ID
	m, _ = update(m, key("x"))
	if !strings.Contains(m.confirmText, "Only this card goes") {
		t.Errorf("confirm text = %q", m.confirmText)
	}
}

func TestHelpClosesOnAnyKey(t *testing.T) {
	for _, k := range []tea.KeyMsg{key("x"), ctrl(tea.KeyEnter), ctrl(tea.KeyEsc), key("?")} {
		m := realModel(t, "a")
		m, _ = update(m, key("?"))
		if m.mode != modeHelp || !strings.Contains(m.View(), "search and run a command") {
			t.Fatalf("? did not open the help")
		}
		m, cmd := update(m, k)
		if m.mode != modeBoard || cmd != nil {
			t.Errorf("%q: mode %v cmd nil %v", k.String(), m.mode, cmd == nil)
		}
	}
}

// fits fails the test if the view is taller or wider than the terminal.
func fits(t *testing.T, name, view string, width, height int) {
	t.Helper()
	if h := lipgloss.Height(view); h > height {
		t.Errorf("%s: view is %d rows, terminal has %d", name, h, height)
	}
	if w := lipgloss.Width(view); w > width {
		t.Errorf("%s: view is %d columns, terminal has %d", name, w, width)
	}
}

func TestEveryModeRendersWithinTheTerminal(t *testing.T) {
	modes := []struct {
		name  string
		mode  mode
		setup func(Model) Model
		want  string
	}{
		{"board", modeBoard, nil, "Planning"},
		{"form", modeForm, func(m Model) Model { m.openForm(m.current()); return m }, "Edit task"},
		{"send", modeSend, func(m Model) Model { m.modalTask = m.current().ID; m.sendInput.Focus(); return m }, "Send ›"},
		{"confirm", modeConfirm, func(m Model) Model { m.confirmText = "Really? (y/N)"; return m }, "Really?"},
		{"move", modeMove, func(m Model) Model { m.modalTask = m.current().ID; return m }, "Move #1 to…"},
		{"palette", modePalette, func(m Model) Model { m.openPalette(); return m }, "Commands"},
		{"filter", modeFilter, func(m Model) Model { m.filterInput.Focus(); return m }, "Filter ›"},
		{"import", modeImport, func(m Model) Model { m.imp.items = nil; return m }, "Claude sessions"},
		{"config", modeConfig, func(m Model) Model { m.configSrc = configSources(m.eng.Root); return m }, ""},
		{"detail", modeDetail, func(m Model) Model { m.detail.SetContent(m.renderDetail(m.current())); return m }, "#1 first"},
		{"live", modeLive, func(m Model) Model { return withLive(m, m.current().ID) }, "LIVE"},
	}
	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		for _, tt := range modes {
			if tt.name == "config" && size[1] < 30 {
				continue
			}
			t.Run(fmt.Sprintf("%s %dx%d", tt.name, size[0], size[1]), func(t *testing.T) {
				m := realModel(t, "first", "second")
				m, _ = update(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				if tt.setup != nil {
					m = tt.setup(m)
				}
				m.mode = tt.mode
				view := m.View()
				if !strings.Contains(view, tt.want) {
					t.Errorf("view misses %q:\n%s", tt.want, view)
				}
				fits(t, tt.name, view, size[0], size[1])
			})
		}
	}
}

func TestHelpRendersBothLayouts(t *testing.T) {
	m := realModel(t, "a")
	m.mode = modeHelp
	for _, width := range []int{220, 60} {
		m.width = width
		help := m.renderHelp()
		if !strings.Contains(help, "Live mode") || !strings.Contains(help, "Keys") {
			t.Errorf("width %d: help misses a section:\n%s", width, help)
		}
	}
	m.width = 220
	wide := lipgloss.Height(m.renderHelp())
	m.width = 60
	if narrow := lipgloss.Height(m.renderHelp()); narrow <= wide {
		t.Errorf("a narrow terminal should stack the sections: %d rows vs %d", narrow, wide)
	}
}

func TestViewIsBlankBeforeTheFirstResize(t *testing.T) {
	if got := (Model{}).View(); got != "" {
		t.Errorf("View = %q", got)
	}
}

func TestHeaderCountsAgentsByState(t *testing.T) {
	m := realModel(t, "a", "b", "c", "d", "e")
	m.frame = 1
	session := func(status store.SessionStatus, activity store.Activity) map[string]*store.Session {
		return map[string]*store.Session{store.MainSession: {Status: status, Activity: activity, UpdatedAt: time.Now(), Usage: claude.Usage{Cost: 0.5}}}
	}
	m.snap.Tasks[0].Sessions = session(store.StatusRunning, store.ActivityThinking)
	m.snap.Tasks[1].Sessions = session(store.StatusRunning, store.ActivityIdle)
	m.snap.Tasks[2].Sessions = session(store.StatusRunning, store.ActivityPermission)
	m.snap.Tasks[3].Sessions = session(store.StatusQueued, store.ActivityIdle)
	m.snap.Running, m.snap.MaxConcurrent = 3, 4
	m.filter = "zz"

	got := ansi.Strip(m.renderHeader())
	for _, want := range []string{"1 working", "1 idle", "1 need you", "1 queued · 3/4 slots", "filter: zz", "$2.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("header misses %q: %q", want, got)
		}
	}
	m.branch = "main"
	if got := ansi.Strip(m.renderHeader()); !strings.Contains(got, "on main") {
		t.Errorf("header misses the branch: %q", got)
	}
}

func TestMouseOnlyReachesTheBoardModes(t *testing.T) {
	m := testModel(120, 40, 2, 2)
	m.mode = modeHelp
	m.drag = dragState{kind: dragCard}
	next, cmd := m.Update(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if got := next.(Model); got.drag.kind != dragNone || cmd != nil {
		t.Errorf("drag %v cmd nil %v", got.drag.kind, cmd == nil)
	}
}

func TestSessionStartedSelectsAndAttachesTheNewCard(t *testing.T) {
	m := realModel(t, "a", "b")
	card := m.snap.Tasks[1]
	snap := m.snap
	snap.At = time.Now()
	snap.Live = map[string]string{card.ID: "sess"}

	m.setFlash("Starting a new session…")
	m, cmd := update(m, sessionStartedMsg{task: card, snap: snap})
	if m.current().ID != card.ID || m.attached != card.ID || cmd == nil || m.flash != "" {
		t.Errorf("current %s attached %q cmd nil %v flash %q", m.current().ID, m.attached, cmd == nil, m.flash)
	}
}

func TestSessionStartedWithoutALiveAgentOnlySelects(t *testing.T) {
	m := realModel(t, "a", "b")
	card := m.snap.Tasks[1]
	snap := m.snap
	snap.At = time.Now()

	m, cmd := update(m, sessionStartedMsg{task: card, snap: snap})
	if m.current().ID != card.ID || m.attached != "" || cmd != nil {
		t.Errorf("current %s attached %q cmd nil %v", m.current().ID, m.attached, cmd == nil)
	}
}

func TestSessionStartedErrorIsFlashed(t *testing.T) {
	m := realModel(t, "a")
	m, cmd := update(m, sessionStartedMsg{task: m.snap.Tasks[0], err: errors.New("no slot")})
	if m.flash != "Error: no slot" || cmd != nil {
		t.Errorf("flash %q cmd nil %v", m.flash, cmd == nil)
	}
}

func TestSessionStartedWithoutACardFlashesTheError(t *testing.T) {
	m := realModel(t, "a")
	m, cmd := update(m, sessionStartedMsg{err: errors.New("first column runs no agent")})
	if m.flash != "Error: first column runs no agent" || cmd != nil {
		t.Errorf("flash %q cmd nil %v", m.flash, cmd == nil)
	}
}

func TestAttachDoneReturnsToTheBoard(t *testing.T) {
	m := realModel(t, "", "named")
	m.attached = m.snap.Tasks[0].ID

	m, cmd := update(m, attachDoneMsg{id: m.attached, agent: true, err: errors.New("lost the pane")})
	if m.attached != "" || m.flash != "Attach failed: lost the pane" {
		t.Errorf("attached %q flash %q", m.attached, m.flash)
	}
	if o := m.busy[m.snap.Tasks[0].ID]; o.label != "discarding" {
		t.Errorf("an untitled card should be discarded, busy = %v", m.busy)
	}
	if cmd == nil {
		t.Error("attach done should turn the mouse back on and discard the card")
	}

	m = realModel(t, "named")
	_, cmd = update(m, attachDoneMsg{id: m.snap.Tasks[0].ID, agent: true})
	if msgs := runCmds(cmd); len(msgs) != 1 {
		t.Errorf("a titled card is kept, but got %d msgs", len(msgs))
	}
}

func TestAttachDoneLeavesTheMouseAloneWhenItIsOff(t *testing.T) {
	m := realModel(t, "named")
	m.prefs.NoMouse = true
	if _, cmd := update(m, attachDoneMsg{id: m.snap.Tasks[0].ID}); len(runCmds(cmd)) != 0 {
		t.Error("attach done enabled the mouse that is off")
	}
}

func TestTakeOverDone(t *testing.T) {
	m := realModel(t, "a")
	card := m.snap.Tasks[0]
	snap := m.snap
	snap.At = time.Now()

	failed, cmd := update(m, takeOverDoneMsg{taskID: card.ID, err: errors.New("denied"), snap: snap})
	if failed.flash != "Error: denied" || cmd != nil {
		t.Errorf("flash %q cmd nil %v", failed.flash, cmd == nil)
	}

	stopped, cmd := update(m, takeOverDoneMsg{taskID: card.ID, snap: snap})
	if !strings.Contains(stopped.flash, "Stopped the other terminal's agent") || cmd != nil {
		t.Errorf("flash %q cmd nil %v", stopped.flash, cmd == nil)
	}

	snap.Live = map[string]string{card.ID: "sess"}
	attached, cmd := update(m, takeOverDoneMsg{taskID: card.ID, snap: snap})
	if attached.attached != card.ID || cmd == nil {
		t.Errorf("attached %q cmd nil %v", attached.attached, cmd == nil)
	}
}

func TestExternalAttachAsksBeforeTakingOver(t *testing.T) {
	m := realModel(t, "a")
	card := m.snap.Tasks[0]

	m, cmd := update(m, externalAttachMsg{id: card.ID, number: 1, pid: 4242})
	if m.mode != modeConfirm || cmd != nil || !strings.Contains(m.confirmText, "#1 runs in another terminal (pid 4242)") {
		t.Fatalf("mode %v cmd nil %v text %q", m.mode, cmd == nil, m.confirmText)
	}
	m, cmd = update(m, key("y"))
	msgs := runCmds(cmd)
	if _, ok := msgs[0].(takeOverDoneMsg); !ok || len(msgs) != 1 {
		t.Errorf("y should take over, got %T", msgs)
	}
}

func TestExternalAttachWithACommandRunsIt(t *testing.T) {
	m := realModel(t, "a")
	_, cmd := update(m, externalAttachMsg{cmd: exec.Command("true"), id: m.snap.Tasks[0].ID})
	if cmd == nil {
		t.Error("an attachable external agent started nothing")
	}
}

func TestSuspendAllQuits(t *testing.T) {
	m, cmd := update(realModel(t, "a"), suspendAllMsg{})
	if !m.SuspendAllRequested() {
		t.Error("suspend all was not recorded")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd returned %T, want tea.QuitMsg", cmd())
	}
	if realModel(t).SuspendAllRequested() {
		t.Error("a fresh board asks to suspend")
	}
}

func TestNoticeIsFlashedAndListenedForAgain(t *testing.T) {
	m, cmd := update(realModel(t, "a"), noticeMsg("tmux went away"))
	if m.flash != "tmux went away" || cmd == nil {
		t.Errorf("flash %q cmd nil %v", m.flash, cmd == nil)
	}
}

func TestLiveTickKeepsTheLoopOnlyWhileLive(t *testing.T) {
	m := liveCard(t, "a")
	m.mode, m.liveGen = modeLive, 2

	if _, cmd := update(m, liveTickMsg{gen: 2}); cmd == nil {
		t.Error("the live loop stopped while live")
	}
	if _, cmd := update(m, liveTickMsg{gen: 1}); cmd != nil {
		t.Error("a tick from an earlier live session kept going")
	}
	board := m
	board.mode = modeBoard
	if _, cmd := update(board, liveTickMsg{gen: 2}); cmd != nil {
		t.Error("the loop kept going on the board")
	}

	m.snap.Live = nil
	m, cmd := update(m, liveTickMsg{gen: 2})
	if m.mode != modeBoard || cmd != nil || !strings.Contains(m.flash, "no longer running") {
		t.Errorf("mode %v cmd nil %v flash %q", m.mode, cmd == nil, m.flash)
	}
}

func TestActionDoneOffersConfirmationAndTakeOver(t *testing.T) {
	m := realModel(t, "a")
	card := m.snap.Tasks[0]
	ran := false
	confirm := func() tea.Cmd { ran = true; return nil }

	m, cmd := update(m, actionDoneMsg{id: card.ID, confirmText: "Sure? (y/N)", confirm: confirm})
	if m.mode != modeConfirm || m.confirmText != "Sure? (y/N)" || cmd != nil {
		t.Fatalf("mode %v text %q", m.mode, m.confirmText)
	}
	update(m, key("y"))
	if !ran {
		t.Error("y did not run the confirmed action")
	}

	m = realModel(t, "a")
	m, cmd = update(m, actionDoneMsg{id: card.ID, err: fmt.Errorf("start: %w", &engine.ExternalError{ID: card.ID, Number: 7, PID: 99})})
	if m.mode != modeConfirm || cmd != nil || !strings.Contains(m.confirmText, "#7 runs in another terminal (pid 99)") {
		t.Errorf("mode %v text %q", m.mode, m.confirmText)
	}
}

func TestActionDoneFlashesTheOutcome(t *testing.T) {
	tests := []struct {
		name string
		msg  actionDoneMsg
		want string
	}{
		{"error", actionDoneMsg{err: errors.New("boom")}, "Error: boom"},
		{"done", actionDoneMsg{done: "Did it"}, "Did it"},
		{"error beats done", actionDoneMsg{err: errors.New("boom"), done: "Did it"}, "Error: boom"},
		{"nothing to say", actionDoneMsg{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, cmd := update(realModel(t, "a"), tt.msg)
			if m.flash != tt.want || cmd != nil || m.mode != modeBoard {
				t.Errorf("flash %q cmd nil %v mode %v", m.flash, cmd == nil, m.mode)
			}
		})
	}
}

func TestActionDoneAttachesOnlyOnTheBoardAndOnSuccess(t *testing.T) {
	m := liveCard(t, "a")
	card := m.snap.Tasks[0]

	attached, cmd := update(m, actionDoneMsg{id: card.ID, attach: true})
	if attached.attached != card.ID || cmd == nil {
		t.Errorf("attached %q cmd nil %v", attached.attached, cmd == nil)
	}
	failed, cmd := update(m, actionDoneMsg{id: card.ID, attach: true, err: errors.New("no")})
	if failed.attached != "" || cmd != nil {
		t.Errorf("a failed start attached: %q", failed.attached)
	}
	m.mode = modeDetail
	away, cmd := update(m, actionDoneMsg{id: card.ID, attach: true})
	if away.attached != "" || cmd != nil {
		t.Errorf("attached while in %v", away.mode)
	}
	m.mode = modeBoard
	noAgent, cmd := update(withLive(m, "other"), actionDoneMsg{id: card.ID, attach: true})
	if noAgent.attached != "" || cmd != nil {
		t.Errorf("attached a card with no agent: %q", noAgent.attached)
	}
}

func TestTickExpiresTheFlashAfterSixSeconds(t *testing.T) {
	m := realModel(t, "a")
	m.setFlash("fresh")
	m, _ = update(m, tickMsg(time.Now()))
	if m.flash != "fresh" {
		t.Errorf("a fresh flash was cleared: %q", m.flash)
	}
	m.flashAt = time.Now().Add(-7 * time.Second)
	m, _ = update(m, tickMsg(time.Now()))
	if m.flash != "" {
		t.Errorf("an old flash stayed: %q", m.flash)
	}
	if m.frame != 2 {
		t.Errorf("frame = %d, want 2", m.frame)
	}
}

func TestTickReportsFailedKeySends(t *testing.T) {
	m := realModel(t, "a")
	m.keys.errs <- errors.New("no server")
	m, _ = update(m, tickMsg(time.Now()))
	if m.flash != "Sending input to the agent failed: no server" {
		t.Errorf("flash = %q", m.flash)
	}
	m.mode = modeAssistant
	m.keys.errs <- errors.New("no server")
	m, _ = update(m, tickMsg(time.Now()))
	if !strings.Contains(m.flash, "the assistant failed") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestTickAsksForOneSnapshotAtATime(t *testing.T) {
	m := realModel(t, "a")
	m, cmd := update(m, tickMsg(time.Now()))
	if !m.snapBusy || cmd == nil {
		t.Fatalf("snapBusy %v cmd nil %v", m.snapBusy, cmd == nil)
	}
	var snaps int
	for _, msg := range runCmds(m.snapshot()) {
		if _, ok := msg.(snapMsg); ok {
			snaps++
		}
	}
	if snaps != 1 {
		t.Errorf("snapshot cmd returned %d snapMsgs", snaps)
	}
	m, _ = update(m, snapMsg(m.eng.Snapshot()))
	if m.snapBusy {
		t.Error("a snapshot should free the next tick to ask for another")
	}
}

func TestTickFollowsABusyCardIntoItsTerminalWhenItsAgentIsUp(t *testing.T) {
	m := realModel(t, "a")
	card := m.snap.Tasks[0]
	m = withLive(m, card.ID)
	m.follow, m.followUntil = card.ID, time.Now().Add(time.Minute)

	m.snap.Busy = map[string]string{card.ID: "creating worktree"}
	m, _ = update(m, tickMsg(time.Now()))
	if m.follow != card.ID || m.attached != "" {
		t.Fatalf("follow %q attached %q while the card is busy", m.follow, m.attached)
	}

	m.snap.Busy = nil
	m, _ = update(m, tickMsg(time.Now()))
	if m.follow != "" || m.attached != card.ID {
		t.Errorf("follow %q attached %q once the card is free", m.follow, m.attached)
	}
}

func TestTickStopsFollowingAfterTheDeadline(t *testing.T) {
	m := realModel(t, "a")
	card := m.snap.Tasks[0]
	m.snap.Busy = map[string]string{card.ID: "creating worktree"}
	m.follow, m.followUntil = card.ID, time.Now().Add(-time.Second)
	m, _ = update(m, tickMsg(time.Now()))
	if m.follow != "" || m.attached != "" {
		t.Errorf("follow %q attached %q", m.follow, m.attached)
	}
}

func TestSyncFocusTellsTheEngineWhereYouAre(t *testing.T) {
	m := realModel(t, "a")
	card := m.snap.Tasks[0]
	m = withLive(m, card.ID)

	m.attached = card.ID
	m.syncFocus()
	if m.focused != card.ID {
		t.Errorf("attached: focused %q", m.focused)
	}
	m.attached, m.mode = "", modeLive
	m.syncFocus()
	if m.focused != card.ID {
		t.Errorf("live: focused %q", m.focused)
	}
	m.mode = modeBoard
	m.syncFocus()
	if m.focused != "" {
		t.Errorf("board: focused %q", m.focused)
	}
}

func TestInitEnablesTheMouseUnlessItIsOff(t *testing.T) {
	m := realModel(t, "a")
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init started nothing")
	}
	// New reads the layout saved for the repo.
	m.prefs.NoMouse = true
	if err := m.prefs.save(m.eng.Root); err != nil {
		t.Fatal(err)
	}
	if got := New(m.eng); !got.prefs.NoMouse {
		t.Error("New ignored the saved layout")
	}
}

func TestNewWithoutABoardFileSaysWhere(t *testing.T) {
	m := New(realModel(t).eng)
	if !strings.Contains(m.flash, "No drover.json found") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestPreviewForACardWithoutAnAgentExplainsWhy(t *testing.T) {
	m := realModel(t, "a")
	if got := ansi.Strip(m.renderPreview(10)); !strings.Contains(got, "No running agent") {
		t.Errorf("preview = %q", got)
	}
}

func TestWriteDefaultBoardKey(t *testing.T) {
	m := realModel(t, "a")
	m, _ = update(m, key("W"))
	if m.flash != "Wrote drover.json" {
		t.Fatalf("flash = %q", m.flash)
	}
	if _, err := os.Stat(filepath.Join(m.eng.Root, "drover.json")); err != nil {
		t.Error(err)
	}
}
