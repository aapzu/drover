package store

import (
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
	return out
}

func eq(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestApplyScheduleKeepsThemSoonestFirst(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("a", "later", time.Hour))
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "*/5 * * * *", Prompt: "poll", Recurring: true, NextAt: base.Add(time.Minute)})
	if got := ids(s); !eq(got, []string{"c", "a"}) {
		t.Errorf("order = %v", got)
	}
	if next, _ := s.NextSchedule(); next.ID != "c" {
		t.Errorf("next = %v", next.ID)
	}
}

func TestApplyScheduleReplacesAWakeupWithTheSamePrompt(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("a", "check", time.Minute))
	s.ApplySchedule(wakeup("b", "other", time.Minute))
	s.ApplySchedule(wakeup("c", "check", 2*time.Minute))
	if got := ids(s); !eq(got, []string{"b", "c"}) {
		t.Errorf("schedules = %v", got)
	}
}

func TestApplyScheduleDeletesACron(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "* * * * *", Recurring: true, NextAt: base})
	s.ApplySchedule(claude.ScheduleCall{Delete: true, ID: "c"})
	if s.Polling() {
		t.Errorf("still polling: %v", ids(s))
	}
}

func TestRunSchedules(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("w", "check", time.Minute))
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "*/5 * * * *", Prompt: "poll", Recurring: true, NextAt: base.Add(5 * time.Minute)})

	s.RunSchedules("check", base)
	if len(s.Schedules) != 2 {
		t.Fatal("a prompt before the run took the wakeup")
	}
	s.RunSchedules("something else", base.Add(time.Minute))
	if len(s.Schedules) != 2 {
		t.Fatal("another prompt took a schedule")
	}

	s.RunSchedules(" check\n", base.Add(time.Minute))
	if got := ids(s); !eq(got, []string{"c"}) {
		t.Fatalf("schedules = %v, want the wakeup spent", got)
	}

	run := base.Add(5*time.Minute + time.Second)
	s.RunSchedules("poll", run)
	next, _ := s.NextSchedule()
	if want := base.Add(10 * time.Minute); !next.NextAt.Equal(want) || next.Runs != 1 || !next.LastAt.Equal(run) {
		t.Errorf("cron after a run = %+v, want next %v, 1 run", next, want)
	}
}

func TestExpireSchedules(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("w", "check", time.Minute))
	s.ApplySchedule(claude.ScheduleCall{ID: "once", Cron: "5 10 * * *", Prompt: "x", NextAt: base.Add(5 * time.Minute)})
	s.ApplySchedule(claude.ScheduleCall{ID: "c", Cron: "*/5 * * * *", Prompt: "poll", Recurring: true, NextAt: base.Add(5 * time.Minute)})

	s.ExpireSchedules(base.Add(30 * time.Second))
	if len(s.Schedules) != 3 {
		t.Fatalf("schedules = %v, want all kept before their time", ids(s))
	}
	s.ExpireSchedules(base.Add(2 * time.Minute))
	if got := ids(s); !eq(got, []string{"once", "c"}) {
		t.Fatalf("schedules = %v, want the missed wakeup dropped", got)
	}
	s.ExpireSchedules(base.Add(7 * time.Minute))
	next, ok := s.NextSchedule()
	if got := ids(s); !eq(got, []string{"c"}) || !ok || !next.NextAt.Equal(base.Add(10*time.Minute)) {
		t.Errorf("schedules = %v, next %v; want the cron moved on and the one-shot dropped", got, next.NextAt)
	}
}

func TestClearSchedulesKeepsDurableOnes(t *testing.T) {
	s := &Session{}
	s.ApplySchedule(wakeup("w", "check", time.Minute))
	s.ApplySchedule(claude.ScheduleCall{ID: "d", Cron: "0 9 * * *", Durable: true, Recurring: true, NextAt: base})
	s.ClearSchedules()
	if got := ids(s); !eq(got, []string{"d"}) {
		t.Errorf("schedules = %v", got)
	}
}

func TestCloneCopiesSchedules(t *testing.T) {
	task := &Task{Sessions: map[string]*Session{MainSession: {}}}
	task.Sessions[MainSession].ApplySchedule(wakeup("w", "check", time.Minute))
	c := task.Clone()
	task.Sessions[MainSession].ClearSchedules()
	if len(c.Sessions[MainSession].Schedules) != 1 {
		t.Error("the clone shares the schedules")
	}
}

func TestFires(t *testing.T) {
	cron := Schedule{Cron: "*/10 * * * *", Recurring: true, NextAt: base.Add(10 * time.Minute)}
	got := cron.Fires(base, base.Add(time.Hour))
	if len(got) != 6 || !got[0].Equal(base.Add(10*time.Minute)) || !got[5].Equal(base.Add(time.Hour)) {
		t.Errorf("cron fires = %v", got)
	}
	if got := cron.Fires(base, base.Add(5*time.Minute)); len(got) != 0 {
		t.Errorf("fires before the first run = %v", got)
	}
	once := Schedule{Wakeup: true, NextAt: base.Add(-time.Minute)}
	if got := once.Fires(base, base.Add(time.Hour)); len(got) != 1 || !got[0].Equal(base) {
		t.Errorf("an overdue wakeup fires at %v, want now", got)
	}
}
