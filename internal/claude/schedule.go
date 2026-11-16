package claude

import (
	"encoding/json"
	"time"

	"github.com/robfig/cron/v3"
)

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
	Durable   bool
	NextAt    time.Time
}

// ScheduleCall decodes a PostToolUse event of a schedule tool. now stands in
// for the first run when the response doesn't say when it is.
func (e Event) ScheduleCall(now time.Time) (ScheduleCall, bool) {
	if e.Name != "PostToolUse" {
		return ScheduleCall{}, false
	}
	in := e.ToolInput
	switch e.ToolName {
	case "ScheduleWakeup":
		var res struct {
			ScheduledFor float64 `json:"scheduledFor"`
		}
		json.Unmarshal(e.ToolResponse, &res)
		next := msTime(res.ScheduledFor)
		if next.IsZero() {
			next = now.Add(time.Duration(in.DelaySeconds) * time.Second)
		}
		return ScheduleCall{Wakeup: true, ID: e.ToolUseID, Prompt: in.Prompt, Reason: in.Reason, NextAt: next}, true
	case "CronCreate":
		var res struct {
			ID         string  `json:"id"`
			Recurring  bool    `json:"recurring"`
			Durable    bool    `json:"durable"`
			NextFireAt float64 `json:"nextFireAt"`
		}
		if json.Unmarshal(e.ToolResponse, &res) != nil || res.ID == "" {
			return ScheduleCall{}, false
		}
		next := msTime(res.NextFireAt)
		if next.IsZero() {
			next, _ = NextCron(in.Cron, now)
		}
		return ScheduleCall{ID: res.ID, Cron: in.Cron, Prompt: in.Prompt, Recurring: res.Recurring, Durable: res.Durable, NextAt: next}, true
	case "CronDelete":
		return ScheduleCall{Delete: true, ID: in.ID}, in.ID != ""
	}
	return ScheduleCall{}, false
}

func msTime(ms float64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms))
}

// NextCron returns the first time after t that the five-field cron expression
// matches, in t's location.
func NextCron(expr string, t time.Time) (time.Time, bool) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, false
	}
	next := sched.Next(t)
	return next, !next.IsZero()
}
