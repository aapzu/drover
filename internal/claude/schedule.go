package claude

import (
	"encoding/json"
	"time"

	"github.com/robfig/cron/v3"
)

const unknownRun = time.Hour

// ScheduleCall is a schedule an agent set up or removed with ScheduleWakeup,
// CronCreate or CronDelete.
type ScheduleCall struct {
	Wakeup    bool
	Delete    bool
	ID        string
	Cron      string
	Prompt    string
	Reason    string
	Recurring bool
	NextAt    time.Time
}

// ScheduleCall decodes a PostToolUse event of a schedule tool. It reports false
// for any other event and for a call that didn't create or delete a schedule.
// now stands in for the first run when the response doesn't say when it is.
func (e Event) ScheduleCall(now time.Time) (ScheduleCall, bool) {
	if e.Name != "PostToolUse" {
		return ScheduleCall{}, false
	}
	switch e.ToolName {
	case "ScheduleWakeup":
		var in struct {
			DelaySeconds float64 `json:"delaySeconds"`
			Prompt       string  `json:"prompt"`
			Reason       string  `json:"reason"`
		}
		if json.Unmarshal(e.ToolInput, &in) != nil || e.ToolUseID == "" {
			return ScheduleCall{}, false
		}
		var res struct {
			ScheduledFor float64 `json:"scheduledFor"`
		}
		json.Unmarshal(e.ToolResponse, &res)
		next := msTime(res.ScheduledFor, now)
		if next.IsZero() {
			next = now.Add(time.Duration(in.DelaySeconds * float64(time.Second)))
		}
		return ScheduleCall{Wakeup: true, ID: e.ToolUseID, Prompt: in.Prompt, Reason: in.Reason, NextAt: next}, true
	case "CronCreate":
		var in struct {
			Cron   string `json:"cron"`
			Prompt string `json:"prompt"`
		}
		var res struct {
			ID         string  `json:"id"`
			Recurring  bool    `json:"recurring"`
			NextFireAt float64 `json:"nextFireAt"`
		}
		if json.Unmarshal(e.ToolInput, &in) != nil || json.Unmarshal(e.ToolResponse, &res) != nil || res.ID == "" {
			return ScheduleCall{}, false
		}
		next := msTime(res.NextFireAt, now)
		if next.IsZero() {
			next = NextRun(in.Cron, now)
		}
		return ScheduleCall{ID: res.ID, Cron: in.Cron, Prompt: in.Prompt, Recurring: res.Recurring, NextAt: next}, true
	case "CronDelete":
		var in struct {
			ID string `json:"id"`
		}
		return ScheduleCall{Delete: true, ID: in.ID}, json.Unmarshal(e.ToolInput, &in) == nil && in.ID != ""
	}
	return ScheduleCall{}, false
}

// msTime converts the epoch milliseconds a schedule tool reports. It returns
// the zero time when ms is missing or so far before now that it can't be one.
func msTime(ms float64, now time.Time) time.Time {
	t := time.UnixMilli(int64(ms))
	if ms <= 0 || t.Before(now.Add(-24*time.Hour)) {
		return time.Time{}
	}
	return t
}

// NextCron returns the first time after t that the cron expression matches, in
// t's location. It reports false for an expression it can't read or that never
// matches.
func NextCron(expr string, t time.Time) (time.Time, bool) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, false
	}
	next := sched.Next(t)
	return next, !next.IsZero()
}

// NextRun is NextCron for a cron that Claude Code runs whether or not it can be
// read here: one it can't read is guessed to run in an hour.
func NextRun(expr string, t time.Time) time.Time {
	if next, ok := NextCron(expr, t); ok {
		return next
	}
	return t.Add(unknownRun)
}
