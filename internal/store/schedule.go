package store

import (
	"slices"
	"strings"
	"time"

	"github.com/aapzu/drover/internal/claude"
)

// Schedule is a run an agent set up for itself with ScheduleWakeup or
// CronCreate, which Claude Code triggers by submitting Prompt.
type Schedule struct {
	// ID is the cron job's id, or the tool use id of a wakeup.
	ID        string    `json:"id"`
	Wakeup    bool      `json:"wakeup,omitempty"`
	Cron      string    `json:"cron,omitempty"`
	Recurring bool      `json:"recurring,omitempty"`
	Durable   bool      `json:"durable,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	NextAt    time.Time `json:"nextAt"`
	LastAt    time.Time `json:"lastAt,omitzero"`
	Runs      int       `json:"runs,omitempty"`
}

const (
	// fireSlack is how much earlier than NextAt a run still counts as that run.
	fireSlack = 5 * time.Second
	// overdueGrace is how long a schedule that didn't run may stay listed.
	overdueGrace = time.Minute
)

// ApplySchedule records a schedule tool call. A wakeup with the same prompt
// replaces the pending one, which is how a /loop agent paces itself.
func (s *Session) ApplySchedule(c claude.ScheduleCall) {
	if c.Delete {
		s.dropSchedules(func(sc *Schedule) bool { return sc.ID == c.ID })
		return
	}
	s.dropSchedules(func(sc *Schedule) bool {
		return sc.ID == c.ID || (c.Wakeup && sc.Wakeup && sc.Prompt == c.Prompt)
	})
	s.Schedules = append(s.Schedules, Schedule{
		ID: c.ID, Wakeup: c.Wakeup, Cron: c.Cron, Recurring: c.Recurring, Durable: c.Durable,
		Prompt: c.Prompt, Reason: c.Reason, NextAt: c.NextAt,
	})
	s.sortSchedules()
}

// RunSchedules records that the agent was prompted with prompt, which runs the
// schedules that carry it and are due. A one-shot schedule is spent; a
// recurring one moves to its next time.
func (s *Session) RunSchedules(prompt string, now time.Time) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	s.dropSchedules(func(sc *Schedule) bool {
		if strings.TrimSpace(sc.Prompt) != prompt || now.Add(fireSlack).Before(sc.NextAt) {
			return false
		}
		sc.LastAt = now
		sc.Runs++
		return sc.spent(now)
	})
	s.sortSchedules()
}

// spent reports whether sc has no run left after the one at now, and otherwise
// moves it to its next run.
func (sc *Schedule) spent(now time.Time) bool {
	if sc.Wakeup || !sc.Recurring {
		return true
	}
	next, ok := claude.NextCron(sc.Cron, now)
	sc.NextAt = next
	return !ok
}

// ExpireSchedules drops the schedules that missed their run, and moves
// recurring ones past it.
func (s *Session) ExpireSchedules(now time.Time) {
	s.dropSchedules(func(sc *Schedule) bool {
		if now.Before(sc.NextAt.Add(overdueGrace)) {
			return false
		}
		return sc.spent(now)
	})
	s.sortSchedules()
}

// ClearSchedules drops the schedules that die with the agent's process.
func (s *Session) ClearSchedules() {
	s.dropSchedules(func(sc *Schedule) bool { return !sc.Durable })
}

// NextSchedule returns the schedule that runs first.
func (s *Session) NextSchedule() (Schedule, bool) {
	if len(s.Schedules) == 0 {
		return Schedule{}, false
	}
	return s.Schedules[0], true
}

// Polling reports whether the agent has runs scheduled, so that its idleness
// is only a wait. Suspending it would lose them.
func (s *Session) Polling() bool { return len(s.Schedules) > 0 }

func (s *Session) sortSchedules() {
	slices.SortStableFunc(s.Schedules, func(a, b Schedule) int { return a.NextAt.Compare(b.NextAt) })
}

// dropSchedules removes the schedules for which drop reports true. drop may
// change the schedule it is given.
func (s *Session) dropSchedules(drop func(*Schedule) bool) {
	kept := s.Schedules[:0]
	for _, sc := range s.Schedules {
		if !drop(&sc) {
			kept = append(kept, sc)
		}
	}
	s.Schedules = kept
}

// maxFires bounds the runs Fires lists for a cron that runs every minute.
const maxFires = 500

// Fires lists when sc runs from the moment from until until, soonest first. A
// run that is already due counts as at from.
func (sc Schedule) Fires(from, until time.Time) []time.Time {
	at := sc.NextAt
	if at.Before(from) {
		at = from
	}
	var out []time.Time
	for !at.After(until) && len(out) < maxFires {
		out = append(out, at)
		if sc.Wakeup || !sc.Recurring {
			break
		}
		next, ok := claude.NextCron(sc.Cron, at)
		if !ok {
			break
		}
		at = next
	}
	return out
}
