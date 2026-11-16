package claude

import (
	"encoding/json"
	"testing"
	"time"
)

func scheduleEvent(t *testing.T, line string) Event {
	t.Helper()
	var e Event
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestScheduleCall(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		line string
		want ScheduleCall
		ok   bool
	}{
		{
			"wakeup uses the time the tool reports",
			`{"hook_event_name":"PostToolUse","tool_name":"ScheduleWakeup","tool_use_id":"tu1","tool_input":{"delaySeconds":180,"reason":"waiting","prompt":"check"},"tool_response":{"scheduledFor":1792756440000}}`,
			ScheduleCall{Wakeup: true, ID: "tu1", Prompt: "check", Reason: "waiting", NextAt: time.UnixMilli(1792756440000)}, true,
		},
		{
			"wakeup without a time falls back to the delay",
			`{"hook_event_name":"PostToolUse","tool_name":"ScheduleWakeup","tool_use_id":"tu1","tool_input":{"delaySeconds":90,"prompt":"check"}}`,
			ScheduleCall{Wakeup: true, ID: "tu1", Prompt: "check", NextAt: now.Add(90 * time.Second)}, true,
		},
		{
			"cron takes its id and first run from the response",
			`{"hook_event_name":"PostToolUse","tool_name":"CronCreate","tool_input":{"cron":"*/3 * * * *","prompt":"poll","recurring":true},"tool_response":{"id":"12c43486","recurring":true,"durable":false,"nextFireAt":1794571746597.5718}}`,
			ScheduleCall{ID: "12c43486", Cron: "*/3 * * * *", Prompt: "poll", Recurring: true, NextAt: time.UnixMilli(1794571746597)}, true,
		},
		{
			"cron without an id in the response was not created",
			`{"hook_event_name":"PostToolUse","tool_name":"CronCreate","tool_input":{"cron":"*/3 * * * *","prompt":"poll"},"tool_response":"error"}`,
			ScheduleCall{}, false,
		},
		{
			"cron delete",
			`{"hook_event_name":"PostToolUse","tool_name":"CronDelete","tool_input":{"id":"12c43486"},"tool_response":{"id":"12c43486"}}`,
			ScheduleCall{Delete: true, ID: "12c43486"}, true,
		},
		{
			"other tools",
			`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`,
			ScheduleCall{}, false,
		},
		{
			"before the tool ran",
			`{"hook_event_name":"PreToolUse","tool_name":"ScheduleWakeup","tool_input":{"delaySeconds":90,"prompt":"check"}}`,
			ScheduleCall{}, false,
		},
	} {
		got, ok := scheduleEvent(t, c.line).ScheduleCall(now)
		if ok != c.ok || !got.NextAt.Equal(c.want.NextAt) {
			t.Errorf("%s: got %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
			continue
		}
		got.NextAt, c.want.NextAt = time.Time{}, time.Time{}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestNextCron(t *testing.T) {
	from := time.Date(2026, 11, 16, 10, 7, 30, 0, time.UTC)
	for expr, want := range map[string]time.Time{
		"*/5 * * * *":   time.Date(2026, 11, 16, 10, 10, 0, 0, time.UTC),
		"0 9 * * *":     time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		"30 10 17 11 *": time.Date(2026, 11, 17, 10, 30, 0, 0, time.UTC),
	} {
		if got, ok := NextCron(expr, from); !ok || !got.Equal(want) {
			t.Errorf("NextCron(%q) = %v, %v; want %v", expr, got, ok, want)
		}
	}
	if _, ok := NextCron("not a cron", from); ok {
		t.Error("an invalid expression has a next run")
	}
}
