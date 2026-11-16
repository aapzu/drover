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
	Prompt    string    `json:"prompt,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	NextAt    time.Time `json:"nextAt"`
	// Runs counts the prompts taken for a run of this schedule.
	Runs int `json:"runs,omitempty"`
}

const (
	// fireSlack is how much earlier than NextAt a run still counts as that run.
	fireSlack = 90 * time.Second
	// overdueGrace is how long after its time, or after the agent last did
	// anything, a schedule may stay listed without having run. A recurring one
	// gets a tenth of its period instead if that is longer, up to maxGrace,
	// because Claude Code runs those late.
	overdueGrace = 2 * time.Minute
	maxGrace     = 15 * time.Minute
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
		ID: c.ID, Wakeup: c.Wakeup, Cron: c.Cron, Recurring: c.Recurring,
		Prompt: c.Prompt, Reason: c.Reason, NextAt: c.NextAt,
	})
}

// RunSchedules records that the agent was prompted with prompt. Claude Code
// emits nothing when a scheduled run starts, so the prompt is the only sign:
// it runs the schedules that carry it and are due, which also counts you typing
// the same text. A one-shot schedule is spent; a recurring one moves to its
// next time.
func (s *Session) RunSchedules(prompt string, now time.Time) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}
	s.dropSchedules(func(sc *Schedule) bool {
		if strings.TrimSpace(sc.Prompt) != prompt || now.Add(fireSlack).Before(sc.NextAt) {
			return false
		}
		sc.Runs++
		return !sc.advance(now)
	})
}

// advance moves a recurring schedule on to its next run after now. It reports
// false when sc has no run left. A cron this program can't read is kept, with
// a guess for its next run, because Claude Code still runs it.
func (sc *Schedule) advance(now time.Time) bool {
	if sc.Wakeup || !sc.Recurring {
		return false
	}
	next, ok := claude.NextCron(sc.Cron, now)
	if !ok {
		next = now.Add(claude.UnknownRun)
	}
	sc.NextAt = next
	return true
}

// grace is how long past NextAt the schedule may wait for its run.
func (sc Schedule) grace() time.Duration {
	if !sc.Recurring || sc.Wakeup {
		return overdueGrace
	}
	next, ok := claude.NextCron(sc.Cron, sc.NextAt)
	if !ok {
		return overdueGrace
	}
	return min(max(overdueGrace, next.Sub(sc.NextAt)/10), maxGrace)
}

// ExpireSchedules drops the one-shot schedules that missed their run, and moves
// recurring ones past it. A schedule that is due while the agent works waits
// until the agent has been quiet for the grace period, since Claude Code only
// runs it between turns.
func (s *Session) ExpireSchedules(now time.Time) {
	s.dropSchedules(func(sc *Schedule) bool {
		late := sc.NextAt
		if s.UpdatedAt.After(late) {
			late = s.UpdatedAt
		}
		if now.Before(late.Add(sc.grace())) {
			return false
		}
		return !sc.advance(now)
	})
}

// ClearSchedules drops every schedule, for an agent whose process ended.
func (s *Session) ClearSchedules() { s.Schedules = nil }

// NextSchedule returns the schedule that runs first.
func (s *Session) NextSchedule() (Schedule, bool) {
	if len(s.Schedules) == 0 {
		return Schedule{}, false
	}
	return slices.MinFunc(s.Schedules, func(a, b Schedule) int { return a.NextAt.Compare(b.NextAt) }), true
}

// Polling reports whether the agent has runs scheduled, so that its idleness
// is only a wait that suspending it would end.
func (s *Session) Polling() bool { return len(s.Schedules) > 0 }

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

// Fires lists when sc runs between from and until, soonest first, with at least
// step between two of them. A run that is already due counts as at from.
func (sc Schedule) Fires(from, until time.Time, step time.Duration) []time.Time {
	at := sc.NextAt
	if at.Before(from) {
		at = from
	}
	var out []time.Time
	for !at.After(until) {
		out = append(out, at)
		if sc.Wakeup || !sc.Recurring {
			break
		}
		next, ok := claude.NextCron(sc.Cron, at.Add(max(step, time.Minute)-time.Nanosecond))
		if !ok {
			break
		}
		at = next
	}
	return out
}
