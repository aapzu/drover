package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

const fakeClaude = `#!/bin/sh
if [ "$1" = "-p" ]; then
  case "$*" in *ALWAYS*) echo YES;; *ASKS_ME*) echo YES;; *"Write a title"*) printf '%s\n' "$FAKE_TITLE";; *) echo NO;; esac
  exit 0
fi
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
  case "$1" in
    *"open a PR"*) emit '{"hook_event_name":"UserPromptSubmit","prompt":"open a PR"}';;
    *TWEAK*) emit '{"hook_event_name":"UserPromptSubmit","prompt":"TWEAK the footer"}';;
    *REVIEWME*) emit '{"hook_event_name":"UserPromptSubmit","prompt":"/code-review https://github.com/o/r/pull/7"}';;
    *AUTOMATION*) emit '{"hook_event_name":"UserPromptSubmit","prompt":"AUTOMATION review"}';;
    *) emit '{"hook_event_name":"UserPromptSubmit"}';;
  esac
  sleep 0.2
  case "$1" in
    *PLAN*) emit '{"hook_event_name":"PostToolUse","tool_name":"ExitPlanMode"}'; sleep 1;;
    *SLOW*) sleep 3;;
    *"open a PR"*) sleep 1;;
    *LATEPR*)
      emit '{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create --fill"},"tool_response":{"stdout":"https://github.com/o/r/pull/1\\n"}}'
      sleep 2;;
    *PRS*)
      emit '{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create --fill"},"tool_response":{"stdout":"https://github.com/o/r/pull/1\\n"}}'
      emit '{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create --fill"},"tool_response":{"stdout":"https://github.com/o/r/pull/2\\n"}}';;
    *ONEPR*)
      emit '{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create --fill"},"tool_response":{"stdout":"https://github.com/o/r/pull/1\\n"}}';;
    *PUSH*) cp "$FAKE_PUSH" "$FAKE_GH";;
    *FORK*)
      emit "{\"hook_event_name\":\"SessionStart\",\"source\":\"fork\",\"session_id\":\"forked-$sid\"}"
      emit "{\"hook_event_name\":\"SessionStart\",\"source\":\"startup\",\"session_id\":\"blank-$sid\"}";;
  esac
  case "$1" in
    /code-review*) reply='Looks fine.\nVerdict: Ready';;
    *ASKS_ME*) reply='Ready to merge. ASKS_ME: should I?';;
    *) reply='Did the work.\n\nFindings: applied';;
  esac
  emit "{\"hook_event_name\":\"Stop\",\"last_assistant_message\":\"$reply\"}"
}
emit "{\"hook_event_name\":\"SessionStart\",\"session_id\":\"$sid\"}"
[ -n "$prompt" ] && turn "$prompt"
while IFS= read -r line; do
  echo "typed:$line" >> "$FAKE_LOG"
  case "$line" in
    */plan) echo "  ⏸ plan mode on (shift+tab to cycle)";;
    *) turn "$line";;
  esac
done
`

const testBoard = `{
  "columns": [
    {"id":"planning","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
    {"id": "exec", "name": "Executing", "autoSpawn": true, "permissionMode": "acceptEdits", "modelOverride": "opus",
     "automations": {"onTurnEnd": [
       {"name": "On to review", "type": "move_to_column", "targetColumn": "Review"}
     ]}},
    {"id": "review", "name": "Review", "autoSpawn": true,
     "automations": {
       "onEnter": [{"name": "Review", "type": "send_message", "message": "/code-review {{baseBranch}}"}],
       "onTurnEnd": [{"name": "Finish", "type": "move_to_column", "targetColumn": "Done"}]
     }},
    {"id": "done", "name": "Done", "role": "done"}
  ]
}`

var testSetup sync.Once

var notified struct {
	sync.Mutex
	got []string
}

// desktopNotified returns the notifications so far that start with prefix.
func desktopNotified(prefix string) []string {
	notified.Lock()
	defer notified.Unlock()
	var out []string
	for _, n := range notified.got {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

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
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	testSetup.Do(func() {
		tmux.Socket = fmt.Sprintf("kt-test-%d", os.Getpid())
		desktopNotify = func(title, body string) {
			notified.Lock()
			defer notified.Unlock()
			notified.got = append(notified.got, title+": "+body)
		}
	})
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
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(testBoard), 0o644)
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

// runEngine runs e until the test ends, then waits for its work to finish so
// nothing writes into the test's directories after they are removed.
func runEngine(t *testing.T, e *Engine) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func hasLog(task *store.Task, text string) bool {
	for _, l := range task.Log {
		if strings.Contains(l.Text, text) {
			return true
		}
	}
	return false
}

// waitNotBusy waits for in-flight moves to finish writing.
func waitNotBusy(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.Snapshot().Busy) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the move to finish")
}

func TestPipelineRunsOnAutomations(t *testing.T) {
	root, log := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Fix the login bug", "Users cannot log in")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := e.Snapshot().Tasks[0]
	if moved.WorktreePath != filepath.Join(root, ".drover", "worktrees", "drover-1") {
		t.Errorf("worktree path = %q", moved.WorktreePath)
	}
	if !strings.HasPrefix(moved.Branch, "fix-the-login-bug-") {
		t.Errorf("branch = %q", moved.Branch)
	}

	done := waitFor(t, e, "task to reach Done", func(t *store.Task) bool { return t.ColumnID == "done" })
	if done.WorktreePath != moved.WorktreePath || !exists(moved.WorktreePath) {
		for _, l := range done.Log {
			t.Log(l.Text)
		}
		t.Errorf("worktree not kept in Done: %q", done.WorktreePath)
	}
	if err := e.ArchiveTask(task.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if got := e.Snapshot().Tasks[0]; got.WorktreePath != "" || !got.Archived {
		t.Errorf("archiving should remove the worktree: path=%q archived=%v", got.WorktreePath, got.Archived)
	}
	if _, err := os.Stat(filepath.Join(root, ".drover", "worktrees", "drover-1")); !os.IsNotExist(err) {
		t.Errorf("worktree directory still exists")
	}
	for key, s := range done.Sessions {
		if s.Status != store.StatusSuspended {
			t.Errorf("session %s status = %s", key, s.Status)
		}
	}
	if len(done.Sessions) != 1 {
		t.Errorf("want one shared session, got %d", len(done.Sessions))
	}

	argv, _ := os.ReadFile(log)
	launches := strings.Split(strings.TrimSpace(string(argv)), "---")
	if len(launches) != 3 {
		t.Fatalf("want 2 launches, got %d:\n%s", len(launches)-1, argv)
	}
	first, second, typed := launches[0], launches[1], launches[2]
	for _, want := range []string{"--permission-mode\nacceptEdits", "--model\nopus", "--session-id", "Fix the login bug\n\nUsers cannot log in"} {
		if !strings.Contains(first, want) {
			t.Errorf("executing launch missing %q:\n%s", want, first)
		}
	}
	if sid := done.Sessions[store.MainSession].AgentSessionID; !strings.Contains(second, "--resume\n"+sid) {
		t.Errorf("review should resume the executing conversation %s:\n%s", sid, second)
	}
	if !strings.Contains(typed, "typed:/code-review main") {
		t.Errorf("review message not typed into the agent:\n%s", typed)
	}
}

func TestMoveMidTurnRespawnsWithNewModeWhenTurnEnds(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"defaultPermissionMode":"acceptEdits","columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

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
	waitFor(t, e, "respawn in the board's default mode", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.PermissionMode == "acceptEdits" && !s.RespawnPending && s.Status == store.StatusRunning
	})

	waitForAll(t, e, "the respawned agent to start", func([]*store.Task) bool {
		return strings.Count(readFile(log), "---") >= 2
	})
	argv, _ := os.ReadFile(log)
	launches := strings.Split(strings.TrimSuffix(strings.TrimSpace(string(argv)), "---"), "---")
	if len(launches) != 2 {
		t.Fatalf("want 2 launches, got %d:\n%s", len(launches), argv)
	}
	if !strings.Contains(launches[1], "--permission-mode\nacceptEdits") || !strings.Contains(launches[1], "--resume") {
		t.Errorf("respawn should resume in the board's default mode:\n%s", launches[1])
	}
}

func TestMoveToPlanInterruptsTheTurn(t *testing.T) {
	root, log := setupProject(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"defaultPermissionMode":"acceptEdits","columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Interrupt me", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	ready := waitFor(t, e, "agent to start", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle
	})
	events, err := os.OpenFile(filepath.Join(e.sessionDir(ready, store.MainSession), "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	events.WriteString(`{"hook_event_name":"UserPromptSubmit","prompt":"SLOW work"}` + "\n")
	busy := waitFor(t, e, "the turn to start", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Activity == store.ActivityThinking
	})

	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.PermissionMode != "acceptEdits" || !s.RespawnPending {
		t.Fatalf("the running turn should keep its mode until interrupted: mode=%s pending=%v", s.PermissionMode, s.RespawnPending)
	}

	transcript := claude.TranscriptPath(busy.Sessions[store.MainSession].AgentSessionID, busy.WorktreePath)
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := `{"type":"user","message":{"content":"Interrupt me"}}`
	interrupt := `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`
	if err := os.WriteFile(transcript, []byte(prompt+"\n"+interrupt+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, e, "the interrupted agent to switch to plan mode", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s.PermissionMode == "plan" && !s.RespawnPending && s.Activity == store.ActivityIdle
	})
	if got := readFile(log); strings.Count(got, "---") != 1 || !strings.Contains(got, "typed:\x1b/plan") {
		t.Errorf("want Escape then /plan typed into the same agent, not a restart:\n%s", got)
	}
}

func TestUnsetPermissionModeInheritsClaudeDefault(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"review","name":"Review","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Inherit", "")
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the planning turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle && s.Started
	})
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the agent to restart without a forced mode", func([]*store.Task) bool {
		return strings.Count(readFile(log), "---") >= 2
	})
	launches := strings.Split(readFile(log), "---")
	if !strings.Contains(launches[0], "--permission-mode\nplan") {
		t.Errorf("planning launch should force plan mode:\n%s", launches[0])
	}
	if !strings.Contains(launches[1], "--resume") || strings.Contains(launches[1], "--permission-mode") {
		t.Errorf("restart should resume with Claude's own mode:\n%s", launches[1])
	}
	for i, launch := range launches[:2] {
		if !strings.Contains(launch, "--append-system-prompt\nYou are running as an agent on a drover kanban board.") {
			t.Errorf("launch %d should carry the board prompt:\n%s", i, launch)
		}
	}

	waitFor(t, e, "the restarted agent to settle", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Activity == store.ActivityIdle
	})
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.Status != store.StatusRunning || strings.Count(readFile(log), "---") != 2 {
		t.Errorf("moving between inheriting columns should not restart the agent: status=%s\n%s", s.Status, readFile(log))
	}
}

