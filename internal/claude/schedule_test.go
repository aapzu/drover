package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	wakeAt, cronAt := now.Add(3*time.Minute), now.Add(90*time.Second+597*time.Millisecond)
	for _, c := range []struct {
		name string
		line string
		want ScheduleCall
		ok   bool
	}{
		{
			"wakeup uses the time the tool reports",
			fmt.Sprintf(`{"hook_event_name":"PostToolUse","tool_name":"ScheduleWakeup","tool_use_id":"tu1","tool_input":{"delaySeconds":180,"reason":"waiting","prompt":"check"},"tool_response":{"scheduledFor":%d}}`, wakeAt.UnixMilli()),
			ScheduleCall{Wakeup: true, ID: "tu1", Prompt: "check", Reason: "waiting", NextAt: wakeAt}, true,
		},
		{
			"wakeup without a time falls back to the delay",
			`{"hook_event_name":"PostToolUse","tool_name":"ScheduleWakeup","tool_use_id":"tu1","tool_input":{"delaySeconds":90,"prompt":"check"}}`,
			ScheduleCall{Wakeup: true, ID: "tu1", Prompt: "check", NextAt: now.Add(90 * time.Second)}, true,
		},
		{
			"cron takes its id and first run from the response",
			fmt.Sprintf(`{"hook_event_name":"PostToolUse","tool_name":"CronCreate","tool_input":{"cron":"*/3 * * * *","prompt":"poll","recurring":true},"tool_response":{"id":"12c43486","recurring":true,"durable":false,"nextFireAt":%d.5718}}`, cronAt.UnixMilli()),
			ScheduleCall{ID: "12c43486", Cron: "*/3 * * * *", Prompt: "poll", Recurring: true, NextAt: cronAt}, true,
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

func TestScheduleCallToleratesWhatCodeClaudeSends(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	got, ok := scheduleEvent(t, `{"hook_event_name":"PostToolUse","tool_name":"CronCreate","tool_input":{"cron":"*/3 * * * *","prompt":"poll"},"tool_response":{"id":"c1","recurring":true}}`).ScheduleCall(now)
	if !ok || !got.NextAt.Equal(time.Date(2026, 11, 16, 10, 3, 0, 0, time.UTC)) {
		t.Errorf("a cron without nextFireAt = %+v, %v; want the next match", got, ok)
	}
	got, ok = scheduleEvent(t, `{"hook_event_name":"PostToolUse","tool_name":"CronCreate","tool_input":{"cron":"nonsense","prompt":"poll"},"tool_response":{"id":"c1","recurring":true}}`).ScheduleCall(now)
	if !ok || !got.NextAt.Equal(now.Add(unknownRun)) {
		t.Errorf("an unreadable cron = %+v, %v; want a guess", got, ok)
	}
	got, _ = scheduleEvent(t, `{"hook_event_name":"PostToolUse","tool_name":"ScheduleWakeup","tool_use_id":"tu1","tool_input":{"delaySeconds":90,"prompt":"p"},"tool_response":{"scheduledFor":1000}}`).ScheduleCall(now)
	if !got.NextAt.Equal(now.Add(90 * time.Second)) {
		t.Errorf("a time in 1970 = %v, want the delay to be used", got.NextAt)
	}
	if _, ok := scheduleEvent(t, `{"hook_event_name":"PostToolUse","tool_name":"ScheduleWakeup","tool_input":{"delaySeconds":90,"prompt":"p"}}`).ScheduleCall(now); ok {
		t.Error("a wakeup without a tool use id was tracked")
	}
	if _, ok := scheduleEvent(t, `{"hook_event_name":"PostToolUse","tool_name":"CronDelete","tool_input":{},"tool_response":{}}`).ScheduleCall(now); ok {
		t.Error("a delete without an id was tracked")
	}
}

func TestEventsWithOddToolInputsAreStillRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	lines := `{"hook_event_name":"PostToolUse","tool_name":"mcp__x__y","tool_input":{"id":123,"prompt":{"a":1},"reason":[1],"delaySeconds":"soon"}}` + "\n" +
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_response":"https://github.com/a/b/pull/1"}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	events, _, err := ReadEvents(path, 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("read %d events, %v; want both", len(events), err)
	}
	if got := events[1].CreatedPRs(); len(got) != 1 {
		t.Errorf("created PRs = %v", got)
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
