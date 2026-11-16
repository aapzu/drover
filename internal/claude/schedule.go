package claude

import (
	"encoding/json"
	"time"

	"github.com/robfig/cron/v3"
)

// UnknownRun is how far ahead a run is put when Claude Code didn't say when it
// is and the cron can't be read here either.
const UnknownRun = time.Hour

// ScheduleCall is a schedule an agent set up or removed with ScheduleWakeup,
// CronCreate or CronDelete.
type ScheduleCall struct {
	Wakeup bool
	Delete bool
	// ID is the cron job's id, or for a wakeup the id of the tool use.
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
	var in struct {
		ID           string  `json:"id"`
		Cron         string  `json:"cron"`
		DelaySeconds float64 `json:"delaySeconds"`
		Prompt       string  `json:"prompt"`
		Reason       string  `json:"reason"`
	}
	if json.Unmarshal(e.ToolInput, &in) != nil {
		return ScheduleCall{}, false
	}
	switch e.ToolName {
	case "ScheduleWakeup":
		var res struct {
			ScheduledFor float64 `json:"scheduledFor"`
		}
		json.Unmarshal(e.ToolResponse, &res)
		next := msTime(res.ScheduledFor, now)
		if next.IsZero() {
			next = now.Add(time.Duration(in.DelaySeconds * float64(time.Second)))
		}
		return ScheduleCall{Wakeup: true, ID: e.ToolUseID, Prompt: in.Prompt, Reason: in.Reason, NextAt: next}, e.ToolUseID != ""
	case "CronCreate":
		var res struct {
			ID         string  `json:"id"`
			Recurring  bool    `json:"recurring"`
			NextFireAt float64 `json:"nextFireAt"`
		}
		if json.Unmarshal(e.ToolResponse, &res) != nil || res.ID == "" {
			return ScheduleCall{}, false
		}
		next := msTime(res.NextFireAt, now)
		if next.IsZero() {
			var ok bool
			if next, ok = NextCron(in.Cron, now); !ok {
				next = now.Add(UnknownRun)
			}
		}
		return ScheduleCall{ID: res.ID, Cron: in.Cron, Prompt: in.Prompt, Recurring: res.Recurring, NextAt: next}, true
	case "CronDelete":
		return ScheduleCall{Delete: true, ID: in.ID}, in.ID != ""
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