func TestSettingsDiffer(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    store.Session
		col  config.Column
		mode string
		dir  string
		want bool
	}{
		{"inherited, hooks report a mode", store.Session{PermissionMode: "default", ModeInherited: true}, config.Column{}, "", "", false},
		{"forced plan leaving for inherited", store.Session{PermissionMode: "plan"}, config.Column{}, "", "", true},
		{"saved before ModeInherited", store.Session{PermissionMode: "auto"}, config.Column{}, "", "", true},
		{"same forced mode", store.Session{PermissionMode: "acceptEdits"}, config.Column{}, "acceptEdits", "", false},
		{"inherited entering a forced mode", store.Session{PermissionMode: "default", ModeInherited: true}, config.Column{}, "plan", "", true},
		{"model differs", store.Session{PermissionMode: "plan", Model: "opus"}, config.Column{ModelOverride: "sonnet"}, "plan", "", true},
		{"launched in the root, card has a worktree", store.Session{PermissionMode: "plan", Cwd: "/root"}, config.Column{}, "plan", "/root/wt", true},
		{"launched in the worktree", store.Session{PermissionMode: "plan", Cwd: "/root/wt"}, config.Column{}, "plan", "/root/wt", false},
		{"launch directory not recorded", store.Session{PermissionMode: "plan"}, config.Column{}, "plan", "/root/wt", false},
	} {
		if got := settingsDiffer(&tc.s, tc.col, tc.mode, tc.dir); got != tc.want {
			t.Errorf("%s: settingsDiffer = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestInheritedAgentSwitchesInPlaceAndBack(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
	  {"id":"review","name":"Review","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Round trip", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle && s.Started
	})
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(log); strings.Count(got, "---") != 1 || !strings.Contains(got, "typed:/plan") {
		t.Fatalf("want /plan typed into the running agent:\n%s", got)
	}
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the agent to restart without a forced mode", func([]*store.Task) bool {
		return strings.Count(readFile(log), "---") >= 2
	})
	if l := strings.Split(readFile(log), "---")[1]; !strings.Contains(l, "--resume") || strings.Contains(l, "--permission-mode") {
		t.Errorf("restart should resume with Claude's own mode:\n%s", l)
	}
}

