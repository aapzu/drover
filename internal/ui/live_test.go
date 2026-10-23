package ui

import (
	"strings"
	"testing"

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

func TestLiveModeDropsKeysTmuxHasNoNameFor(t *testing.T) {
	m := liveModel()
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab"), Alt: true})
	if _, ok := sentJob(m); ok || cmd != nil {
		t.Errorf("an unmappable key was sent: ok %v, cmd nil %v", ok, cmd == nil)
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
