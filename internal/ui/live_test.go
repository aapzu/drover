package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func liveModel() Model {
	m := testModel(120, 40, 2, 1)
	m.keys = &keySender{jobs: make(chan keyJob, 4)}
	m.snap.Live = map[string]string{"c0-t0": "sess"}
	m.mode = modeLive
	return m
}

func TestLiveModeSendsTypedKeysToTheCardsAgent(t *testing.T) {
	m := liveModel()
	m.previewScroll = 5
	m, cmd := update(m, key("x"))
	job, ok := sentJob(m)
	if !ok || job.session != "sess" || job.literal != "x" {
		t.Errorf("job = %+v, %v", job, ok)
	}
	if m.previewScroll != 0 || cmd == nil {
		t.Errorf("previewScroll = %d, cmd nil = %v", m.previewScroll, cmd == nil)
	}

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb"), Paste: true})
	if job, _ := sentJob(m); job.paste != "a\nb" {
		t.Errorf("paste job = %+v", job)
	}
}

func TestLiveModeWithoutARunningAgentSaysSoAndLeaves(t *testing.T) {
	m := liveModel()
	m.snap.Live = map[string]string{}
	m, _ = update(m, key("x"))
	if _, ok := sentJob(m); ok || !strings.Contains(m.flash, "No running agent") {
		t.Errorf("flash %q", m.flash)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if m.mode != modeBoard {
		t.Errorf("mode = %v after ctrl+q", m.mode)
	}
}

func TestLiveModeSendsNamedKeysAndForwardsEsc(t *testing.T) {
	for _, tt := range []struct {
		msg tea.KeyMsg
		key string
	}{
		{tea.KeyMsg{Type: tea.KeyEsc}, "Escape"},
		{tea.KeyMsg{Type: tea.KeyEnter}, "Enter"},
		{tea.KeyMsg{Type: tea.KeyCtrlC}, "C-c"},
	} {
		m, _ := update(liveModel(), tt.msg)
		if job, ok := sentJob(m); !ok || job.key != tt.key || job.session != "sess" {
			t.Errorf("%q: job = %+v, %v", tt.msg.String(), job, ok)
		}
		if m.mode != modeLive {
			t.Errorf("%q left live mode", tt.msg.String())
		}
	}
}

func TestLiveModeDropsKeysItCannotSend(t *testing.T) {
	for name, msg := range map[string]tea.KeyMsg{
		"ctrl+z":                       {Type: tea.KeyCtrlZ},
		"a chord tmux has no name for": {Type: tea.KeyRunes, Runes: []rune("ab"), Alt: true},
	} {
		t.Run(name, func(t *testing.T) {
			m, cmd := update(liveModel(), msg)
			if _, ok := sentJob(m); ok || cmd != nil || m.mode != modeLive {
				t.Errorf("sent %v, cmd nil %v, mode %v", ok, cmd == nil, m.mode)
			}
		})
	}
}

func TestCtrlQLeavesLiveModeAndDiscardsAnUntouchedCard(t *testing.T) {
	m := liveCard(t, "")
	m.mode = modeLive
	m.keys = &keySender{jobs: make(chan keyJob, 4)}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if m.mode != modeBoard || cmd == nil || m.busy[m.current().ID].label != "discarding" {
		t.Errorf("mode %v cmd nil %v busy %v", m.mode, cmd == nil, m.busy)
	}
	if _, ok := sentJob(m); ok {
		t.Error("ctrl+q was sent to the agent")
	}

	titled := liveCard(t, "named")
	titled.mode = modeLive
	titled, cmd = update(titled, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if titled.mode != modeBoard || cmd != nil {
		t.Errorf("a titled card: mode %v cmd nil %v", titled.mode, cmd == nil)
	}

	empty := liveModel()
	empty.snap.Tasks = nil
	empty, cmd = update(empty, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if empty.mode != modeBoard || cmd != nil {
		t.Errorf("no card: mode %v cmd nil %v", empty.mode, cmd == nil)
	}
}

func TestLiveModeWithoutACardIgnoresKeys(t *testing.T) {
	m := liveModel()
	m.snap.Tasks = nil
	m, cmd := update(m, key("x"))
	if _, ok := sentJob(m); ok || cmd != nil || m.mode != modeLive {
		t.Errorf("sent %v cmd nil %v mode %v", ok, cmd == nil, m.mode)
	}
}

func TestStartLiveBeginsAGenerationAndCapturesRightAway(t *testing.T) {
	m := liveCard(t, "a")
	m.prefs.HidePreview = true
	m, cmd := m.startLive()
	if m.mode != modeLive || m.liveGen != 1 || m.prefs.HidePreview || cmd == nil {
		t.Fatalf("mode %v gen %d hidePreview %v", m.mode, m.liveGen, m.prefs.HidePreview)
	}
	var tick bool
	for _, msg := range runCmds(cmd) {
		if lt, ok := msg.(liveTickMsg); ok && lt.gen == 1 {
			tick = true
		}
	}
	if !tick {
		t.Error("the live loop was not started for this generation")
	}
	if again, _ := m.startLive(); again.liveGen != 2 {
		t.Errorf("a second start reused generation %d", again.liveGen)
	}

	noCard, cmd := realModel(t).startLive()
	if noCard.mode != modeBoard || cmd != nil || noCard.liveGen != 0 {
		t.Errorf("no card: mode %v gen %d", noCard.mode, noCard.liveGen)
	}
}

func TestAfterKeyWaitsBrieflyBeforeCapturing(t *testing.T) {
	if afterKey(nil) != nil {
		t.Error("afterKey invented a command")
	}
	start := time.Now()
	got := afterKey(func() tea.Msg { return previewMsg{taskID: "x"} })()
	if p, ok := got.(previewMsg); !ok || p.taskID != "x" {
		t.Errorf("afterKey returned %v", got)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("afterKey did not wait for the agent to react")
	}
}

func TestLiveTickCarriesItsGeneration(t *testing.T) {
	if got := liveTick(7)(); got != (liveTickMsg{gen: 7}) {
		t.Errorf("liveTick returned %v", got)
	}
}

func TestKeySenderReportsFailedSends(t *testing.T) {
	s := newKeySender()
	for _, job := range []keyJob{
		{session: "drover-no-such-session", literal: "hi"},
		{session: "drover-no-such-session", key: "Enter"},
		{session: "drover-no-such-session", paste: "a\nb"},
	} {
		s.send(job)
		select {
		case err := <-s.errs:
			if err == nil {
				t.Errorf("%+v: nil error reported", job)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%+v: no error reported", job)
		}
	}
}
