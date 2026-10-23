package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/linear"
	"github.com/aapzu/drover/internal/store"
)

// An agent imported while it runs in another terminal is mirrored, never
// duplicated, and is resumed on the board in its own directory once it exits.
func TestImportRunningAgentLifecycle(t *testing.T) {
	root, log := setupProject(t)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	os.MkdirAll(filepath.Join(claudeDir, "sessions"), 0o755)
	agent := exec.Command("sleep", "60")
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	defer agent.Process.Kill()
	register := func(pid int) string {
		path := filepath.Join(claudeDir, "sessions", fmt.Sprintf("%d.json", pid))
		entry, _ := json.Marshal(claude.Live{PID: pid, SessionID: "abcdef12-live", Cwd: root, Status: "busy"})
		os.WriteFile(path, entry, 0o644)
		return path
	}
	registry := register(agent.Process.Pid)

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	found, _ := e.DiscoverSessions(40)
	if len(found) != 1 || !found[0].Running {
		t.Fatalf("discover = %+v", found)
	}
	e.ImportSessions(found)
	task := e.Snapshot().Tasks[0]
	if task.ColumnID != "exec" || task.WorktreePath != root || task.Sessions[store.MainSession].Status != store.StatusExternal {
		t.Fatalf("imported card wrong: column=%s worktree=%s", task.ColumnID, task.WorktreePath)
	}
	if again, _ := e.DiscoverSessions(40); len(again) != 0 {
		t.Errorf("imported session offered again: %+v", again)
	}
	if n := e.ImportSessions(found); n != 0 || len(e.Snapshot().Tasks) != 1 {
		t.Errorf("re-importing the same session added %d cards", n)
	}

	var ext *ExternalError
	if err := e.Move(task.ID, "done", MoveOptions{}); !errors.As(err, &ext) {
		t.Errorf("move to Done of a live external agent: err = %v", err)
	}
	if err := e.Start(task.ID); !errors.As(err, &ext) {
		t.Errorf("start of a live external agent: err = %v", err)
	}

	// A registry entry missing for a moment must not release a live agent.
	os.Remove(registry)
	time.Sleep(2 * time.Second)
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.Status != store.StatusExternal {
		t.Fatalf("live agent released when its registry entry vanished: %s", s.Status)
	}
	agent.Process.Kill()
	agent.Wait()
	waitFor(t, e, "external agent to be released", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Status == store.StatusSuspended
	})

	other := exec.Command("sleep", "60")
	other.Start()
	defer other.Process.Kill()
	exited := make(chan struct{})
	go func() { other.Wait(); close(exited) }()
	register(other.Process.Pid)
	if err := e.Start(task.ID); !errors.As(err, &ext) {
		t.Errorf("start while resumed elsewhere: err = %v", err)
	}
	if err := e.TakeOver(task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("take over left the other terminal's agent running")
	}
	waitFor(t, e, "agent resumed with the imported id", func(*store.Task) bool {
		return strings.Contains(readFile(log), "--resume\nabcdef12-live")
	})
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("project root went missing: %v", err)
	}
}

func TestImportLinearIssue(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	issue := linear.Issue{ID: "i1", Identifier: "DX-1", Title: "Speed up CI", Description: "CI takes 40 minutes.",
		URL: "https://linear.app/x/issue/DX-1", BranchName: "aapeli/dx-1-speed-up-ci"}
	e.ImportIssues([]linear.Issue{issue, issue})
	snap := e.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("want one card, got %d", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.Branch != "aapeli/dx-1-speed-up-ci" || task.Issue.Key != "DX-1" || !strings.Contains(task.Prompt, "CI takes 40 minutes.") {
		t.Errorf("card wrong: %+v", task)
	}
	got := e.render(task, "{{issueKey}} {{issueUrl}}", groupContext{to: config.Column{}}, false)
	if got != "DX-1 https://linear.app/x/issue/DX-1" {
		t.Errorf("issue placeholders = %q", got)
	}
}

func TestImportSplitsSessionThatOpenedSeveralPRs(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(prBoard), 0o644)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	created := prCreatedLine
	path := claude.TranscriptPath("sid", root)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(created(1)+created(2)+created(1)), 0o644)

	found := []claude.Found{
		{SessionID: "sid", Title: "Auth", Cwd: root, TranscriptPath: path},
		{SessionID: "plain", Title: "Chat", Cwd: root},
	}
	if n := e.ImportSessions(found); n != 3 {
		t.Fatalf("ImportSessions added %d cards, want 3", n)
	}
	tasks := e.Snapshot().Tasks
	if len(tasks) != 3 || tasks[0].PR == nil || tasks[0].PR.Number != 1 {
		t.Fatalf("session card did not take PR one: %+v", tasks)
	}
	if tasks[1].PR != nil || tasks[1].GroupID != "" {
		t.Errorf("session without PRs changed: %+v", tasks[1])
	}
	if split := tasks[2]; split.GroupID != tasks[0].ID || split.PR == nil || split.PR.Number != 2 {
		t.Errorf("PR two is not split off the session card: %+v", split)
	}
	if n := e.ImportSessions(found); n != 0 || len(e.Snapshot().Tasks) != 3 {
		t.Errorf("re-importing added %d cards", n)
	}
}

func TestDiscoverSessionsSkipsTheAssistantsConversation(t *testing.T) {
	root, _ := setupProject(t)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	os.MkdirAll(filepath.Join(claudeDir, "sessions"), 0o755)
	const assistantID = "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"
	register := func(id string) {
		agent := exec.Command("sleep", "60")
		if err := agent.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })
		path := filepath.Join(claudeDir, "sessions", fmt.Sprintf("%d.json", agent.Process.Pid))
		entry, _ := json.Marshal(claude.Live{PID: agent.Process.Pid, SessionID: id, Cwd: root, Status: "busy"})
		os.WriteFile(path, entry, 0o644)
	}
	register(assistantID)
	register("abcdef12-other")
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(root), "assistant-session"), []byte(assistantID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	found, _ := e.DiscoverSessions(40)
	if len(found) != 1 || found[0].SessionID != "abcdef12-other" {
		t.Fatalf("discover = %+v; the assistant's own conversation must not be offered for import", found)
	}
}
