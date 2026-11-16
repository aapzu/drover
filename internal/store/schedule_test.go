package store

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/claude"
)

var base = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

func wakeup(id, prompt string, in time.Duration) claude.ScheduleCall {
	return claude.ScheduleCall{Wakeup: true, ID: id, Prompt: prompt, NextAt: base.Add(in)}
}

func ids(s *Session) []string {
	var out []string
	for _, sc := range s.Schedules {
		out = append(out, sc.ID)
	}
	slices.Sort(out)
	return out
}

func TestNextScheduleIsTheSoonest(t *testing.T) {
	s := &Session{}
	if _, ok := s.NextSchedule(); ok || s.Polling() {
		t.Fatal("a session without schedules is polling")
	}
	s.ApplySchedule(wakeup("a", "later", time.Hour))
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "*/5 * * * *", Prompt: "poll", Recurring: true, NextAt: base.Add(time.Minute)})
	s.ApplySchedule(wakeup("b", "last", 2*time.Hour))
	if next, ok := s.NextSchedule(); !ok || next.ID != "c" || !s.Polling() {
		t.Errorf("next = %v, %v", next.ID, ok)
	}
}

func TestApplyScheduleReplacesAWakeupWithTheSamePrompt(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("a", "check", time.Minute))
	s.ApplySchedule(wakeup("b", "other", time.Minute))
	s.ApplySchedule(wakeup("c", "check", 2*time.Minute))
	if got := ids(s); !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("schedules = %v", got)
	}
}

func TestApplyScheduleDeletesACron(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "* * * * *", Recurring: true, NextAt: base})
	s.ApplySchedule(claude.ScheduleCall{Delete: true, ID: "unknown"})
	if !s.Polling() {
		t.Fatal("deleting an unknown id dropped the cron")
	}
	s.ApplySchedule(claude.ScheduleCall{Delete: true, ID: "c"})
	if s.Polling() {
		t.Errorf("still polling: %v", ids(s))
	}
}

func TestRunSchedules(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("w", "check", 10*time.Minute))
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "*/5 * * * *", Prompt: "poll", Recurring: true, NextAt: base.Add(5 * time.Minute)})

	s.RunSchedules("check", base)
	if len(s.Schedules) != 2 {
		t.Fatal("a prompt long before the run took the wakeup")
	}
	s.RunSchedules("something else", base.Add(10*time.Minute))
	if len(s.Schedules) != 2 {
		t.Fatal("another prompt took a schedule")
	}
	s.RunSchedules("check", base.Add(10*time.Minute-fireSlack))
	if got := ids(s); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("schedules = %v, want the wakeup spent by a prompt within the slack", got)
	}

	run := base.Add(5*time.Minute + time.Second)
	s.RunSchedules(" poll\n", run)
	next, _ := s.NextSchedule()
	if want := base.Add(10 * time.Minute); !next.NextAt.Equal(want) || next.Runs != 1 {
		t.Errorf("cron after a run = %+v, want next %v after 1 run", next, want)
	}
}

func TestRunSchedulesKeepsACronItCantRead(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "0 9 * * 7", Prompt: "poll", Recurring: true, NextAt: base})
	s.RunSchedules("poll", base)
	next, ok := s.NextSchedule()
	if !ok || !next.NextAt.Equal(base.Add(time.Hour)) {
		t.Errorf("schedule = %+v, %v; want it kept with a guessed next run", next, ok)
	}
}

func TestExpireSchedules(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("w", "check", time.Minute))
	s.ApplySchedule(claude.ScheduleCall{ID: "once", Cron: "5 10 * * *", Prompt: "x", NextAt: base.Add(5 * time.Minute)})
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "*/5 * * * *", Prompt: "poll", Recurring: true, NextAt: base.Add(5 * time.Minute)})

	s.ExpireSchedules(base.Add(time.Minute + overdueGrace/2))
	if len(s.Schedules) != 3 {
		t.Fatalf("schedules = %v, want all kept within the grace period", ids(s))
	}
	s.ExpireSchedules(base.Add(time.Minute + overdueGrace + time.Second))
	if got := ids(s); !slices.Equal(got, []string{"c", "once"}) {
		t.Fatalf("schedules = %v, want the missed wakeup dropped", got)
	}
	s.ExpireSchedules(base.Add(5*time.Minute + overdueGrace + time.Second))
	next, _ := s.NextSchedule()
	if got := ids(s); !slices.Equal(got, []string{"c"}) || !next.NextAt.Equal(base.Add(10*time.Minute)) {
		t.Errorf("schedules = %v, next %v; want the cron moved on and the one-shot dropped", got, next.NextAt)
	}
}

