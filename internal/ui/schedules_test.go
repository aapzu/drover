package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

var scheduleNow = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

func TestCountdown(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Minute:                   "due",
		0:                              "due",
		42 * time.Second:               "in 42s",
		4*time.Minute + 12*time.Second: "in 4m12s",
		42 * time.Minute:               "in 42m",
		3*time.Hour + 5*time.Minute:    "in 3h05m",
		49 * time.Hour:                 "in 2d",
		9*time.Minute + 59*time.Second + 600*time.Millisecond: "in 10m",
	} {
		if got := countdown(d); got != want {
			t.Errorf("countdown(%v) = %q, want %q", d, got, want)
		}
	}
}

// pollingModel is a model whose cards 1 and 2 have an idle agent, and card 1
// runs a wakeup in 4 minutes and a cron every 10.
func pollingModel() Model {
	m := testModel(120, 30, 1, 3)
	m.eng = &engine.Engine{ProjectName: "proj"}
	m.snap.At = scheduleNow
	for _, task := range m.snap.Tasks[:2] {
		task.Sessions = map[string]*store.Session{store.MainSession: {
			Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true,
		}}
	}
	s := m.snap.Tasks[0].Sessions[store.MainSession]
	s.Schedules = []store.Schedule{
		{ID: "w", Wakeup: true, Prompt: "check the build", Reason: "waiting for CI", NextAt: scheduleNow.Add(4*time.Minute + 12*time.Second)},
		{ID: "c", Cron: "*/10 * * * *", Recurring: true, Prompt: "poll", Runs: 2, NextAt: scheduleNow.Add(9 * time.Minute)},
	}
	m.snap.Tasks[1].Sessions[store.MainSession].Schedules = []store.Schedule{
		{ID: "d", Wakeup: true, Prompt: "later", NextAt: scheduleNow.Add(2 * time.Hour)},
	}
	return m
}

func TestSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{15 * time.Minute: "15m", time.Hour: "1h", 90 * time.Minute: "1h30m", 6 * time.Hour: "6h"} {
		if got := span(d); got != want {
			t.Errorf("span(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestPollingCardShowsItsNextRun(t *testing.T) {
	m := pollingModel()
	if got := ansi.Strip(m.status(m.snap.Tasks[0])); got != "⏲ in 4m12s +1" {
		t.Errorf("status = %q", got)
	}
	if got := ansi.Strip(m.status(m.snap.Tasks[2])); strings.Contains(got, "⏲") {
		t.Errorf("a card without a schedule shows %q", got)
	}
	m.snap.Tasks[0].Sessions[store.MainSession].Activity = store.ActivityThinking
	if got := ansi.Strip(m.status(m.snap.Tasks[0])); !strings.Contains(got, "working") {
		t.Errorf("a working agent shows %q", got)
	}
}

func TestHeaderCountsPollingAgentsApart(t *testing.T) {
	m := pollingModel()
	got := ansi.Strip(m.renderHeader())
	if !strings.Contains(got, "⏲ 2 polling") || strings.Contains(got, "idle") {
		t.Errorf("header = %q", got)
	}
}

func TestDetailListsTheSchedules(t *testing.T) {
	m := pollingModel()
	got := ansi.Strip(m.renderDetail(m.snap.Tasks[0]))
	for _, want := range []string{"Schedules", "in 4m12s  wakeup  waiting for CI", "in 9m00s  cron */10 * * * *  ran 2×  poll"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail misses %q:\n%s", want, got)
		}
	}
	if got := ansi.Strip(m.renderDetail(m.snap.Tasks[2])); strings.Contains(got, "Schedules") {
		t.Errorf("detail of a card without schedules has a section:\n%s", got)
	}
}

func TestScheduleTimeline(t *testing.T) {
	m := pollingModel()
	m.mode = modeSchedules
	body := m.renderSchedules(m.width-6, 20)
	plain := ansi.Strip(body)
	for _, want := range []string{"#1 task", "#2 task", "◇", "●", "now", "1h", "in 4m12s  wakeup"} {
		if !strings.Contains(plain, want) {
			t.Errorf("timeline misses %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "#3 task") {
		t.Errorf("timeline lists a card without schedules:\n%s", plain)
	}
	for _, line := range strings.Split(body, "\n") {
		if w := lipgloss.Width(line); w > m.width-6 {
			t.Errorf("line is %d wide, room is %d: %q", w, m.width-6, ansi.Strip(line))
		}
	}

	m.scheduleWindow = len(scheduleWindows) - 1
	if got := ansi.Strip(m.renderSchedules(m.width-6, 20)); !strings.Contains(got, "24h") || !strings.Contains(got, "6h") {
		t.Errorf("the widest window doesn't reach a day:\n%s", got)
	}
}

func TestScheduleTimelineWithoutSchedules(t *testing.T) {
	m := testModel(120, 30, 1, 1)
	got := ansi.Strip(m.renderSchedules(100, 20))
	if !strings.Contains(got, "No agent has runs scheduled") {
		t.Errorf("empty timeline = %q", got)
	}
}

func TestScheduleTimelineKeys(t *testing.T) {
	m := pollingModel()
	m, _ = update(m, key(scheduleKey))
	if m.mode != modeSchedules {
		t.Fatalf("mode = %v after %s", m.mode, scheduleKey)
	}
	if got := ansi.Strip(m.View()); !strings.Contains(got, "Schedules") || !strings.Contains(got, "enter go to card") {
		t.Errorf("view doesn't show the timeline and its keys:\n%s", got)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.scheduleSel != 1 || m.scheduleWindow != 1 {
		t.Errorf("selection %d window %d, want 1 and 1", m.scheduleSel, m.scheduleWindow)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.scheduleSel != 1 || m.scheduleWindow != len(scheduleWindows)-1 {
		t.Errorf("selection %d window %d went past the ends", m.scheduleSel, m.scheduleWindow)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeBoard || m.current() == nil || m.current().ID != m.snap.Tasks[1].ID {
		t.Errorf("enter left mode %v on %v, want the board on the second card", m.mode, m.current())
	}
	m, _ = update(m, key(scheduleKey))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeBoard {
		t.Errorf("esc left mode %v", m.mode)
	}
}
