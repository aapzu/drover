package claude

import (
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