func TestSendMessageAndMoveBackToTheFirstColumn(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

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

	os.WriteFile(filepath.Join(root, ".drover", "worktrees", "drover-1", "wip.txt"), []byte("x"), 0o644)
	if err := e.Move(task.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.ArchiveTask(task.ID, true, false); err != ErrDirty {
		t.Fatalf("archiving with a dirty worktree: err = %v, want ErrDirty", err)
	}
	if got := e.Snapshot().Tasks[0]; got.Archived || got.WorktreePath == "" {
		t.Fatalf("a refused archive changed the card: archived=%v worktree=%q", got.Archived, got.WorktreePath)
	}
	if err := e.Move(task.ID, "backlog", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	back := e.Snapshot().Tasks[0]
	if back.WorktreePath == "" || back.Sessions[store.MainSession] == nil || !back.Sessions[store.MainSession].Started {
		t.Errorf("moving back should keep the worktree and conversation: worktree=%q sessions=%v", back.WorktreePath, back.Sessions)
	}
	if tmux.Has(fmt.Sprintf("repo-1-%s", task.ID[:6])) {
		t.Errorf("tmux session still running in a column without an agent")
	}
}

func TestWorktreesDirSetting(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	os.WriteFile(filepath.Join(root, "drover.local.json"), []byte(`{"worktreesDir":".claude/worktrees"}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Custom dir", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".claude", "worktrees", "drover-1")
	if got := e.Snapshot().Tasks[0].WorktreePath; got != want {
		t.Fatalf("worktree path = %q, want %q", got, want)
	}
	for _, path := range []string{
		want,
		filepath.Join(want, "sub"),
		filepath.Join(root, ".drover", "worktrees", "drover-1"),
		filepath.Join(root, ".drover", "worktrees", "1"),
	} {
		if !e.ownsWorktree(path) {
			t.Errorf("%s not recognised as a board worktree", path)
		}
	}
	for _, path := range []string{
		root,
		filepath.Join(root, ".claude", "worktrees", "feat"),
		filepath.Join(root, ".claude", "worktrees", "1"),
		filepath.Join(root, ".claude", "worktrees", "drover-fix"),
		filepath.Join(root, ".claude", "worktrees", "drover-"),
	} {
		if e.ownsWorktree(path) {
			t.Errorf("%s counts as a board worktree", path)
		}
	}

	if err := e.Move(task.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.ArchiveTask(task.ID, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Errorf("worktree directory still exists after archiving")
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
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan","planExitTarget":"Executing"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"permissionMode":"acceptEdits",
	   "automations":{"onTurnEnd":[{"type":"move_to_column","targetColumn":"Done"}]}},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("PLAN the refactor", "")
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	done := waitFor(t, e, "plan exit then turn end to reach Done", func(t *store.Task) bool {
		return t.ColumnID == "done"
	})
	var moves []string
	for _, l := range done.Log {
		if strings.HasPrefix(l.Text, "Moved") {
			moves = append(moves, l.Text)
		}
	}
	if want := []string{"Moved Backlog → Planning", "Moved Planning → Executing", "Moved Executing → Done"}; strings.Join(moves, "|") != strings.Join(want, "|") {
		t.Errorf("moves = %v, want %v", moves, want)
	}
}

func TestPlanExitRestartsTheAgentInAWorktree(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan","planExitTarget":"Executing","noWorktree":true},
	  {"id":"exec","name":"Executing","autoSpawn":true,"permissionMode":"acceptEdits"},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("PLAN the refactor", "")
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, e, "the agent to run in the worktree after the plan exit", func(t *store.Task) bool {
		return t.ColumnID == "exec" && inWorktree(t)
	})
	if got.WorktreePath == root {
		t.Errorf("worktree is the project root %s", root)
	}
}

func TestStartedAgentResumesInTheWorktreeWhenOnlyTheDirectoryChanges(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan","noWorktree":true},
	  {"id":"review","name":"Review","autoSpawn":true,"permissionMode":"plan"},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Do the work", "")
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	started := waitFor(t, e, "first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	id := started.Sessions[store.MainSession].AgentSessionID
	rootTranscript := claude.TranscriptPath(id, root)
	os.MkdirAll(filepath.Dir(rootTranscript), 0o755)
	os.WriteFile(rootTranscript, []byte("{}\n"), 0o644)
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, e, "the agent to resume in the worktree", inWorktree)
	if _, err := os.Stat(claude.TranscriptPath(id, got.WorktreePath)); err != nil {
		t.Errorf("the conversation did not follow the agent into the worktree: %v", err)
	}
	if _, err := os.Stat(rootTranscript); !os.IsNotExist(err) {
		t.Errorf("the transcript is still under the project root: %v", err)
	}
	if s := got.Sessions[store.MainSession]; s.AgentSessionID != id {
		t.Errorf("session id changed from %s to %s; the conversation should resume", id, s.AgentSessionID)
	}
	waitForAll(t, e, "the agent to be resumed", func([]*store.Task) bool {
		return strings.Contains(readFile(log), "--resume\n"+id)
	})
}

func TestPRRequestMovesCard(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"self","name":"Self-review","autoSpawn":true,
	   "automations":{"onEnter":[{"type":"send_message","message":"/code-review"}],
	    "onTurnEnd":[{"type":"move_to_column","targetColumn":"Done"}]}},
	  {"id":"done","name":"Done","role":"done"}],
	  "prFlow":{"requested":"self"}}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Do the work", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	if err := e.Send(task.ID, "Looks good, open a PR"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "PR request to move the card via Self-review to Done", func(t *store.Task) bool {
		return t.ColumnID == "done"
	})
	if strings.Contains(readFile(log), "/code-review") {
		t.Error("Self-review's enter group ran on a PR request")
	}
}

func TestPromptInReviewStaysInReview(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"review","name":"In review"},
	  {"id":"done","name":"Done","role":"done"}],
	  "prFlow":{"opened":"review","changesRequested":"exec"}}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Do the work", "")
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(task.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	if got := e.Snapshot().Tasks[0].ColumnID; got != "review" {
		t.Fatalf("card left review before it was prompted: %s", got)
	}
	if err := e.Send(task.ID, "TWEAK the footer"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if got := e.Snapshot().Tasks[0].ColumnID; got != "review" {
		t.Fatalf("a prompt in review moved the card to %s", got)
	}
}

// A manual start in a column without autoSpawn can still be queued, and must
// start once a slot frees up instead of being dropped.
func TestQueuedManualStartRunsWhenSlotFrees(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"maxConcurrent":1,"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"review","name":"Review"},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	// A busy agent is never suspended to make room.
	first := e.CreateTask("First SLOW", "")
	if err := e.Move(first.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	second := e.CreateTask("Second", "")
	if err := e.Move(second.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(second.ID); err != nil {
		t.Fatal(err)
	}
	session := func() (*store.Session, string) {
		snap := e.Snapshot()
		for _, task := range snap.Tasks {
			if task.ID == second.ID {
				return task.Sessions[store.MainSession], snap.Busy[task.ID]
			}
		}
		return nil, ""
	}
	if s, _ := session(); s == nil || s.Status != store.StatusQueued {
		t.Fatalf("second agent should be queued, got %+v", s)
	}
	select {
	case notice := <-e.Notices():
		if !strings.Contains(notice, "limit 1") {
			t.Errorf("queued notice = %q", notice)
		}
	case <-time.After(time.Second):
		t.Error("no notice when the agent was queued")
	}

	// Starting from the queue has to recreate a missing worktree.
	worktree := e.Snapshot().Tasks[1].WorktreePath
	if out, err := exec.Command("git", "-C", root, "worktree", "remove", "--force", worktree).CombinedOutput(); err != nil {
		t.Fatalf("remove worktree: %s", out)
	}

	e.Suspend(first.ID)
	deadline := time.Now().Add(20 * time.Second)
	for {
		s, busy := session()
		if s.Status == store.StatusRunning && s.Activity != store.ActivityStarting && tmux.Has(s.Tmux) && busy == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued agent never started: status=%s activity=%s busy=%q", s.Status, s.Activity, busy)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

const prBoard = `{
  "columns": [
    {"id":"backlog","name":"Backlog"},
    {"id": "exec", "name": "Executing", "autoSpawn": true,
     "automations": {"onTurnEnd": [{"name": "Ship", "type": "move_to_column", "targetColumn": "Review"}]}},
    {"id": "review", "name": "Review"},
    {"id": "done", "name": "Done", "role": "done"}
  ]
}`

const fakeGh = `#!/bin/sh
case "$3" in
  */2) echo '{"title":"PR two","headRefName":"two","state":"OPEN"}';;
  *) echo '{"title":"PR one","headRefName":"one","state":"OPEN"}';;
esac
`

func TestSplitByPROffKeepsOneCard(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(prBoard), 0o644)
	os.WriteFile(filepath.Join(root, "drover.local.json"), []byte(`{"splitByPR":false}`), 0o644)
	bin := strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0]
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Refactor auth", "PRS")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}

	const second = "https://github.com/o/r/pull/2"
	waitForAll(t, e, "owner shipped to review with its first PR and the second one seen", func(tasks []*store.Task) bool {
		return tasks[0].ColumnID == "review" && tasks[0].PR != nil && slices.Contains(tasks[0].SeenPRs, second)
	})
	tasks := e.Snapshot().Tasks
	if len(tasks) != 1 || tasks[0].PR.Number != 1 {
		t.Fatalf("splitByPR off: %d cards, owner PR %+v", len(tasks), tasks[0].PR)
	}
	logged := 0
	for _, entry := range tasks[0].Log {
		if strings.Contains(entry.Text, "splitByPR is off") {
			logged++
		}
	}
	if logged != 1 {
		t.Errorf("dropped PR logged %d times, want once", logged)
	}
}

func TestSessionOpeningTwoPRsSplitsIntoCards(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(prBoard), 0o644)
	bin := strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0]
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Refactor auth", "PRS")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}

	var owner, two *store.Task
	waitForAll(t, e, "second PR split off and titled by gh, owner shipped to review", func(tasks []*store.Task) bool {
		if len(tasks) != 2 {
			return false
		}
		owner, two = tasks[0], tasks[1]
		return owner.ColumnID == "review" && two.Title == "PR two"
	})
	if owner.PR == nil || owner.PR.Number != 1 || two.PR == nil || two.PR.Number != 2 || two.PR.Branch != "two" {
		t.Fatalf("PRs: owner %+v, two %+v", owner.PR, two.PR)
	}
	if owner.Title != "Refactor auth" {
		t.Errorf("owner retitled to %q", owner.Title)
	}
	if two.GroupID != owner.ID || two.ColumnID != "exec" || len(two.Sessions) != 0 {
		t.Fatalf("PR card: group %q column %q sessions %d", two.GroupID, two.ColumnID, len(two.Sessions))
	}
	if owner.ActiveCard != two.ID {
		t.Errorf("active card = %q, want PR two's", owner.ActiveCard)
	}
	ownerTmux, ok1 := e.LiveSession(owner.ID)
	twoTmux, ok2 := e.LiveSession(two.ID)
	if !ok1 || !ok2 || ownerTmux != twoTmux {
		t.Fatalf("cards don't share a live session: %q %v, %q %v", ownerTmux, ok1, twoTmux, ok2)
	}

	waitForAll(t, e, "agent idle", func(tasks []*store.Task) bool {
		return tasks[0].Sessions[store.MainSession].Activity == store.ActivityIdle
	})
	if err := e.Send(owner.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "PR two shipped too", func(tasks []*store.Task) bool { return tasks[1].ColumnID == "review" })
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "typed:hello") {
		t.Errorf("message not delivered:\n%s", data)
	}
	waitForAll(t, e, "agent suspended with no card in an agent column", func(tasks []*store.Task) bool {
		return tasks[0].Sessions[store.MainSession].Status == store.StatusSuspended
	})

	worktree := e.Snapshot().Tasks[0].WorktreePath
	if err := e.Move(owner.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree removed while a card is still in review: %v", err)
	}
	if err := e.Move(two.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.ArchiveTask(owner.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree removed while a card is still unarchived: %v", err)
	}
	if err := e.ArchiveTask(two.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree kept after every card is archived: %v", err)
	}

	if err := e.DeleteTask(owner.ID); err != nil {
		t.Fatal(err)
	}
	tasks := e.Snapshot().Tasks
	if len(tasks) != 1 || tasks[0].ID != two.ID || tasks[0].GroupID != "" ||
		tasks[0].Sessions[store.MainSession] == nil {
		t.Fatalf("after deleting the owner: %+v", tasks)
	}
	if dir := tasks[0].Sessions[store.MainSession].Dir; !strings.Contains(dir, owner.ID) {
		t.Errorf("session dir = %q, want the original owner's", dir)
	}
}

const openedBoard = `{
  "columns": [
    {"id":"backlog","name":"Backlog"},
    {"id": "exec", "name": "Executing", "autoSpawn": true},
    {"id": "exec2", "name": "Executing too", "autoSpawn": true},
    {"id": "review", "name": "In review"},
    {"id": "done", "name": "Done", "role": "done"}
  ],
  "prFlow": {"opened": "In review"}
}`

// startWithBoard runs an engine on a fresh project with board and a gh that
// reports every PR as open.
func startWithBoard(t *testing.T, board string) *Engine {
	t.Helper()
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(board), 0o644)
	bin := strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0]
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)
	return e
}

func TestFirstPRMovesSessionCardAfterTheTurn(t *testing.T) {
	e := startWithBoard(t, openedBoard)
	task := e.CreateTask("Auth", "LATEPR")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the card to take the PR and wait for the turn to end", func(t *store.Task) bool {
		return t.PR != nil && t.PRMovePending
	})
	if tasks := e.Snapshot().Tasks; len(tasks) != 1 || tasks[0].ColumnID != "exec" {
		t.Fatalf("PR split off or card moved mid-turn: %+v", tasks)
	}
	waitFor(t, e, "the card in review once the turn ended", func(t *store.Task) bool { return t.ColumnID == "review" })
	got := e.Snapshot().Tasks
	if len(got) != 1 || got[0].PRMovePending || got[0].PR.Number != 1 || got[0].Title != "Auth" {
		t.Fatalf("after the move: %+v", got)
	}
	waitFor(t, e, "the agent suspended in review", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Status == store.StatusSuspended
	})
}

func TestMovingTheCardByHandCancelsTheOpenedMove(t *testing.T) {
	e := startWithBoard(t, openedBoard)
	task := e.CreateTask("Auth", "LATEPR")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the card to take the PR", func(t *store.Task) bool { return t.PRMovePending })
	if err := e.Move(task.ID, "exec2", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the agent to finish its turn in the other column", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Status == store.StatusRunning && s.Started && s.Activity == store.ActivityIdle
	})
	time.Sleep(time.Second)
	if got := e.Snapshot().Tasks[0]; got.ColumnID != "exec2" || got.PRMovePending {
		t.Fatalf("card in %s, pending %v", got.ColumnID, got.PRMovePending)
	}
}

func TestPendingPRMoveShipsWhenNoAgentIsRunning(t *testing.T) {
	e := startWithBoard(t, openedBoard)
	task := e.CreateTask("Auth", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	e.Suspend(task.ID)
	e.with(func() {
		card := e.find(task.ID)
		card.PR = &store.PR{URL: "https://github.com/o/r/pull/1", Number: 1}
		card.PRMovePending = true
	})
	waitFor(t, e, "the card in review without a turn end", func(t *store.Task) bool {
		return t.ColumnID == "review" && !t.PRMovePending
	})
}

func TestFirstPRStaysPutWithoutAnOpenedColumn(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`)
	task := e.CreateTask("Auth", "ONEPR")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the card to take the PR and the turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return t.PR != nil && s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	time.Sleep(time.Second)
	if tasks := e.Snapshot().Tasks; len(tasks) != 1 || tasks[0].ColumnID != "exec" || tasks[0].PRMovePending {
		t.Fatalf("cards: %+v", tasks)
	}
}

const flowBoard = `{
  "columns": [
    {"id":"backlog","name":"Backlog"},
    {"id": "exec", "name": "Executing", "autoSpawn": true},
    {"id": "review", "name": "In review"},
    {"id": "verify", "name": "Verify"},
    {"id": "done", "name": "Done", "role": "done"}
  ],
  "prFlow": {"opened": "In review", "changesRequested": "exec", "changesRequestedMessage": "FIX {{prNumber}}",
    "merged": "verify", "released": "done", "releaseBranch": "main"}
}`

func TestPRCardFollowsReviewMergeAndRelease(t *testing.T) {
	root, log := setupProject(t)
	tmp := filepath.Dir(root)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(flowBoard), 0o644)
	bin := strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0]
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ncat \"$FAKE_GH\"\n"), 0o755)
	ghFile, pushFile := filepath.Join(tmp, "gh.json"), filepath.Join(tmp, "pushed.json")
	t.Setenv("FAKE_GH", ghFile)
	t.Setenv("FAKE_PUSH", pushFile)
	pr := func(file, state, head, reviews, merge string) {
		os.WriteFile(file, []byte(fmt.Sprintf(`{"title":"PR one","headRefName":"one","state":%q,"headRefOid":%q,
		  "reviewDecision":"","latestReviews":[%s],"mergeCommit":%s}`, state, head, reviews, merge)), 0o644)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--bare", filepath.Join(tmp, "origin.git"))
	git("remote", "add", "origin", filepath.Join(tmp, "origin.git"))
	git("push", "-q", "origin", "main")
	merge := git("commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "Merge PR one")
	git("push", "-q", "origin", merge+":refs/heads/develop")

	pr(ghFile, "OPEN", "aaa", "", "null")
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	e.PRPollInterval, e.ReleaseFetchInterval = 200*time.Millisecond, 200*time.Millisecond
	runEngine(t, e)

	task := e.CreateTask("Auth", "ONEPR")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	card := func(tasks []*store.Task) *store.Task {
		if len(tasks) != 1 {
			return &store.Task{PR: &store.PR{}}
		}
		return tasks[0]
	}
	waitForAll(t, e, "the session card, holding its PR, waits in review", func(tasks []*store.Task) bool {
		c := card(tasks)
		return c.ColumnID == "review" && c.PR.Title == "PR one"
	})
	if c := card(e.Snapshot().Tasks); c.Title != "Auth" || c.PR.Number != 1 || c.GroupID != "" {
		t.Fatalf("session card: %q %+v", c.Title, c.PR)
	}
	idle := func(tasks []*store.Task) bool {
		s := tasks[0].Sessions[store.MainSession]
		return s.Activity == store.ActivityIdle && len(s.Pending) == 0
	}

	review := `{"state":"CHANGES_REQUESTED","submittedAt":"2026-10-19T10:00:00Z"}`
	pr(pushFile, "OPEN", "bbb", review, "null")
	pr(ghFile, "OPEN", "aaa", review, "null")
	waitForAll(t, e, "card sent back for fixes", func(tasks []*store.Task) bool {
		c := card(tasks)
		return c.ColumnID == "exec" && c.PR.FixFrom == "aaa" && idle(tasks)
	})
	time.Sleep(600 * time.Millisecond)
	if c := card(e.Snapshot().Tasks); c.ColumnID != "exec" {
		t.Fatalf("card left %s before the agent pushed", c.ColumnID)
	}
	if err := e.Send(task.ID, "PUSH"); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "card back in review after the fix", func(tasks []*store.Task) bool {
		c := card(tasks)
		return c.ColumnID == "review" && c.PR.FixFrom == ""
	})
	time.Sleep(600 * time.Millisecond)
	if c := card(e.Snapshot().Tasks); c.ColumnID != "review" {
		t.Fatalf("the same review sent the card to %s again", c.ColumnID)
	}
	if data, _ := os.ReadFile(log); strings.Count(string(data), "typed:FIX 1") != 1 {
		t.Errorf("review message not delivered exactly once:\n%s", data)
	}

	pr(ghFile, "MERGED", "bbb", review, fmt.Sprintf(`{"oid":%q}`, merge))
	waitForAll(t, e, "merged card in verify", func(tasks []*store.Task) bool { return card(tasks).ColumnID == "verify" })
	time.Sleep(600 * time.Millisecond)
	if c := card(e.Snapshot().Tasks); c.ColumnID != "verify" || c.PR.Released {
		t.Fatalf("released before the merge reached main: %s %+v", c.ColumnID, c.PR)
	}

	git("push", "-q", "origin", merge+":refs/heads/main")
	waitForAll(t, e, "released card done", func(tasks []*store.Task) bool {
		c := card(tasks)
		return c.ColumnID == "done" && c.PR.Released
	})
}