func TestExpireSchedulesWaitsForABusyAgent(t *testing.T) {
	s := &Session{UpdatedAt: base.Add(10 * time.Minute)}
	s.ApplySchedule(wakeup("w", "check", time.Minute))
	s.ExpireSchedules(base.Add(10*time.Minute + overdueGrace/2))
	if !s.Polling() {
		t.Error("a wakeup that fell due during a turn was dropped as the turn ended")
	}
	s.ExpireSchedules(base.Add(10*time.Minute + overdueGrace + time.Second))
	if s.Polling() {
		t.Error("a wakeup the agent never ran was kept")
	}
}

func TestSlowCronsGetALongerGrace(t *testing.T) {
	hourly := Schedule{Cron: "0 * * * *", Recurring: true, NextAt: base}
	if got := hourly.grace(); got != 6*time.Minute {
		t.Errorf("hourly grace = %v, want a tenth of the period", got)
	}
	daily := Schedule{Cron: "0 10 * * *", Recurring: true, NextAt: base}
	if got := daily.grace(); got != maxGrace {
		t.Errorf("daily grace = %v, want the cap", got)
	}
	if got := (Schedule{Wakeup: true, NextAt: base}).grace(); got != overdueGrace {
		t.Errorf("wakeup grace = %v", got)
	}
}

func TestClearSchedules(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("w", "check", time.Minute))
	s.ClearSchedules()
	if s.Polling() {
		t.Errorf("schedules = %v", ids(s))
	}
}

func TestCloneCopiesSchedules(t *testing.T) {
	task := &Task{Sessions: map[string]*Session{MainSession: {}}}
	s := task.Sessions[MainSession]
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "*/5 * * * *", Prompt: "poll", Recurring: true, NextAt: base})
	c := task.Clone()
	s.RunSchedules("poll", base)
	if got := c.Sessions[MainSession].Schedules[0]; !got.NextAt.Equal(base) || got.Runs != 0 {
		t.Errorf("the clone follows the original: %+v", got)
	}
}

func TestSchedulesSurviveTheStateFile(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("w", "check", time.Minute))
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var got Session
	if err := json.Unmarshal(data, &got); err != nil || len(got.Schedules) != 1 || !got.Schedules[0].NextAt.Equal(base.Add(time.Minute)) {
		t.Errorf("round trip = %+v, %v", got.Schedules, err)
	}
}

func TestFires(t *testing.T) {
	cron := Schedule{Cron: "*/10 * * * *", Recurring: true, NextAt: base.Add(10 * time.Minute)}
	got := cron.Fires(base, base.Add(time.Hour), 0)
	if len(got) != 6 || !got[0].Equal(base.Add(10*time.Minute)) || !got[5].Equal(base.Add(time.Hour)) {
		t.Errorf("cron fires = %v", got)
	}
	if got := cron.Fires(base, base.Add(5*time.Minute), 0); len(got) != 0 {
		t.Errorf("fires before the first run = %v", got)
	}
	if got := cron.Fires(base, base.Add(time.Hour), 25*time.Minute); len(got) != 2 {
		t.Errorf("fires at least 25m apart = %v, want 2", got)
	}
	every := Schedule{Cron: "* * * * *", Recurring: true, NextAt: base}
	if got := every.Fires(base, base.Add(24*time.Hour), 24*time.Hour/100); len(got) > 101 {
		t.Errorf("a per-minute cron over a day with a step has %d fires", len(got))
	}
	once := Schedule{Wakeup: true, NextAt: base.Add(-time.Minute)}
	if got := once.Fires(base, base.Add(time.Hour), 0); len(got) != 1 || !got[0].Equal(base) {
		t.Errorf("an overdue wakeup fires at %v, want now", got)
	}
}
