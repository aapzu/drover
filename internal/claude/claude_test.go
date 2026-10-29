package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWriteSettingsAllowsEnterWorktree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := WriteSettings(path, "/bin/drover", "/tmp/events.jsonl"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Permissions.Allow, "EnterWorktree") {
		t.Errorf("allow = %v, want EnterWorktree", got.Permissions.Allow)
	}
}

func TestWriteSettingsRegistersStopFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := WriteSettings(path, "/bin/drover", "/tmp/events.jsonl"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Hooks["StopFailure"]; !ok {
		t.Errorf("hooks = %v, want StopFailure", got.Hooks)
	}
}

func TestReadEventsKeepsPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	RecordHook(path, strings.NewReader(`{"hook_event_name":"UserPromptSubmit"}`))
	RecordHook(path, strings.NewReader("{\n  \"hook_event_name\": \"Stop\",\n  \"last_assistant_message\": \"ok\"\n}"))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"hook_event_name":"Pre`)
	f.Close()

	events, offset, err := ReadEvents(path, 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("got %d events, err %v", len(events), err)
	}
	if events[1].Name != "Stop" || events[1].Reply() != "ok" {
		t.Errorf("bad stop event: %+v", events[1])
	}
	again, offset2, _ := ReadEvents(path, offset)
	if len(again) != 0 || offset2 != offset {
		t.Errorf("partial line was consumed: %d events, offset %d→%d", len(again), offset, offset2)
	}
}