type mergedRig struct {
	e       *Engine
	task    *store.Task
	agent   *exec.Cmd
	pr      func(state string)
	polls   func() int
	release func()
}

func newMergedRig(t *testing.T, board string) *mergedRig {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(board), 0o644)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	os.MkdirAll(filepath.Join(claudeDir, "sessions"), 0o755)
	bin := strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0]
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho x >> \"$FAKE_GH.calls\"\ncat \"$FAKE_GH\"\n"), 0o755)
	ghFile := filepath.Join(filepath.Dir(root), "gh.json")
	t.Setenv("FAKE_GH", ghFile)
	r := &mergedRig{pr: func(state string) {
		os.WriteFile(ghFile, []byte(fmt.Sprintf(`{"title":"PR one","headRefName":"one","state":%q,"headRefOid":"aaa",
		  "reviewDecision":"","latestReviews":[],"mergeCommit":null}`, state)), 0o644)
	}, polls: func() int {
		data, _ := os.ReadFile(ghFile + ".calls")
		return strings.Count(string(data), "\n")
	}}
	r.pr("OPEN")
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	e.PRPollInterval = 200 * time.Millisecond
	runEngine(t, e)
	r.e = e

	r.task = e.CreateTask("Auth", "ONEPR")
	if err := e.Move(r.task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the card waits in review", func(tasks []*store.Task) bool {
		return len(tasks) == 1 && tasks[0].ColumnID == "review" && tasks[0].Sessions[store.MainSession].AgentSessionID != ""
	})

	// The conversation is also open in another terminal, which blocks moves
	// that would end the card.
	r.agent = exec.Command("sleep", "60")
	if err := r.agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.agent.Process.Kill() })
	sid := e.Snapshot().Tasks[0].Sessions[store.MainSession].AgentSessionID
	e.with(func() { e.find(r.task.ID).Sessions[store.MainSession].Imported = true })
	entry, _ := json.Marshal(claude.Live{PID: r.agent.Process.Pid, SessionID: sid, Cwd: root, Status: "busy"})
	os.WriteFile(filepath.Join(claudeDir, "sessions", fmt.Sprintf("%d.json", r.agent.Process.Pid)), entry, 0o644)
	r.release = func() {
		r.agent.Process.Kill()
		r.agent.Wait()
	}
	return r
}

func (r *mergedRig) holdMerged(t *testing.T) *store.Task {
	r.pr("MERGED")
	waitForAll(t, r.e, "the blocked move to be logged", func(tasks []*store.Task) bool {
		return hasLog(tasks[0], "Moving to Done failed")
	})
	polled := r.polls()
	waitForAll(t, r.e, "more polls of the merged PR", func([]*store.Task) bool { return r.polls() >= polled+3 })
	return r.e.Snapshot().Tasks[0]
}

const mergedBoard = `{
  "columns": [
    {"id":"backlog","name":"Backlog"},
    {"id": "exec", "name": "Executing", "autoSpawn": true},
    {"id": "review", "name": "In review"},
    {"id": "parked", "name": "Parked"},
    {"id": "done", "name": "Done", "role": "done"}
  ],
  "prFlow": {"opened": "In review", "merged": "done"}
}`

func TestMergedPRRetriesMoveBlockedByExternalSession(t *testing.T) {
	r := newMergedRig(t, mergedBoard)
	c := r.holdMerged(t)
	if c.ColumnID != "review" || !c.PR.MergeMovePending {
		t.Fatalf("a live external session should hold the merged card in review: %s %+v", c.ColumnID, c.PR)
	}
	blocked := 0
	for _, l := range c.Log {
		if strings.Contains(l.Text, "Moving to Done failed") {
			blocked++
		}
	}
	if blocked != 1 {
		t.Errorf("the blocked move was logged %d times, want once", blocked)
	}

	r.release()
	waitForAll(t, r.e, "the merged card to reach done", func(tasks []*store.Task) bool {
		return tasks[0].ColumnID == "done" && !tasks[0].PR.MergeMovePending
	})
}

func TestMovingAMergedCardByHandStopsTheRetry(t *testing.T) {
	r := newMergedRig(t, mergedBoard)
	r.holdMerged(t)
	if err := r.e.Move(r.task.ID, "parked", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	r.release()
	polled := r.polls()
	time.Sleep(3 * r.e.PRPollInterval)
	if c := r.e.Snapshot().Tasks[0]; c.ColumnID != "parked" || c.PR.MergeMovePending {
		t.Fatalf("the card you moved was pulled back or is still pending: %s %+v", c.ColumnID, c.PR)
	}
	if after := r.polls(); after != polled {
		t.Errorf("the merged PR was polled %d more times after you moved the card", after-polled)
	}
}

func TestMergedPRWithoutMergedColumnIsNotRetried(t *testing.T) {
	r := newMergedRig(t, `{
  "columns": [
    {"id":"backlog","name":"Backlog"},
    {"id": "exec", "name": "Executing", "autoSpawn": true},
    {"id": "review", "name": "In review"}
  ],
  "prFlow": {"opened": "In review"}
}`)
	r.pr("MERGED")
	waitForAll(t, r.e, "the merge to be seen", func(tasks []*store.Task) bool { return tasks[0].PR.State == "MERGED" })
	if c := r.e.Snapshot().Tasks[0]; c.ColumnID != "review" || c.PR.MergeMovePending {
		t.Fatalf("a merge with nowhere to go should leave the card alone: %s %+v", c.ColumnID, c.PR)
	}
}

func TestTranscriptPRsFillSessionCardThenSplit(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(prBoard), 0o644)
	bin := strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0]
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	e.PRPollInterval = 200 * time.Millisecond
	task := e.CreateTask("Auth", "")
	e.with(func() {
		task.Sessions = map[string]*store.Session{store.MainSession: {
			Key: store.MainSession, AgentSessionID: "sid", Status: store.StatusSuspended, Started: true,
		}}
	})
	created := prCreatedLine
	path := claude.TranscriptPath("sid", root)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(created(1)), 0o644)

	runEngine(t, e)

	waitForAll(t, e, "the session card to take the PR from the transcript", func(tasks []*store.Task) bool {
		return len(tasks) == 1 && tasks[0].PR != nil && tasks[0].PR.Title == "PR one"
	})
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.PRScanPath != path || s.PRScan != int64(len(created(1))) {
		t.Fatalf("scan position %q %d", s.PRScanPath, s.PRScan)
	}

	os.WriteFile(filepath.Join(filepath.Dir(path), "sid2.jsonl"), []byte(created(1)+created(2)), 0o644)
	e.with(func() {
		s := task.Sessions[store.MainSession]
		s.TranscriptPath, s.AgentSessionID = path, "sid2"
	})
	waitForAll(t, e, "only the new PR split off", func(tasks []*store.Task) bool {
		return len(tasks) == 2 && tasks[1].Title == "PR two"
	})
	time.Sleep(3 * e.PRPollInterval)
	if tasks := e.Snapshot().Tasks; len(tasks) != 2 || tasks[0].PR.Number != 1 {
		t.Errorf("PR one got a card of its own: %+v", tasks)
	}
}

// prCreatedLine is the transcript line Claude Code records when a session
// creates pull request n.
func prCreatedLine(n int) string {
	return fmt.Sprintf(`{"type":"user","toolUseResult":{"gitOperation":{"pr":{"url":"https://github.com/o/r/pull/%d","action":"created"}}}}`+"\n", n)
}

func waitForAll(t *testing.T, e *Engine, what string, cond func([]*store.Task) bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond(e.Snapshot().Tasks) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	var logs []string
	for _, task := range e.Snapshot().Tasks {
		for _, l := range task.Log {
			logs = append(logs, fmt.Sprintf("#%d %s: %s", task.Number, task.ColumnID, l.Text))
		}
	}
	t.Fatalf("timed out waiting for %s; log:\n%s", what, strings.Join(logs, "\n"))
}

