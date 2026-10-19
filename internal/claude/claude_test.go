package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
