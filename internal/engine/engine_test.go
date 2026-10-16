package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aapelihaanpuu/kangentic-tui/internal/store"
	"github.com/aapelihaanpuu/kangentic-tui/internal/tmux"
)

// fakeClaude stands in for the claude CLI: it records its argv and writes the
// hook events Claude would, replying "Verdict: Ready" to /code-review and
// "Findings: applied" to anything else, including messages typed into it.
const fakeClaude = `#!/bin/sh
settings=""; sid=""; prompt=""
for a in "$@"; do printf '%s\n' "$a"; done >> "$FAKE_LOG"
echo "---" >> "$FAKE_LOG"
while [ $# -gt 0 ]; do
  case "$1" in
    --settings) settings=$2; shift 2;;
    --session-id|--resume) sid=$2; shift 2;;
    --) prompt=$2; shift 2;;
    *) shift;;
  esac
done
events="$(dirname "$settings")/events.jsonl"
emit() { printf '%s\n' "$1" >> "$events"; }
turn() {
  emit '{"hook_event_name":"UserPromptSubmit"}'
  sleep 0.2
  case "$1" in
    *PLAN*) emit '{"hook_event_name":"PostToolUse","tool_name":"ExitPlanMode"}'; sleep 1;;
    *SLOW*) sleep 3;;
  esac
  case "$1" in
    /code-review*) reply='Looks fine.\nVerdict: Ready';;
    *) reply='Did the work.\n\nFindings: applied';;
  esac
  emit "{\"hook_event_name\":\"Stop\",\"last_assistant_message\":\"$reply\"}"
}
emit "{\"hook_event_name\":\"SessionStart\",\"session_id\":\"$sid\"}"
[ -n "$prompt" ] && turn "$prompt"
while IFS= read -r line; do echo "typed:$line" >> "$FAKE_LOG"; turn "$line"; done
`

const testBoard = `{
  "version": 1,
  "columns": [
    {"id": "todo", "name": "To Do", "role": "todo"},
    {"id": "exec", "name": "Executing", "autoSpawn": true, "permissionMode": "acceptEdits", "modelOverride": "opus",
     "automations": {"onTurnEnd": [
       {"name": "On to review", "type": "move_to_column", "finalLine": "Findings: applied", "targetColumn": "Review", "maxRuns": 2}
     ]}},
    {"id": "review", "name": "Review", "autoSpawn": true, "sessionTarget": "isolated", "sessionSpawnStrategy": "always_spawn_new",
     "automations": {
       "onEnter": [{"name": "Review", "type": "send_message", "message": "/code-review {{baseBranch}}", "mode": "immediate"}],
       "onTurnEnd": [{"name": "Finish", "type": "move_to_column", "finalLine": "Verdict: Ready", "targetColumn": "Done"}]
     }},
    {"id": "done", "name": "Done", "role": "done"}
  ]
}`

func setupProject(t *testing.T) (root, log string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmp := t.TempDir()
	root = filepath.Join(tmp, "repo")
	bin := filepath.Join(tmp, "bin")
	log = filepath.Join(tmp, "claude.log")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755)

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", log)
	tmux.Socket = fmt.Sprintf("kt-test-%d", os.Getpid())
	t.Cleanup(func() { exec.Command("tmux", "-L", tmux.Socket, "kill-server").Run() })

	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	os.WriteFile(filepath.Join(root, "kangentic.json"), []byte(testBoard), 0o644)
	return root, log
}

