package claude

import (
	"encoding/json"
	"fmt"
	"os"
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

func TestFirstLineTruncatesOnRuneBoundary(t *testing.T) {
	if got, want := FirstLine(strings.Repeat("ä", 100)+"\nsecond"), strings.Repeat("ä", 77)+"…"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if exact := strings.Repeat("ä", 80); FirstLine(exact) != exact {
		t.Errorf("a line of exactly 80 runes was cut")
	}
}
