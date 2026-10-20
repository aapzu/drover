package claude

import (
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