func waitFor(t *testing.T, e *Engine, what string, cond func(*store.Task) bool) *store.Task {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		snap := e.Snapshot()
		if len(snap.Tasks) == 1 && cond(snap.Tasks[0]) {
			return snap.Tasks[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	snap := e.Snapshot()
	var logs []string
	for _, l := range snap.Tasks[0].Log {
		logs = append(logs, l.Text)
	}
	t.Fatalf("timed out waiting for %s; column=%s log:\n%s", what, snap.Tasks[0].ColumnID, strings.Join(logs, "\n"))
	return nil
}

func TestPipelineRunsOnAutomations(t *testing.T) {
	root, log := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	task := e.CreateTask("Fix the login bug", "Users cannot log in")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := e.Snapshot().Tasks[0]
	if moved.WorktreePath != filepath.Join(root, ".kangentic", "worktrees", "1") {
		t.Errorf("worktree path = %q", moved.WorktreePath)
	}
	if !strings.HasPrefix(moved.Branch, "fix-the-login-bug-") {
		t.Errorf("branch = %q", moved.Branch)
	}

	done := waitFor(t, e, "task to reach Done", func(t *store.Task) bool { return t.ColumnID == "done" && t.WorktreePath == "" })
	if done.WorktreePath != "" {
		for _, l := range done.Log {
			t.Log(l.Text)
		}
		t.Errorf("worktree not removed: %s", done.WorktreePath)
	}
	if _, err := os.Stat(filepath.Join(root, ".kangentic", "worktrees", "1")); !os.IsNotExist(err) {
		t.Errorf("worktree directory still exists")
	}
	for key, s := range done.Sessions {
		if s.Status != store.StatusSuspended {
			t.Errorf("session %s status = %s", key, s.Status)
		}
	}
	if len(done.Sessions) != 2 {
		t.Errorf("want main + isolated review session, got %d", len(done.Sessions))
	}

	argv, _ := os.ReadFile(log)
	launches := strings.Split(strings.TrimSuffix(strings.TrimSpace(string(argv)), "---"), "---")
	if len(launches) != 2 {
		t.Fatalf("want 2 launches, got %d:\n%s", len(launches), argv)
	}
	first, second := launches[0], launches[1]
	for _, want := range []string{"--permission-mode\nacceptEdits", "--model\nopus", "--session-id", "Fix the login bug\n\nUsers cannot log in"} {
		if !strings.Contains(first, want) {
			t.Errorf("executing launch missing %q:\n%s", want, first)
		}
	}
	if !strings.Contains(second, "/code-review main") || !strings.Contains(second, "--session-id") {
		t.Errorf("review launch should start fresh with the enter message:\n%s", second)
	}
}

func TestMoveMidTurnRespawnsWithNewModeWhenTurnEnds(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "kangentic.json"), []byte(`{"version":1,"columns":[
	  {"id":"todo","name":"To Do","role":"todo"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"permissionMode":"auto"},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	task := e.CreateTask("SLOW work", "")
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the planning turn to start", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityThinking
	})
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; !s.RespawnPending || s.PermissionMode != "plan" {
		t.Fatalf("a mid-turn move should defer the respawn: pending=%v mode=%s", s.RespawnPending, s.PermissionMode)
	}
	waitFor(t, e, "respawn in auto mode", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.PermissionMode == "auto" && !s.RespawnPending && s.Status == store.StatusRunning
	})

	argv, _ := os.ReadFile(log)
	launches := strings.Split(strings.TrimSuffix(strings.TrimSpace(string(argv)), "---"), "---")
	if len(launches) != 2 {
		t.Fatalf("want 2 launches, got %d:\n%s", len(launches), argv)
	}
	if !strings.Contains(launches[1], "--permission-mode\nauto") || !strings.Contains(launches[1], "--resume") {
		t.Errorf("respawn should resume in auto mode:\n%s", launches[1])
	}
}

func TestSendMessageAndMoveBackToTodo(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "kangentic.json"), []byte(`{"version":1,"columns":[
	  {"id":"todo","name":"To Do","role":"todo"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	task := e.CreateTask("Typed messages", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle && s.Started
	})
	if err := e.Send(task.ID, "- keep going;"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "reply to typed message", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s.Activity == store.ActivityIdle && s.EventsOffset > 0 && strings.Count(readFile(e.sessionDir(t, store.MainSession)+"/events.jsonl"), "Stop") == 2
	})

	if typed := readFile(os.Getenv("FAKE_LOG")); !strings.Contains(typed, "typed:- keep going;") {
		t.Errorf("typed message arrived garbled:\n%s", typed)
	}

	os.WriteFile(filepath.Join(root, ".kangentic", "worktrees", "1", "wip.txt"), []byte("x"), 0o644)
	if err := e.Move(task.ID, "done", MoveOptions{}); err != ErrDirty {
		t.Fatalf("move to Done with dirty worktree: err = %v, want ErrDirty", err)
	}
	if err := e.Move(task.ID, "todo", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	back := e.Snapshot().Tasks[0]
	if back.WorktreePath != "" || len(back.Sessions) != 0 {
		t.Errorf("To Do should reset the task: worktree=%q sessions=%d", back.WorktreePath, len(back.Sessions))
	}
	if tmux.Has(fmt.Sprintf("repo-1-%s", task.ID[:6])) {
		t.Errorf("tmux session still running after move to To Do")
	}
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// After the plan is approved the agent keeps going in the same turn; that
// turn's end must run the target column's automations.
func TestPlanExitHandsTurnToTarget(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "kangentic.json"), []byte(`{"version":1,"columns":[
	  {"id":"todo","name":"To Do","role":"todo"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan","planExitTarget":"Executing"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"permissionMode":"acceptEdits",
	   "automations":{"onTurnEnd":[{"type":"move_to_column","finalLine":"Findings: applied","targetColumn":"Done"}]}},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	task := e.CreateTask("PLAN the refactor", "")
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	done := waitFor(t, e, "plan exit then turn end to reach Done", func(t *store.Task) bool {
		return t.ColumnID == "done" && t.WorktreePath == ""
	})
	var moves []string
	for _, l := range done.Log {
		if strings.HasPrefix(l.Text, "Moved") {
			moves = append(moves, l.Text)
		}
	}
	if want := []string{"Moved To Do → Planning", "Moved Planning → Executing", "Moved Executing → Done"}; strings.Join(moves, "|") != strings.Join(want, "|") {
		t.Errorf("moves = %v, want %v", moves, want)
	}
}
