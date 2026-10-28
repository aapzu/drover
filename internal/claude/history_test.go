package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTranscript(t *testing.T, config, cwd, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(config, "projects", projectDirName(cwd))
	os.MkdirAll(dir, 0o755)
	user := func(text string) string {
		b, _ := json.Marshal(map[string]any{
			"type": "user", "cwd": cwd, "gitBranch": "feature-x", "isSidechain": false,
			"message": map[string]any{"role": "user", "content": text},
		})
		return string(b)
	}
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "{") {
			out = append(out, l)
		} else {
			out = append(out, user(l))
		}
	}
	os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// writeJob records a job in Claude Code's agents view; an empty state leaves
// the directory without a state file, as dismissing the job does.
func writeJob(t *testing.T, config, dir, state string) {
	t.Helper()
	os.MkdirAll(filepath.Join(config, "jobs", dir), 0o755)
	if state != "" {
		os.WriteFile(filepath.Join(config, "jobs", dir, "state.json"), []byte(state), 0o644)
	}
}

func TestDiscover(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	root := "/work/app"

	writeTranscript(t, config, root, "11111111-old", "<command-name>/clear</command-name>", "Fix the flaky login test", `{"type":"ai-title","aiTitle":"Flaky login test"}`)
	writeTranscript(t, config, root+"/.claude/worktrees/feat", "22222222-wt", "Add dark mode")
	writeTranscript(t, config, "/work/app-other", "33333333-sib", "Not this repo")
	writeTranscript(t, config, root, "44444444-tool", `{"type":"user","cwd":"/work/app","message":{"content":[{"type":"tool_result"}]}}`)
	writeTranscript(t, config, root, "55555555-skip", "Already on the board")
	writeTranscript(t, config, root, "66666666-cmd", "<command-name>/review</command-name>\n<command-args>PR 12</command-args>")
	older := time.Now().Add(-2 * time.Hour)
	os.Chtimes(filepath.Join(config, "projects", projectDirName(root), "66666666-cmd.jsonl"), older, older)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(config, "projects", projectDirName(root), "11111111-old.jsonl"), past, past)

	writeTranscript(t, config, root, "77777777-plain", "A terminal session")
	writeTranscript(t, config, root, "88888888-gone", "Dismissed from the agents view")
	writeJob(t, config, "11111111", `{"sessionId":"11111111-old"}`)
	writeJob(t, config, "66666666", `{"sessionId":"other","resumeSessionId":"66666666-cmd"}`)
	writeJob(t, config, "88888888", "")

	os.MkdirAll(filepath.Join(config, "sessions"), 0o755)
	registry := func(pid int, id, cwd, status string) {
		b, _ := json.Marshal(Live{PID: pid, SessionID: id, Cwd: cwd, Name: "live one", Status: status})
		os.WriteFile(filepath.Join(config, "sessions", fmt.Sprintf("%d.json", pid)), b, 0o644)
	}
	registry(os.Getpid(), "22222222-wt", root+"/.claude/worktrees/feat", "busy")
	registry(99999999, "11111111-old", root, "idle") // dead process

	found, more := Discover(root, map[string]bool{"55555555-skip": true}, 10)
	if more {
		t.Error("more is set though every session fit")
	}
	if len(found) != 3 {
		t.Fatalf("got %d sessions: %+v", len(found), found)
	}
	live, old, cmd := found[0], found[1], found[2]
	if cmd.SessionID != "66666666-cmd" || cmd.Title != "/review PR 12" {
		t.Errorf("slash-command session wrong: %+v", cmd)
	}
	if live.SessionID != "22222222-wt" || !live.Running || !live.Busy || live.Title != "live one" || live.PID != os.Getpid() {
		t.Errorf("running session wrong: %+v", live)
	}
	if old.SessionID != "11111111-old" || old.Running || old.Title != "Flaky login test" ||
		old.FirstPrompt != "Fix the flaky login test" || old.Branch != "feature-x" || old.Cwd != root {
		t.Errorf("past session wrong: %+v", old)
	}
}

func TestDiscoverLimitsPastSessions(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	root := "/work/app"
	for i := range 4 {
		id := fmt.Sprintf("%08d-past", i)
		writeTranscript(t, config, root, id, "Past "+id)
		writeJob(t, config, id, `{"sessionId":"`+id+`"}`)
		at := time.Now().Add(-time.Duration(4-i) * time.Hour)
		os.Chtimes(filepath.Join(config, "projects", projectDirName(root), id+".jsonl"), at, at)
	}
	writeTranscript(t, config, root, "99999999-live", "Running one")
	writeTranscript(t, config, root, "88888888-hidden", "Not in the agents view")
	os.MkdirAll(filepath.Join(config, "sessions"), 0o755)
	b, _ := json.Marshal(Live{PID: os.Getpid(), SessionID: "99999999-live", Cwd: root, Status: "idle"})
	os.WriteFile(filepath.Join(config, "sessions", "live.json"), b, 0o644)

	var ids []string
	found, more := Discover(root, nil, 2)
	for _, f := range found {
		ids = append(ids, f.SessionID)
	}
	if want := "99999999-live 00000003-past 00000002-past"; strings.Join(ids, " ") != want || !more {
		t.Errorf("limit 2 found %v (more %v), want %s with more", ids, more, want)
	}
	if found, more := Discover(root, nil, 3); len(found) != 4 || !more {
		t.Errorf("limit 3 found %d sessions (more %v), want the running one and three past, with more", len(found), more)
	}
	if found, more := Discover(root, nil, 4); len(found) != 5 || more {
		t.Errorf("limit 4 found %d sessions (more %v), want all five and no more", len(found), more)
	}
}

