package ui

import (
	"fmt"
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
	m.snap.Tasks[2].Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true}}
	got := ansi.Strip(m.renderHeader())
	if !strings.Contains(got, "⏲ 2 polling") || !strings.Contains(got, "● 1 idle") {
		t.Errorf("header = %q", got)
	}
}

func TestPollingCardStatusPrecedence(t *testing.T) {
	m := pollingModel()
	s := m.snap.Tasks[0].Sessions[store.MainSession]
	s.Background = []string{"b1"}
	if got := ansi.Strip(m.status(m.snap.Tasks[0])); !strings.Contains(got, "background tasks") {
		t.Errorf("polling with background tasks shows %q", got)
	}
	s.Background = nil
	s.Pending = []store.Pending{{Text: "x"}}
	if got := ansi.Strip(m.status(m.snap.Tasks[0])); got != "⏲ in 4m12s +1 +1 queued" {
		t.Errorf("polling with a queued message shows %q", got)
	}
	s.Pending = nil
	s.Schedules = []store.Schedule{{ID: "w", Wakeup: true, NextAt: scheduleNow.Add(-time.Minute)}}
	if got := ansi.Strip(m.status(m.snap.Tasks[0])); got != "⏲ due" {
		t.Errorf("an overdue run shows %q", got)
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

// axisColumn finds where mark sits on the timeline row of the card numbered
// number, counted from the start of the axis, or -1.
func axisColumn(t *testing.T, m Model, number int, mark string) int {
	t.Helper()
	axisStart := min(28, (m.width-6)/3) + 1
	for _, line := range strings.Split(ansi.Strip(m.renderSchedules(m.width-6, 30)), "\n") {
		if !strings.Contains(line, fmt.Sprintf("#%d task", number)) {
			continue
		}
		for i, r := range []rune(line) {
			if i >= axisStart && string(r) == mark {
				return i - axisStart
			}
		}
	}
	return -1
}

func TestScheduleTimelinePlacesMarks(t *testing.T) {
	m := pollingModel()
	m.snap.Tasks[0].Sessions[store.MainSession].Schedules = []store.Schedule{
		{ID: "w", Wakeup: true, NextAt: scheduleNow.Add(30 * time.Minute)},
	}
	half := axisColumn(t, m, 1, "◇")
	m.snap.Tasks[0].Sessions[store.MainSession].Schedules[0].NextAt = scheduleNow.Add(15 * time.Minute)
	quarter := axisColumn(t, m, 1, "◇")
	m.snap.Tasks[0].Sessions[store.MainSession].Schedules[0].NextAt = scheduleNow.Add(-time.Minute)
	overdue := axisColumn(t, m, 1, "◇")
	if !(overdue < quarter && quarter < half) {
		t.Errorf("marks at now, +15m, +30m are at columns %d, %d, %d; want them in that order", overdue, quarter, half)
	}
	if overdue > 2 {
		t.Errorf("an overdue run is at column %d, want the start of the axis", overdue)
	}

	m.snap.Tasks[0].Sessions[store.MainSession].Schedules[0].NextAt = scheduleNow.Add(2 * time.Hour)
	if got := axisColumn(t, m, 1, "◇"); got != -1 {
		t.Errorf("a run in 2h is at column %d of the 1h window", got)
	}
	m.scheduleWindow = 1
	if got := axisColumn(t, m, 1, "◇"); got == -1 {
		t.Error("a run in 2h is missing from the 6h window")
	}
}

func TestScheduleTimelineStaysWithinItsHeight(t *testing.T) {
	m := testModel(120, 30, 1, 14)
	m.snap.At = scheduleNow
	for i, task := range m.snap.Tasks {
		s := &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true}
		for j := range 4 {
			s.Schedules = append(s.Schedules, store.Schedule{ID: fmt.Sprintf("%d-%d", i, j), Wakeup: true, NextAt: scheduleNow.Add(time.Duration(i+j+1) * time.Minute)})
		}
		task.Sessions = map[string]*store.Session{store.MainSession: s}
	}
	for _, sel := range []int{0, 7, 13} {
		m.scheduleSel = sel
		out := ansi.Strip(m.renderSchedules(m.width-6, 20))
		if n := strings.Count(out, "\n") + 1; n > 20 {
			t.Errorf("selected %d: timeline is %d lines, room is 20", sel, n)
		}
		if !strings.Contains(out, fmt.Sprintf("▸ #%d task", sel+1)) {
			t.Errorf("selected %d: the selected row is not shown:\n%s", sel, out)
		}
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
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyUp})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.scheduleSel != 0 || m.scheduleWindow != 0 {
		t.Errorf("selection %d window %d went below the start", m.scheduleSel, m.scheduleWindow)
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
	m.scheduleSel = 1
	m.snap.Tasks[1].Sessions[store.MainSession].Schedules = nil
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeBoard || m.current().ID != m.snap.Tasks[0].ID {
		t.Errorf("enter after the rows shrank left mode %v on %v, want the board on the first card", m.mode, m.current())
	}
	m, _ = update(m, key(scheduleKey))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeBoard {
		t.Errorf("esc left mode %v", m.mode)
	}
}