func TestExplicitStartSuspendsIdlestAgent(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"maxConcurrent":1,"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"review","name":"Review"},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := func(id string) *store.Task {
		for _, task := range e.Snapshot().Tasks {
			if task.ID == id {
				return task
			}
		}
		return nil
	}
	first := e.CreateTask("First", "")
	if err := e.Move(first.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		s := task(first.ID).Sessions[store.MainSession]
		if s.Started && s.Activity == store.ActivityIdle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first agent never went idle: %+v", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	firstTmux := task(first.ID).Sessions[store.MainSession].Tmux

	second := e.CreateTask("Second", "")
	if err := e.Move(second.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(second.ID); err != nil {
		t.Fatal(err)
	}
	if s := task(first.ID).Sessions[store.MainSession]; s.Status != store.StatusSuspended {
		t.Fatalf("idle agent should be suspended, got %s", s.Status)
	}
	if tmux.Has(firstTmux) {
		t.Error("suspended agent's tmux session is still running")
	}
	if s := task(second.ID).Sessions[store.MainSession]; s.Status != store.StatusRunning || !tmux.Has(s.Tmux) {
		t.Fatalf("second agent should be running, got %+v", s)
	}
	if !hasLog(task(first.ID), "free a slot for #") || !hasLog(task(second.ID), "to free a slot") {
		t.Error("both cards should log the suspension")
	}
}

func TestBackgroundForkGoesExternal(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Go FORK", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, e, "the card to follow the background session", func(task *store.Task) bool {
		s := task.Sessions[store.MainSession]
		return s != nil && s.Status == store.StatusExternal
	})
	s := got.Sessions[store.MainSession]
	if !strings.HasPrefix(s.AgentSessionID, "forked-") || !s.Imported {
		t.Errorf("session = %s imported=%v, want the forked id", s.AgentSessionID, s.Imported)
	}
	deadline := time.Now().Add(5 * time.Second)
	for tmux.Has(s.Tmux) {
		if time.Now().After(deadline) {
			t.Fatal("blank pane left running")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestSuspendAllHandsOffEveryAgent(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	for _, title := range []string{"First", "Second"} {
		task := e.CreateTask(title, "")
		if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	waitForAll(t, e, "both agents to start", func(tasks []*store.Task) bool {
		started := 0
		for _, task := range tasks {
			if s := task.Sessions[store.MainSession]; s != nil && s.Started {
				started++
			}
		}
		return started == 2
	})

	handoff := e.SuspendAll()
	if len(handoff) != 2 {
		t.Fatalf("want 2 handoffs, got %+v", handoff)
	}
	for i, task := range e.Snapshot().Tasks {
		s := task.Sessions[store.MainSession]
		if s.Status != store.StatusSuspended || tmux.Has(s.Tmux) {
			t.Errorf("#%d: status %s, tmux alive %v", task.Number, s.Status, tmux.Has(s.Tmux))
		}
		h := handoff[i]
		if h.Number != task.Number || h.Dir != task.WorktreePath || h.SessionID != s.AgentSessionID || !s.HandedOff {
			t.Errorf("handoff %+v does not match #%d in %s (%s)", h, task.Number, task.WorktreePath, s.AgentSessionID)
		}
	}
}

// liveConversation starts a stand-in process and registers it with Claude
// Code's registry as hosting conversation id. The channel closes when it exits.
func liveConversation(t *testing.T, id, kind string) <-chan struct{} {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	dir := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "sessions")
	os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(claude.Live{PID: cmd.Process.Pid, SessionID: id, Kind: kind})
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", cmd.Process.Pid)), b, 0o644)
	return exited
}

// newEngineWithClaudeDir opens a test board whose Claude Code registry is empty.
func newEngineWithClaudeDir(t *testing.T) *Engine {
	t.Helper()
	root, _ := setupProject(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func exitsSoon(exited <-chan struct{}) bool {
	select {
	case <-exited:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func TestSnapshotListsLiveSessionsAndIsStamped(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	running := e.CreateTask("running", "")
	idle := e.CreateTask("idle", "")
	e.with(func() {
		running.Sessions = map[string]*store.Session{store.MainSession: {
			Key: store.MainSession, Tmux: "repo-live", Status: store.StatusRunning,
		}}
		idle.Sessions = map[string]*store.Session{store.MainSession: {
			Key: store.MainSession, Tmux: "repo-gone", Status: store.StatusSuspended,
		}}
	})

	first := e.Snapshot()
	want, _ := e.LiveSession(running.ID)
	if got := first.Live[running.ID]; got != want || got != "repo-live" {
		t.Errorf("Live[running] = %q, want %q", got, want)
	}
	if name, ok := first.Live[idle.ID]; ok {
		t.Errorf("a suspended card has no live session, got %q", name)
	}
	if second := e.Snapshot(); second.At.Before(first.At) || first.At.IsZero() {
		t.Errorf("snapshots must be stamped in order: %v then %v", first.At, second.At)
	}
}

func TestClosingStopsBackgroundConversation(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	withSession := func(id string, status store.SessionStatus) *store.Task {
		task := e.CreateTask("Imported "+id, "")
		e.with(func() {
			task.Sessions = map[string]*store.Session{store.MainSession: {
				Key: store.MainSession, AgentSessionID: id, Tmux: e.tmuxName(task),
				Status: status, Started: true, Imported: true,
			}}
		})
		return task
	}

	for _, status := range []store.SessionStatus{store.StatusSuspended, store.StatusRunning, store.StatusExternal} {
		id := "bg-delete-" + string(status)
		exited := liveConversation(t, id, "bg")
		if err := e.DeleteTask(withSession(id, status).ID); err != nil {
			t.Fatal(err)
		}
		if !exitsSoon(exited) {
			t.Errorf("deleting a %s card left its background conversation running", status)
		}
	}

	exited := liveConversation(t, "bg-suspend", "bg")
	task := withSession("bg-suspend", store.StatusExternal)
	e.Suspend(task.ID)
	if !exitsSoon(exited) {
		t.Error("suspending left the background conversation running")
	}
	if s := e.Snapshot(); s.Tasks[len(s.Tasks)-1].Sessions[store.MainSession].Status != store.StatusSuspended {
		t.Error("session should be suspended")
	}
}

func TestClosingLeavesTerminalConversationAndSaysSo(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	exited := liveConversation(t, "in-terminal", "interactive")
	task := e.CreateTask("Imported", "")
	e.with(func() {
		task.Sessions = map[string]*store.Session{store.MainSession: {
			Key: store.MainSession, AgentSessionID: "in-terminal", Tmux: e.tmuxName(task),
			Status: store.StatusExternal, Imported: true,
		}}
	})
	e.Suspend(task.ID)
	select {
	case <-exited:
		t.Fatal("a conversation running in your terminal was killed")
	default:
	}
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.Status != store.StatusExternal {
		t.Errorf("status = %s, want it still followed as external", s.Status)
	}
	var notices []string
	for len(e.Notices()) > 0 {
		notices = append(notices, <-e.Notices())
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "another terminal") {
		t.Errorf("notices = %q, want only the another-terminal one", notices)
	}
}

func TestStopSessionsSparesKeptConversation(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	kept, other := liveConversation(t, "kept", "bg"), liveConversation(t, "other", "bg")
	task := e.CreateTask("Two sessions", "")
	e.with(func() {
		task.Sessions = map[string]*store.Session{
			store.MainSession: {Key: store.MainSession, AgentSessionID: "kept", Status: store.StatusExternal},
			"review":          {Key: "review", AgentSessionID: "other", Status: store.StatusSuspended},
		}
	})
	e.stopSessions(task, store.MainSession, store.StatusSuspended)
	if !exitsSoon(other) {
		t.Error("the other session's background conversation kept running")
	}
	select {
	case <-kept:
		t.Fatal("the kept conversation was stopped")
	default:
	}
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.Status != store.StatusExternal {
		t.Errorf("kept status = %s", s.Status)
	}
}

func TestExternalAttachOpensBackgroundSessionsOnly(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	tmux.UserServer = []string{"-L", fmt.Sprintf("kt-no-server-%d", os.Getpid())}
	t.Cleanup(func() { tmux.UserServer = nil })
	for _, kind := range []string{"bg", "interactive"} {
		id := "attach-" + kind
		liveConversation(t, id, kind)
		task := e.CreateTask(id, "")
		e.with(func() {
			task.Sessions = map[string]*store.Session{store.MainSession: {
				Key: store.MainSession, AgentSessionID: id, Status: store.StatusExternal, Imported: true,
			}}
		})
		cmd, ok := e.ExternalAttach(task.ID)
		switch {
		case kind == "bg" && (!ok || strings.Join(cmd.Args, " ") != "claude attach "+id):
			t.Errorf("background session: cmd = %v, ok = %v", cmd, ok)
		case kind == "interactive" && ok:
			t.Errorf("a plain-terminal session got an attach command: %v", cmd.Args)
		}
	}
}

func TestTakeOverOnlyStopsRegisteredAgents(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	external := func(id string, pid int) *store.Task {
		task := e.CreateTask(id, "")
		e.with(func() {
			task.Sessions = map[string]*store.Session{store.MainSession: {
				Key: store.MainSession, AgentSessionID: id, Status: store.StatusExternal, Imported: true, PID: pid,
				Tmux: "external-" + id,
			}}
		})
		return task
	}

	exited := liveConversation(t, "in-done", "interactive")
	inDone := external("in-done", 0)
	e.with(func() { inDone.ColumnID = "done" })
	if err := e.TakeOver(inDone.ID); err == nil {
		t.Error("take over of a Done card should fail")
	}
	select {
	case <-exited:
		t.Fatal("take over stopped an agent it could not resume")
	default:
	}

	unrelated := exec.Command("sleep", "60")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unrelated.Process.Kill() })
	task := external("stale", unrelated.Process.Pid)
	e.with(func() { task.ColumnID, task.WorktreePath = "exec", e.Root })
	if err := e.TakeOver(task.ID); err != nil {
		t.Fatal(err)
	}
	if !claude.ProcessAlive(unrelated.Process.Pid) {
		t.Fatal("take over killed a process the registry does not list")
	}
}

func TestMoveToColumnWithoutModelResetsModel(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"modelOverride":"opus"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Reset model", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle && s.Started
	})
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the agent to restart", func([]*store.Task) bool {
		return strings.Count(readFile(log), "---") >= 2
	})
	if l := strings.Split(readFile(log), "---")[1]; !strings.Contains(l, "--resume") || !strings.Contains(l, "--model\ndefault") || strings.Contains(l, "--model\nopus") {
		t.Errorf("restart should resume on the default model:\n%s", l)
	}
}

