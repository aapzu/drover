package engine

import (
	"context"
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	found := e.DiscoverSessions()
	if len(found) != 1 || !found[0].Running {
		t.Fatalf("discover = %+v", found)
	}
	e.ImportSessions(found)
	task := e.Snapshot().Tasks[0]
	if task.ColumnID != "exec" || task.WorktreePath != root || task.Sessions[store.MainSession].Status != store.StatusExternal {
		t.Fatalf("imported card wrong: column=%s worktree=%s", task.ColumnID, task.WorktreePath)
	}
	if again := e.DiscoverSessions(); len(again) != 0 {
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
	got := e.render(task, "{{issueKey}} {{issueUrl}}", groupContext{to: config.Column{}}, "")
	if got != "DX-1 https://linear.app/x/issue/DX-1" {
		t.Errorf("issue placeholders = %q", got)
	}
}