func TestProjectDirName(t *testing.T) {
	got := projectDirName("/Users/me/dev/drover/.claude/worktrees/aapeli+drover")
	if want := "-Users-me-dev-drover--claude-worktrees-aapeli-drover"; got != want {
		t.Errorf("got %s", got)
	}
}

func TestMoveTranscript(t *testing.T) {
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	writeTranscript(t, config, "/repo", "abc", "fix the thing")
	from := TranscriptPath("abc", "/repo")
	if err := os.MkdirAll(filepath.Join(strings.TrimSuffix(from, ".jsonl"), "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}

	if moved, err := MoveTranscript(from, "/repo/.drover/worktrees/1"); err != nil || !moved {
		t.Fatalf("moved=%v err=%v", moved, err)
	}
	to := TranscriptPath("abc", "/repo/.drover/worktrees/1")
	for _, p := range []string{to, filepath.Join(strings.TrimSuffix(to, ".jsonl"), "subagents")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s not moved: %v", p, err)
		}
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Errorf("transcript still in the old project: %v", err)
	}
	if moved, err := MoveTranscript(TranscriptPath("missing", "/repo"), "/elsewhere"); err != nil || moved {
		t.Errorf("missing transcript: moved=%v err=%v", moved, err)
	}
}

func TestPromptText(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		text    string
		command bool
	}{
		{"plain string", `{"content":"  fix it \n"}`, "fix it", false},
		{"text block", `{"content":[{"type":"text","text":"fix it"}]}`, "fix it", false},
		{"first text block wins", `{"content":[{"type":"image"},{"type":"text","text":"one"},{"type":"text","text":"two"}]}`, "one", false},
		{"tool result only", `{"content":[{"type":"tool_result","content":"x"}]}`, "", false},
		{"system wrapper", `{"content":"<system-reminder>x</system-reminder>"}`, "", false},
		{"slash command", `{"content":"<command-name>/review</command-name>\n<command-args>PR 12</command-args>"}`, "/review PR 12", true},
		{"slash command without a leading slash or args", `{"content":"<command-name>clear</command-name>"}`, "/clear", true},
		{"content of the wrong type", `{"content":5}`, "", false},
		{"not an object", `"text"`, "", false},
		{"invalid json", `{`, "", false},
	}
	for _, c := range cases {
		text, command := promptText(json.RawMessage(c.raw))
		if text != c.text || command != c.command {
			t.Errorf("%s: promptText = %q, %v, want %q, %v", c.name, text, command, c.text, c.command)
		}
	}
}

func TestTagValue(t *testing.T) {
	cases := []struct {
		name, in, tag, value string
		ok                   bool
	}{
		{"present", "<a> x </a>", "a", "x", true},
		{"surrounded", "pre <a>x</a> post", "a", "x", true},
		{"other tag", "<b>x</b>", "a", "", false},
		{"empty", "<a></a>", "a", "", true},
	}
	for _, c := range cases {
		value, ok := tagValue(c.in, c.tag)
		if value != c.value || ok != c.ok {
			t.Errorf("%s: tagValue(%q, %q) = %q, %v", c.name, c.in, c.tag, value, ok)
		}
	}
}

func TestTagValueNeedsTheClosingTag(t *testing.T) {
	if _, ok := tagValue("<a>x", "a"); ok {
		t.Error("an unclosed tag reported ok")
	}
}

func TestFirstLine(t *testing.T) {
	long := strings.Repeat("x", 100)
	cases := []struct{ name, in, want string }{
		{"single line", "hello", "hello"},
		{"takes the first line", "one\ntwo", "one"},
		{"trims first", "\n  one\ntwo\n", "one"},
		{"exactly 80", long[:80], long[:80]},
		{"truncates", long, long[:77] + "…"},
		{"truncates whole characters", strings.Repeat("ä", 100), strings.Repeat("ä", 77) + "…"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := FirstLine(c.in); got != c.want {
			t.Errorf("%s: FirstLine = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		root, path string
		want       bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b/c/d", true},
		{"/a/b", "/a/bc", false},
		{"/a/b", "/a", false},
		{"/a/b", "/x/a/b", false},
	}
	for _, c := range cases {
		if got := Within(c.root, c.path); got != c.want {
			t.Errorf("Within(%q, %q) = %v, want %v", c.root, c.path, got, c.want)
		}
	}
}

func TestShortID(t *testing.T) {
	for in, want := range map[string]string{
		"11111111-2222-3333": "11111111",
		"12345678":           "12345678",
		"abc":                "abc",
		"":                   "",
	} {
		if got := ShortID(in); got != want {
			t.Errorf("ShortID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLiveBackground(t *testing.T) {
	for kind, want := range map[string]bool{"bg": true, "": false, "interactive": false} {
		if got := (Live{Kind: kind}).Background(); got != want {
			t.Errorf("Live{Kind: %q}.Background() = %v, want %v", kind, got, want)
		}
	}
}

func TestProcessAliveAndTerminate(t *testing.T) {
	if ProcessAlive(0) || ProcessAlive(-1) {
		t.Error("a non-positive pid is not alive")
	}
	if !ProcessAlive(os.Getpid()) {
		t.Error("the test process is alive")
	}

	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { cmd.Wait(); close(reaped) }()
	t.Cleanup(func() { cmd.Process.Kill() })

	if !ProcessAlive(cmd.Process.Pid) {
		t.Fatal("the sleep process is not alive")
	}
	if err := Terminate(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	<-reaped
	if ProcessAlive(cmd.Process.Pid) {
		t.Error("the process survived Terminate")
	}
	if err := Terminate(cmd.Process.Pid); err != nil {
		t.Errorf("terminating a gone process: %v", err)
	}
}