func TestMoveSwitchesSettingsInPlace(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"defaultPermissionMode":"acceptEdits","columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"modelOverride":"opus"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan","modelOverride":"opus"},
	  {"id":"review","name":"Review","autoSpawn":true,"permissionMode":"plan","modelOverride":"sonnet"},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Retune", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle && s.Started
	})
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	s := e.Snapshot().Tasks[0].Sessions[store.MainSession]
	if s.PermissionMode != "plan" || s.Status != store.StatusRunning {
		t.Errorf("not switched in place: mode=%s status=%s", s.PermissionMode, s.Status)
	}
	if got := readFile(log); strings.Count(got, "---") != 1 || !strings.Contains(got, "typed:/plan") {
		t.Errorf("want /plan typed into the running agent:\n%s", got)
	}

	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the agent to restart", func([]*store.Task) bool {
		return strings.Count(readFile(log), "---") >= 2
	})
	if l := strings.Split(readFile(log), "---")[1]; !strings.Contains(l, "--resume") || !strings.Contains(l, "--permission-mode\nacceptEdits") {
		t.Errorf("restart should resume in Executing's mode:\n%s", l)
	}

	waitFor(t, e, "the restarted agent to settle", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Activity == store.ActivityIdle
	})
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the agent to restart with the new model", func([]*store.Task) bool {
		return strings.Count(readFile(log), "---") >= 3
	})
	if got := readFile(log); strings.Contains(got, "typed:/model") || !strings.Contains(strings.Split(got, "---")[2], "--model\nsonnet") {
		t.Errorf("model change should restart with --model sonnet:\n%s", got)
	}
}

func TestInterruptedTurnGoesIdle(t *testing.T) {
	root, _ := setupProject(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	if _, err := e.NewSession(); err != nil {
		t.Fatal(err)
	}
	ready := waitFor(t, e, "the new session to start", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle
	})
	transcript := claude.TranscriptPath(ready.Sessions[store.MainSession].AgentSessionID, ready.WorktreePath)
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(lines ...string) {
		if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prompt := `{"type":"user","message":{"content":"Fix the login bug"}}`
	reply := `{"type":"assistant","message":{"content":[{"type":"text","text":"On it"}]}}`
	var interrupt string
	write(prompt, reply)
	events, err := os.OpenFile(filepath.Join(e.sessionDir(ready, store.MainSession), "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	events.WriteString(`{"hook_event_name":"UserPromptSubmit","prompt":"/fix the login bug"}` + "\n")
	waitFor(t, e, "turn to start", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Activity == store.ActivityThinking
	})
	time.Sleep(time.Second)
	if got := e.Snapshot().Tasks[0].Sessions[store.MainSession].Activity; got != store.ActivityThinking {
		t.Fatalf("a running turn became %s", got)
	}

	interrupt = `{"type":"user","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`
	write(prompt, reply, interrupt)
	waitFor(t, e, "interrupted turn to go idle", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Activity == store.ActivityIdle
	})

	// The next prompt's hook can arrive before Claude Code records the prompt,
	// while the transcript still ends with the earlier interrupt.
	events.WriteString(`{"hook_event_name":"UserPromptSubmit","prompt":"/try again"}` + "\n")
	waitFor(t, e, "second turn to start", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Activity == store.ActivityThinking
	})
	time.Sleep(time.Second)
	if got := e.Snapshot().Tasks[0].Sessions[store.MainSession].Activity; got != store.ActivityThinking {
		t.Fatalf("an earlier turn's interrupt ended the new turn: %s", got)
	}
}

const reviewBoard = `{
  "columns": [
    {"id":"planning","name":"Planning","autoSpawn":true},
    {"id": "exec", "name": "Executing", "autoSpawn": true},
    {"id": "reviewing", "name": "Reviewing", "autoSpawn": true},
    {"id": "waiting", "name": "Waiting on author"},
    {"id": "done", "name": "Done", "role": "done"}
  ],
  "prFlow": {"opened": "waiting", "changesRequested": "exec", "changesRequestedMessage": "FIX {{prNumber}}"},
  "reviewFlow": {"started": "reviewing", "reviewed": "waiting", "updated": "reviewing", "done": "done"}
}`

// reviewRig runs an engine against a fake gh that reports one PR, number 7,
// written by author. Its state and the failures of `gh pr view` and
// `gh api user` are controlled by files in dir.
type reviewRig struct {
	e      *Engine
	log    string
	dir    string
	author string
}

func newReviewRig(t *testing.T, author, board string) *reviewRig {
	t.Helper()
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(board), 0o644)
	dir := filepath.Join(filepath.Dir(root), "gh")
	os.MkdirAll(dir, 0o755)
	t.Setenv("FAKE_DIR", dir)
	bin := strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0]
	os.WriteFile(filepath.Join(bin, "gh"), []byte(`#!/bin/sh
case "$1" in
  api) [ -e "$FAKE_DIR/fail-login" ] && { echo "not logged in" >&2; exit 1; }; echo me;;
  *) [ -e "$FAKE_DIR/fail-view" ] && { echo "boom" >&2; exit 1; }; cat "$FAKE_DIR/pr.json";;
esac
`), 0o755)
	old := loginRetry
	loginRetry = 100 * time.Millisecond
	t.Cleanup(func() { loginRetry = old })
	r := &reviewRig{log: log, dir: dir, author: author}
	r.gh("OPEN", "aaa", "")
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	e.PRPollInterval = 200 * time.Millisecond
	runEngine(t, e)
	r.e = e
	return r
}

// gh sets what gh reports for the PR; reviews is a list of JSON objects.
func (r *reviewRig) gh(state, head, reviews string) {
	os.WriteFile(filepath.Join(r.dir, "pr.json"), []byte(fmt.Sprintf(`{"title":"Their change","headRefName":"theirs","state":%q,
	  "headRefOid":%q,"author":{"login":%q},"reviewDecision":"","latestReviews":[%s],"mergeCommit":null}`, state, head, r.author, reviews)), 0o644)
}

// fail makes "login" (gh api user) or "view" (gh pr view) fail, or work again.
func (r *reviewRig) fail(what string, on bool) {
	path := filepath.Join(r.dir, "fail-"+what)
	if on {
		os.WriteFile(path, nil, 0o644)
	} else {
		os.Remove(path)
	}
}

// ask starts a session in the first column and asks it to review PR 7.
func (r *reviewRig) ask(t *testing.T) {
	t.Helper()
	task, err := r.e.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.e.Send(task.ID, "REVIEWME"); err != nil {
		t.Fatal(err)
	}
}

// reviewCard is the only card, or a stand-in with an empty PR while there is
// none or it holds no PR yet.
func reviewCard(tasks []*store.Task) *store.Task {
	if len(tasks) != 1 || tasks[0].PR == nil {
		return &store.Task{PR: &store.PR{}}
	}
	return tasks[0]
}

// agentDone reports whether the only card has a PR and its agent is between turns.
func agentDone(tasks []*store.Task) bool {
	return len(tasks) == 1 && tasks[0].PR != nil && tasks[0].Sessions[store.MainSession] != nil && agentIdle(tasks[0])
}

func TestReviewCardFollowsYourReviewAndTheAuthorsPushes(t *testing.T) {
	r := newReviewRig(t, "them", reviewBoard)
	e := r.e
	r.ask(t)
	waitForAll(t, e, "the review card to take its PR and move to Reviewing", func(tasks []*store.Task) bool {
		c := reviewCard(tasks)
		return c.ColumnID == "reviewing" && c.PR.Author == "them"
	})
	if c := reviewCard(e.Snapshot().Tasks); !c.PR.Review || c.PR.State != "OPEN" || c.PR.Number != 7 || c.Title != "Review: Their change" {
		t.Fatalf("review card: %q %+v", c.Title, c.PR)
	}
	waitForAll(t, e, "the agent to finish its turn", agentDone)

	first := `{"author":{"login":"them"},"state":"APPROVED","submittedAt":"2026-10-19T09:00:00Z","commit":{"oid":"zzz"}},
	  {"author":{"login":"me"},"state":"CHANGES_REQUESTED","submittedAt":"2026-10-19T10:00:00Z","commit":{"oid":"aaa"}}`
	r.gh("OPEN", "aaa", first)
	waitForAll(t, e, "your review to move the card to Waiting", func(tasks []*store.Task) bool {
		c := reviewCard(tasks)
		return c.ColumnID == "waiting" && c.PR.MyReview == "CHANGES_REQUESTED" && c.PR.ReviewedSHA == "aaa"
	})
	time.Sleep(600 * time.Millisecond)
	if c := reviewCard(e.Snapshot().Tasks); c.ColumnID != "waiting" {
		t.Fatalf("the same review moved the card to %s again", c.ColumnID)
	}

	r.gh("OPEN", "bbb", first)
	waitForAll(t, e, "new commits to move the card back to Reviewing", func(tasks []*store.Task) bool {
		c := reviewCard(tasks)
		return c.ColumnID == "reviewing" && c.PR.HeadSHA == "bbb" && c.PR.ReviewedSHA == "aaa"
	})
	waitForAll(t, e, "the agent to finish its turn", agentDone)

	second := first + `,
	  {"author":{"login":"me"},"state":"APPROVED","submittedAt":"2026-10-20T10:00:00Z","commit":{"oid":"bbb"}}`
	r.gh("OPEN", "bbb", second)
	waitForAll(t, e, "a newer review to move the card to Waiting again", func(tasks []*store.Task) bool {
		c := reviewCard(tasks)
		return c.ColumnID == "waiting" && c.PR.MyReview == "APPROVED" && c.PR.ReviewedSHA == "bbb"
	})

	r.gh("MERGED", "bbb", second)
	waitForAll(t, e, "the merged PR to finish the card", func(tasks []*store.Task) bool { return reviewCard(tasks).ColumnID == "done" })
	if data, _ := os.ReadFile(r.log); strings.Contains(string(data), "typed:FIX") {
		t.Errorf("your own changes-requested review was sent to the agent to address:\n%s", data)
	}
}