func TestLastAssistantText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(path, []byte(strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"final answer"},{"type":"tool_use"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}`,
		`{"type":"user","message":{"content":"hi"}}`,
	}, "\n")), 0o644)
	if got := LastAssistantText(path); got != "final answer" {
		t.Errorf("got %q", got)
	}
}

func TestArgv(t *testing.T) {
	fresh := Launch{SettingsPath: "s.json", SessionID: "id", PermissionMode: "plan", Model: "opus", Effort: "xhigh", Prompt: "-do it"}.Argv()
	want := []string{"claude", "--settings", "s.json", "--session-id", "id", "--permission-mode", "plan", "--model", "opus", "--effort", "xhigh", "--", "-do it"}
	if !slices.Equal(fresh, want) {
		t.Errorf("fresh argv = %v", fresh)
	}
	resume := Launch{SettingsPath: "s.json", SessionID: "id", Resume: true, Prompt: "ignored"}.Argv()
	if !slices.Equal(resume, []string{"claude", "--settings", "s.json", "--resume", "id"}) {
		t.Errorf("resume argv = %v", resume)
	}
	system := Launch{SettingsPath: "s.json", SessionID: "id", Resume: true, SystemPrompt: "ctx"}.Argv()
	if !slices.Equal(system, []string{"claude", "--settings", "s.json", "--resume", "id", "--append-system-prompt", "ctx"}) {
		t.Errorf("system prompt argv = %v", system)
	}
	both := Launch{SettingsPath: "s.json", SessionID: "id", SystemPrompt: "ctx", Prompt: "do it"}.Argv()
	if !slices.Equal(both, []string{"claude", "--settings", "s.json", "--session-id", "id", "--append-system-prompt", "ctx", "--", "do it"}) {
		t.Errorf("system prompt should precede the task prompt: %v", both)
	}
}

func TestBoardPrompt(t *testing.T) {
	got := BoardPrompt("/opt/it's/drover")
	for _, want := range []string{"'/opt/it'\\''s/drover' move $DROVER_TASK_NUMBER <column>", "'/opt/it'\\''s/drover' list"} {
		if !strings.Contains(got, want) {
			t.Errorf("BoardPrompt is missing %q:\n%s", want, got)
		}
	}
}

func TestCreatedPRs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	created := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create --draft --title x"},` +
		`"tool_response":{"stdout":"https://github.com/o/r/pull/12\nhttps://github.com/o/r/pull/12\n","stderr":""}}`
	viewed := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr view 7"},` +
		`"tool_response":{"stdout":"https://github.com/o/r/pull/7"}}`
	stacked := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh stack submit"},` +
		`"tool_response":{"stdout":"https://github.com/o/r/pull/13 https://github.com/o/r.js/pull/14"}}`
	for _, line := range []string{created, viewed, stacked} {
		RecordHook(path, strings.NewReader(line))
	}
	events, _, _ := ReadEvents(path, 0)
	var got []string
	for _, ev := range events {
		got = append(got, ev.CreatedPRs()...)
	}
	want := []string{"https://github.com/o/r/pull/12", "https://github.com/o/r/pull/13", "https://github.com/o/r.js/pull/14"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTranscriptPRs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	pr := func(n int, action string) string {
		return fmt.Sprintf(`{"type":"user","toolUseResult":{"stdout":"","gitOperation":{"pr":{"number":%d,"url":"https://github.com/o/r/pull/%d","action":%q}}}}`, n, n, action)
	}
	os.WriteFile(path, []byte(strings.Join([]string{
		pr(1, "created"),
		`{"type":"user","toolUseResult":"plain text"}`,
		pr(2, "viewed"),
		pr(1, "created"),
		"",
	}, "\n")+pr(3, "created")), 0o644)

	urls, offset, err := TranscriptPRs(path, 0)
	if err != nil || !slices.Equal(urls, []string{"https://github.com/o/r/pull/1"}) {
		t.Fatalf("got %v, err %v", urls, err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n")
	f.Close()
	urls, _, _ = TranscriptPRs(path, offset)
	if !slices.Equal(urls, []string{"https://github.com/o/r/pull/3"}) {
		t.Errorf("after offset got %v", urls)
	}
}

func TestBackgroundAgentAndDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	background := `{"hook_event_name":"PostToolUse","tool_name":"Agent","tool_use_id":"toolu_1",` +
		`"tool_response":{"isAsync":true,"status":"async_launched","agentId":"a1"}}`
	foreground := `{"hook_event_name":"PostToolUse","tool_name":"Agent","tool_use_id":"toolu_2",` +
		`"tool_response":{"status":"completed","content":[]}}`
	for _, line := range []string{background, foreground} {
		RecordHook(path, strings.NewReader(line))
	}
	events, _, _ := ReadEvents(path, 0)
	var got []string
	for _, ev := range events {
		if id, ok := ev.BackgroundAgent(); ok {
			got = append(got, id)
		}
	}
	if !slices.Equal(got, []string{"toolu_1"}) {
		t.Fatalf("background agents %v, want [toolu_1]", got)
	}

	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(transcript, []byte(strings.Join([]string{
		`{"type":"user","message":{"content":"hi"}}`,
		`{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>a1</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n</task-notification>"}`,
		`{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>b1</task-id>\n</task-notification>"}`,
		"",
	}, "\n")), 0o644)
	ids, offset, err := BackgroundDone(transcript, 0)
	if err != nil || !slices.Equal(ids, []string{"a1", "toolu_1", "b1"}) {
		t.Fatalf("done %v, err %v", ids, err)
	}
	ids, _, _ = BackgroundDone(transcript, offset)
	if len(ids) != 0 {
		t.Errorf("after offset got %v", ids)
	}
}

func TestRunningBackgrounds(t *testing.T) {
	cases := []struct {
		name   string
		tasks  []BackgroundTask
		want   []string
		listed bool
	}{
		{"field absent", nil, nil, false},
		{"no tasks listed", []BackgroundTask{}, nil, true},
		{"one running", []BackgroundTask{{ID: "a", Status: "running"}}, []string{"a"}, true},
		{"one completed", []BackgroundTask{{ID: "a", Status: "completed"}}, nil, true},
		{"running without id", []BackgroundTask{{Status: "running"}}, nil, true},
		{"one running among completed", []BackgroundTask{{ID: "a", Status: "completed"}, {ID: "b", Status: "running"}}, []string{"b"}, true},
	}
	for _, c := range cases {
		got, listed := (Event{BackgroundTasks: c.tasks}).RunningBackgrounds()
		if !slices.Equal(got, c.want) || listed != c.listed {
			t.Errorf("%s: RunningBackgrounds() = %v, %v, want %v, %v", c.name, got, listed, c.want, c.listed)
		}
	}
}

func TestAsksForPR(t *testing.T) {
	for prompt, want := range map[string]bool{
		"Looks good, open a PR":                      true,
		"commit, push and create a pull request":     true,
		"Please make PRs for both repos.":            true,
		"Fix the tests. Then open a draft PR":        true,
		"don't open a PR yet":                        false,
		"Do not create the pull request":             false,
		"review the PR comments":                     false,
		"/pr-review-toolkit:review-pr":               false,
		"open the file and check the price":          false,
		"open app.go":                                false,
		"make sure the PR checks pass":               false,
		"open the PR and read the comments":          false,
		"open PR #12 in the browser":                 false,
		"file a bug about the PR":                    false,
		"Don’t open a PR yet":                        false,
		"Do not, under any circumstances, open a PR": false,
		"Wait before you open a PR":                  false,
	} {
		if got := AsksForPR(prompt); got != want {
			t.Errorf("AsksForPR(%q) = %v, want %v", prompt, got, want)
		}
	}
}

func TestReviewedPRs(t *testing.T) {
	const a, b = "https://github.com/o/r/pull/12", "https://github.com/o/r/pull/13"
	for prompt, want := range map[string][]string{
		"/code-review " + a: {a},
		"/pr-review-toolkit:review-pr " + a + " " + b + " " + a: {a, b},
		"review " + a + " please":                               {a},
		"Reviewing " + a:                                        {a},
		"don't review " + a + " yet":                            nil,
		"/code-review":                                          nil,
		"review the PR comments":                                nil,
		"/simplify " + a:                                        nil,
		"open a PR for " + a:                                    nil,
		"PR " + a + " got review comments":                      nil,
		"address the review comments on " + a:                   nil,
		"please review the diff in " + a:                        {a},
		"review " + a + " and " + b:                             {a, b},
		"see how " + a + " was reviewed":                        nil,
		"review the plan. Then look at " + a:                    nil,
		"open a PR, then review " + b:                           {b},
	} {
		if got := ReviewedPRs(prompt); !slices.Equal(got, want) {
			t.Errorf("ReviewedPRs(%q) = %v, want %v", prompt, got, want)
		}
	}
}

func TestScreenMode(t *testing.T) {
	for screen, want := range map[string]string{
		"> fix it\n\n  plan mode is great\n" + strings.Repeat("\n", 8) + "────\n> \n────\n  ⏸ plan mode on (shift+tab to cycle)\n\n": "plan",
		"────\n> \n────\n  ⏵⏵ accept edits on (shift+tab to cycle)":                                                                  "acceptEdits",
		"────\n> \n────\n  ⏸ manual mode on · ? for shortcuts":                                                                       "default",
		"we discussed auto mode earlier\n" + strings.Repeat("line\n", 10) + "────\n> \n────\n  ? for shortcuts":                      "",
	} {
		if got := ScreenMode(screen); got != want {
			t.Errorf("ScreenMode(%q) = %q, want %q", screen, got, want)
		}
	}
}

func TestInterrupted(t *testing.T) {
	turnStart := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	interrupt := func(at string) string {
		return `{"type":"user","timestamp":"` + at + `","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`
	}
	fresh, stale := interrupt("2026-10-19T12:00:05.000Z"), interrupt("2026-10-19T11:59:00.000Z")
	cases := []struct {
		name    string
		lines   []string
		partial string
		want    bool
	}{
		{"interrupted", []string{`{"type":"assistant","message":{"content":[]}}`, fresh, `{"type":"file-history-snapshot"}`, `{"type":"last-prompt"}`}, "", true},
		{"tool use", []string{`{"type":"user","message":{"content":[{"type":"tool_result","content":"x"},{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`}, "", true},
		{"no timestamp", []string{`{"type":"user","message":{"content":"[Request interrupted by user]"}}`}, "", true},
		{"left over from an earlier turn", []string{stale}, "", false},
		{"new prompt", []string{fresh, `{"type":"user","message":{"content":"go on"}}`}, "", false},
		{"new reply", []string{fresh, `{"type":"assistant","message":{"content":[]}}`}, "", false},
		{"tool result", []string{fresh, `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`}, "", false},
		{"sidechain", []string{fresh, `{"type":"assistant","isSidechain":true,"message":{"content":[]}}`}, "", true},
		{"reply being written", []string{fresh}, `{"type":"assistant","mess`, false},
		{"empty", nil, "", false},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "t.jsonl")
		data := strings.Join(c.lines, "\n")
		if len(c.lines) > 0 {
			data += "\n"
		}
		if err := os.WriteFile(path, []byte(data+c.partial), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := Interrupted(path, turnStart); got != c.want {
			t.Errorf("%s: Interrupted = %v, want %v", c.name, got, c.want)
		}
	}
	if Interrupted(filepath.Join(t.TempDir(), "missing.jsonl"), turnStart) {
		t.Error("a missing transcript is not interrupted")
	}
}

// fakeClaude puts a `claude` executable on PATH that records its arguments and
// working directory and answers with reply, exiting with status.
func fakeClaude(t *testing.T, reply string, status int) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$PWD\" \"$@\" > %s\nprintf '%%s' %s\nexit %d\n", ShellQuote(argsFile), ShellQuote(reply), status)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func TestAskHaiku(t *testing.T) {
	argsFile := fakeClaude(t, "  an answer\n", 0)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := AskHaiku(context.Background(), dir, "what?")
	if err != nil || got != "an answer" {
		t.Fatalf("got %q, err %v", got, err)
	}
	recorded, _ := os.ReadFile(argsFile)
	want := strings.Join([]string{dir, "-p", "--no-session-persistence", "--model", "haiku", "--permission-mode", "plan", "what?"}, "\n") + "\n"
	if string(recorded) != want {
		t.Errorf("claude ran with:\n%s\nwant:\n%s", recorded, want)
	}
}

func TestAskHaikuFailure(t *testing.T) {
	fakeClaude(t, "", 3)
	if _, err := AskHaiku(context.Background(), t.TempDir(), "what?"); err == nil {
		t.Error("a failing claude must return an error")
	}
}

func TestEvalCondition(t *testing.T) {
	cases := []struct {
		answer string
		want   bool
	}{
		{"YES", true},
		{"yes, it does", true},
		{"  Yes\n", true},
		{"NO", false},
		{"not really", false},
		{"", false},
	}
	for _, c := range cases {
		argsFile := fakeClaude(t, c.answer, 0)
		got, err := EvalCondition(context.Background(), t.TempDir(), "tests pass", "all green")
		if err != nil || got != c.want {
			t.Errorf("answer %q: got %v, err %v, want %v", c.answer, got, err, c.want)
		}
		recorded, _ := os.ReadFile(argsFile)
		for _, part := range []string{"Condition: tests pass", "<message>\nall green\n</message>"} {
			if !strings.Contains(string(recorded), part) {
				t.Errorf("prompt is missing %q:\n%s", part, recorded)
			}
		}
	}
}

func TestEvalConditionFailure(t *testing.T) {
	fakeClaude(t, "YES", 1)
	got, err := EvalCondition(context.Background(), t.TempDir(), "c", "r")
	if err == nil || got || !strings.Contains(err.Error(), "condition check failed") {
		t.Errorf("got %v, err %v", got, err)
	}
}