func TestReviewCardWaitsForTheAgentBeforeMovingOnYourReview(t *testing.T) {
	r := newReviewRig(t, "them", reviewBoard)
	r.ask(t)
	waitForAll(t, r.e, "the review card to move to Reviewing", func(tasks []*store.Task) bool {
		return reviewCard(tasks).ColumnID == "reviewing" && agentDone(tasks)
	})
	if err := r.e.Send(r.e.Snapshot().Tasks[0].ID, "SLOW"); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, r.e, "the agent to be mid-turn", func(tasks []*store.Task) bool {
		return tasks[0].Sessions[store.MainSession].Activity == store.ActivityThinking
	})
	r.gh("OPEN", "aaa", `{"author":{"login":"me"},"state":"COMMENTED","submittedAt":"2026-10-19T10:00:00Z","commit":{"oid":"aaa"}}`)
	time.Sleep(time.Second)
	if c := reviewCard(r.e.Snapshot().Tasks); c.ColumnID != "reviewing" {
		t.Fatalf("the card moved to %s under a running agent", c.ColumnID)
	}
	waitForAll(t, r.e, "the review to move the card once the agent is idle", func(tasks []*store.Task) bool {
		return reviewCard(tasks).ColumnID == "waiting"
	})
}

func TestReviewOfYourOwnPRIsForgotten(t *testing.T) {
	r := newReviewRig(t, "me", reviewBoard)
	r.ask(t)
	waitForAll(t, r.e, "gh to show PR 7 is yours and the card to forget it", func(tasks []*store.Task) bool {
		for _, l := range tasks[0].Log {
			if strings.Contains(l.Text, "is yours, not tracking it as a review") {
				return true
			}
		}
		return false
	})
	time.Sleep(600 * time.Millisecond)
	if c := r.e.Snapshot().Tasks[0]; c.PR != nil || c.PRMovePending || c.ColumnID != "planning" {
		t.Fatalf("own PR kept as a review: %+v, pending %v, column %s", c.PR, c.PRMovePending, c.ColumnID)
	}
}

func TestReviewCardWaitsForGhToConfirmThePR(t *testing.T) {
	r := newReviewRig(t, "them", reviewBoard)
	r.fail("login", true)
	r.ask(t)
	waitForAll(t, r.e, "the nominated PR and a failed login lookup", func(tasks []*store.Task) bool {
		c := reviewCard(tasks)
		return c.PR.Review && strings.Contains(c.PR.Error, "can't tell whether it is yours") && agentDone(tasks)
	})
	r.fail("view", true)
	waitForAll(t, r.e, "gh pr view to fail first", func(tasks []*store.Task) bool {
		return strings.Contains(reviewCard(tasks).PR.Error, "boom")
	})
	r.fail("login", false)
	time.Sleep(600 * time.Millisecond)
	c := r.e.Snapshot().Tasks[0]
	if c.ColumnID != "planning" || !c.PRMovePending || c.PR.State != "" {
		t.Fatalf("card moved before gh confirmed the PR: column %s, pending %v, %+v", c.ColumnID, c.PRMovePending, c.PR)
	}
	r.fail("view", false)
	waitForAll(t, r.e, "the confirmed review card to move to Reviewing", func(tasks []*store.Task) bool {
		c := reviewCard(tasks)
		return c.ColumnID == "reviewing" && c.PR.State == "OPEN" && c.PR.Error == ""
	})
	waitForAll(t, r.e, "the agent to settle in Reviewing", agentDone)
}

func TestReviewedPRAlreadyMergedStaysPut(t *testing.T) {
	r := newReviewRig(t, "them", reviewBoard)
	r.gh("MERGED", "aaa", "")
	r.ask(t)
	waitForAll(t, r.e, "gh to confirm the merged PR", func(tasks []*store.Task) bool {
		return reviewCard(tasks).PR.State == "MERGED"
	})
	time.Sleep(600 * time.Millisecond)
	if c := r.e.Snapshot().Tasks[0]; c.ColumnID == "done" || len(c.Sessions) == 0 {
		t.Fatalf("a PR that was merged before the review sent the card to %s, sessions %d", c.ColumnID, len(c.Sessions))
	}
}

func TestReviewedPRClosedFinishesTheCard(t *testing.T) {
	r := newReviewRig(t, "them", reviewBoard)
	r.ask(t)
	waitForAll(t, r.e, "the review card to move to Reviewing", func(tasks []*store.Task) bool {
		return reviewCard(tasks).ColumnID == "reviewing"
	})
	r.gh("CLOSED", "aaa", "")
	waitForAll(t, r.e, "the closed PR to finish the card", func(tasks []*store.Task) bool { return reviewCard(tasks).ColumnID == "done" })
	logged := false
	for _, l := range r.e.Snapshot().Tasks[0].Log {
		logged = logged || strings.Contains(l.Text, "closed without merging")
	}
	if !logged {
		t.Error("the close wasn't logged")
	}
}

func TestReviewWithoutReviewFlowLeavesTheCardAlone(t *testing.T) {
	board := strings.Replace(reviewBoard, `,
  "reviewFlow": {"started": "reviewing", "reviewed": "waiting", "updated": "reviewing", "done": "done"}`, "", 1)
	r := newReviewRig(t, "them", board)
	r.ask(t)
	waitForAll(t, r.e, "gh to confirm the review PR", func(tasks []*store.Task) bool { return reviewCard(tasks).PR.State == "OPEN" })
	r.gh("MERGED", "bbb", `{"author":{"login":"me"},"state":"CHANGES_REQUESTED","submittedAt":"2026-10-19T10:00:00Z"}`)
	waitForAll(t, r.e, "gh to report the merge", func(tasks []*store.Task) bool { return reviewCard(tasks).PR.State == "MERGED" })
	time.Sleep(600 * time.Millisecond)
	c := r.e.Snapshot().Tasks[0]
	if c.ColumnID == "waiting" || c.ColumnID == "done" || c.PRMovePending || c.PR.Released {
		t.Fatalf("review card followed the prFlow: column %s, pending %v, %+v", c.ColumnID, c.PRMovePending, c.PR)
	}
	if data, _ := os.ReadFile(r.log); strings.Contains(string(data), "typed:FIX") {
		t.Errorf("your own review was sent to the agent:\n%s", data)
	}
}

func TestReviewInAWorkingSessionGetsItsOwnCard(t *testing.T) {
	r := newReviewRig(t, "them", reviewBoard)
	task := r.e.CreateTask("Auth", "REVIEWME")
	if err := r.e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	var owner, review *store.Task
	waitForAll(t, r.e, "the reviewed PR to get its own confirmed card", func(tasks []*store.Task) bool {
		if len(tasks) != 2 {
			return false
		}
		owner, review = tasks[0], tasks[1]
		return review.PR != nil && review.PR.State == "OPEN"
	})
	if owner.PR != nil || owner.ColumnID != "exec" || owner.PRMovePending {
		t.Fatalf("the working card was taken over: %+v, column %s", owner.PR, owner.ColumnID)
	}
	if !review.PR.Review || review.GroupID != owner.ID || review.ColumnID != "reviewing" || review.PR.Author != "them" {
		t.Fatalf("review card: group %q, column %s, %+v", review.GroupID, review.ColumnID, review.PR)
	}
}

func TestSplitByPROffGivesAReviewInAWorkingSessionNoCard(t *testing.T) {
	r := newReviewRig(t, "them", strings.Replace(reviewBoard, "{", `{"splitByPR": false,`, 1))
	task := r.e.CreateTask("Auth", "REVIEWME")
	if err := r.e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	const reviewed = "https://github.com/o/r/pull/7"
	waitForAll(t, r.e, "the reviewed PR to be seen", func(tasks []*store.Task) bool {
		return slices.Contains(tasks[0].SeenPRs, reviewed)
	})
	tasks := r.e.Snapshot().Tasks
	if len(tasks) != 1 || tasks[0].PR != nil || tasks[0].ColumnID != "exec" {
		t.Fatalf("splitByPR off: %d cards, owner PR %+v, column %s", len(tasks), tasks[0].PR, tasks[0].ColumnID)
	}
	logged := false
	for _, entry := range tasks[0].Log {
		logged = logged || strings.Contains(entry.Text, "Reviewing PR #7, not split off")
	}
	if !logged {
		t.Errorf("the dropped review was not logged: %+v", tasks[0].Log)
	}
}

func TestLeavingDoneClearsArchived(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	task := e.CreateTask("Fix the login bug", "")
	if err := e.Move(task.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.ArchiveTask(task.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if err := e.Move(task.ID, "planning", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().Tasks[0].Archived {
		t.Error("leaving Done kept the card archived")
	}
}

func TestArchiveTaskOnlyInDone(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	open := e.CreateTask("Open", "")
	finished := e.CreateTask("Finished", "")
	if err := e.Move(finished.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.ArchiveTask(open.ID, true, false); err == nil {
		t.Error("archived a card outside Done")
	}
	if n := e.ArchiveColumn("exec"); n != 0 {
		t.Errorf("ArchiveColumn on a non-Done column = %d, want 0", n)
	}
	if n := e.ArchiveColumn("done"); n != 1 {
		t.Errorf("ArchiveColumn = %d, want 1", n)
	}
	if n := e.ArchiveColumn("done"); n != 0 {
		t.Errorf("second ArchiveColumn = %d, want 0", n)
	}
	for _, task := range e.Snapshot().Tasks {
		if task.Archived != (task.ID == finished.ID) {
			t.Errorf("#%d archived = %v", task.Number, task.Archived)
		}
	}
	if err := e.ArchiveTask(finished.ID, false, false); err != nil {
		t.Fatal(err)
	}
	for _, task := range e.Snapshot().Tasks {
		if task.ID == finished.ID && task.Archived {
			t.Error("card still archived after restoring it")
		}
	}
}

// finishedCard runs a card to Done and returns its id and worktree path. With
// dirty set the worktree holds an uncommitted file.
func finishedCard(t *testing.T, e *Engine, title string, dirty bool) (id, path string) {
	t.Helper()
	task := e.CreateTask(title, "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitNotBusy(t, e)
	path = cardWithID(e, task.ID).WorktreePath
	if path == "" {
		t.Fatalf("%q has no worktree", title)
	}
	if dirty {
		if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Move(task.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitNotBusy(t, e)
	return task.ID, path
}

func cardWithID(e *Engine, id string) *store.Task {
	for _, task := range e.Snapshot().Tasks {
		if task.ID == id {
			return task
		}
	}
	return nil
}

func TestArchiveColumnKeepsDirtyWorktrees(t *testing.T) {
	e, _ := running(t)
	cleanID, cleanPath := finishedCard(t, e, "Clean", false)
	dirtyID, dirtyPath := finishedCard(t, e, "Dirty", true)
	if n := e.ArchiveColumn("done"); n != 2 {
		t.Fatalf("ArchiveColumn = %d, want 2", n)
	}
	if exists(cleanPath) || cardWithID(e, cleanID).WorktreePath != "" {
		t.Error("the clean card's worktree survived archiving")
	}
	if !exists(dirtyPath) {
		t.Error("archiving removed a worktree with uncommitted changes")
	}
	if !hasLog(cardWithID(e, dirtyID), "Worktree kept") {
		t.Error("keeping the dirty worktree was not logged")
	}
}

func TestArchiveStaleRemovesWorktrees(t *testing.T) {
	e, _ := running(t)
	cleanID, cleanPath := finishedCard(t, e, "Clean", false)
	dirtyID, dirtyPath := finishedCard(t, e, "Dirty", true)
	var release func()
	e.with(func() {
		e.board.ArchiveDoneAfter = "7d"
		for _, task := range e.state.Tasks {
			task.UpdatedAt = time.Now().Add(-8 * 24 * time.Hour)
		}
		release = e.archiveStale(time.Now())
	})
	if release == nil {
		t.Fatal("archiveStale found nothing to archive")
	}
	release()
	if exists(cleanPath) {
		t.Fatal("the automatic archive left the clean worktree")
	}
	if !cardWithID(e, cleanID).Archived || !cardWithID(e, dirtyID).Archived {
		t.Error("stale cards were not archived")
	}
	if !exists(dirtyPath) {
		t.Error("the automatic archive removed a worktree with uncommitted changes")
	}
}

func TestRestoredCardGetsItsWorktreeBack(t *testing.T) {
	e, _ := running(t)
	id, path := finishedCard(t, e, "Redo", true)
	if err := e.ArchiveTask(id, true, true); err != nil {
		t.Fatal(err)
	}
	card := cardWithID(e, id)
	if !card.Archived || card.WorktreePath != "" || exists(path) {
		t.Fatalf("forced archive: archived=%v worktree=%q exists=%v", card.Archived, card.WorktreePath, exists(path))
	}
	branch := card.Branch
	if err := e.ArchiveTask(id, false, false); err != nil {
		t.Fatal(err)
	}
	if err := e.Move(id, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitNotBusy(t, e)
	got := cardWithID(e, id)
	if got.Archived || got.WorktreePath != path || !exists(path) || got.Branch != branch {
		t.Errorf("restored card: archived=%v worktree=%q (want %q) branch=%q (want %q)", got.Archived, got.WorktreePath, path, got.Branch, branch)
	}
}

func TestArchiveStaleUsesTimeInDone(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.with(func() {
		e.board.ArchiveDoneAfter = "7d"
		e.state.Tasks = []*store.Task{
			{ID: "old", ColumnID: "done", UpdatedAt: now.Add(-8 * 24 * time.Hour)},
			{ID: "fresh", ColumnID: "done", UpdatedAt: now.Add(-24 * time.Hour)},
			{ID: "open", ColumnID: "exec", UpdatedAt: now.Add(-30 * 24 * time.Hour)},
		}
		e.archiveStale(now)
	})
	want := map[string]bool{"old": true, "fresh": false, "open": false}
	for _, task := range e.Snapshot().Tasks {
		if task.Archived != want[task.ID] {
			t.Errorf("%s archived = %v, want %v", task.ID, task.Archived, want[task.ID])
		}
	}

	if err := e.ArchiveTask("old", false, false); err != nil {
		t.Fatal(err)
	}
	e.with(func() { e.archiveStale(now) })
	for _, task := range e.Snapshot().Tasks {
		if task.ID == "old" && task.Archived {
			t.Error("a restored card was archived again on the next sweep")
		}
	}

	e.with(func() {
		e.board.ArchiveDoneAfter = ""
		for _, task := range e.state.Tasks {
			task.Archived = false
			task.UpdatedAt = now.Add(-30 * 24 * time.Hour)
		}
		e.archiveStale(now)
	})
	for _, task := range e.Snapshot().Tasks {
		if task.Archived {
			t.Errorf("%s archived with archiveDoneAfter unset", task.ID)
		}
	}
}

func TestTickArchivesStaleDoneCards(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	e.with(func() {
		e.board.ArchiveDoneAfter = "7d"
		e.state.Tasks = []*store.Task{{ID: "old", ColumnID: "done", UpdatedAt: time.Now().Add(-8 * 24 * time.Hour)}}
	})
	e.Tick()
	if !e.Snapshot().Tasks[0].Archived {
		t.Error("Tick left a stale Done card unarchived")
	}
	reloaded, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Snapshot().Tasks[0].Archived {
		t.Error("archived state didn't survive a reload")
	}
}

func TestTurnEndQuestionMovesCardToWaiting(t *testing.T) {
	for _, c := range []struct{ title, want string }{
		{"ASKS_ME ready to merge?", "waiting-for-me"},
		{"Plain work", "exec"},
	} {
		t.Run(c.title, func(t *testing.T) {
			e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,
	    "automations":{"onTurnEnd":[{"name":"Waiting for me","type":"move_to_column","targetColumn":"waiting-for-me",
	      "condition":"The reply ends by asking me for a decision or input"}]}},
	  {"id":"waiting-for-me","name":"Waiting for me","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`)

			task := e.CreateTask(c.title, "")
			if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, e, "the turn-end rule to decide", func(t *store.Task) bool {
				s := t.Sessions[store.MainSession]
				if s == nil || !s.Started || s.Activity != store.ActivityIdle {
					return false
				}
				return t.ColumnID == "waiting-for-me" || hasLog(t, "condition not met")
			})
			if got := e.Snapshot().Tasks[0].ColumnID; got != c.want {
				t.Fatalf("card in %s, want %s", got, c.want)
			}
		})
	}
}

func TestTurnEndMoveStillShipsPR(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,
	    "automations":{"onTurnEnd":[{"name":"Waiting for me","type":"move_to_column","targetColumn":"waiting-for-me",
	      "condition":"The reply ends by asking me for a decision or input"}]}},
	  {"id":"waiting-for-me","name":"Waiting for me","autoSpawn":true},
	  {"id":"review","name":"In review"},
	  {"id":"done","name":"Done","role":"done"}],
	  "prFlow":{"opened":"review","changesRequested":"exec"}}`)

	task := e.CreateTask("ONEPR ASKS_ME merge?", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the PR to ship to In review", func(t *store.Task) bool {
		return t.ColumnID == "review"
	})
	if !hasLog(e.Snapshot().Tasks[0], "moving to Waiting for me") {
		t.Fatal("the card did not pass through Waiting for me")
	}
}

func TestKeepAgentLeavesPendingRespawnAlone(t *testing.T) {
	root, log := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"defaultPermissionMode":"acceptEdits","columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"review","name":"In review","keepAgent":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

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
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle && !s.RespawnPending
	})
	time.Sleep(time.Second)
	s := e.Snapshot().Tasks[0].Sessions[store.MainSession]
	if s.Status != store.StatusRunning || s.PermissionMode != "plan" {
		t.Fatalf("a kept agent was respawned: status=%v mode=%s", s.Status, s.PermissionMode)
	}
	if n := strings.Count(readFile(log), "---"); n != 1 {
		t.Fatalf("want 1 launch, got %d", n)
	}
}

func TestKeepAgentColumnLeavesAgentRunning(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"review","name":"In review","keepAgent":true},
	  {"id":"park","name":"Parked"},
	  {"id":"done","name":"Done","role":"done"}]}`)
	task := startIdleAgent(t, e, "Watched PR")
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.Status != store.StatusRunning || !tmux.Has(s.Tmux) {
		t.Fatalf("keepAgent column stopped the agent: status=%v", s.Status)
	}
	if err := e.Move(task.ID, "park", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.Status != store.StatusSuspended {
		t.Fatalf("a plain column should suspend the agent: status=%v", s.Status)
	}
}

func TestSlotEvictionSparesKeptAgent(t *testing.T) {
	e := startWithBoard(t, `{"maxConcurrent":1,"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"watch","name":"In review","keepAgent":true},
	  {"id":"review","name":"Review"},
	  {"id":"done","name":"Done","role":"done"}]}`)
	first := startIdleAgent(t, e, "Watched PR")
	if err := e.Move(first.ID, "watch", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	second := e.CreateTask("Second", "")
	if err := e.Move(second.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(second.ID); err != nil {
		t.Fatal(err)
	}
	for _, task := range e.Snapshot().Tasks {
		s := task.Sessions[store.MainSession]
		switch task.ID {
		case first.ID:
			if s.Status != store.StatusRunning || !tmux.Has(s.Tmux) {
				t.Fatalf("a kept agent was evicted for another start: status=%v", s.Status)
			}
		case second.ID:
			if s != nil && s.Status == store.StatusRunning {
				t.Fatalf("second agent should not start past the limit: status=%v", s.Status)
			}
		}
	}
}

func TestHideTaskSurvivesMove(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	task := e.CreateTask("Open", "")
	if err := e.HideTask(task.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.Move(task.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if !e.Snapshot().Tasks[0].Hidden {
		t.Error("moving a card unhid it")
	}
	if err := e.HideTask(task.ID, false); err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().Tasks[0].Hidden {
		t.Error("card is still hidden")
	}
	if err := e.HideTask("nope", true); err == nil {
		t.Error("hid an unknown card")
	}
}

func TestArchiveColumnSkipsHiddenCards(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	shown := e.CreateTask("Shown", "")
	hidden := e.CreateTask("Hidden", "")
	for _, task := range []*store.Task{shown, hidden} {
		if err := e.Move(task.ID, "done", MoveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.HideTask(hidden.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := e.ArchiveColumn("done"); n != 1 {
		t.Errorf("ArchiveColumn = %d, want only the visible card", n)
	}
	for _, task := range e.Snapshot().Tasks {
		if want := task.ID == shown.ID; task.Archived != want {
			t.Errorf("#%d archived = %v, want %v", task.Number, task.Archived, want)
		}
	}
}
