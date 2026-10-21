package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

func worktreeBoard(extra string) string {
	return `{` + extra + `"columns":[
	  {"id":"todo","name":"To Do","role":"todo"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`
}

func TestNewWorktreeGetsCopiedFilesAndInitScript(t *testing.T) {
	e := startWithBoard(t, worktreeBoard(`"copyFiles":[".env"],"initScript":"cat .env > init-ran && echo \"$DROVER_BRANCHNAME\" >> init-ran",`))
	os.WriteFile(filepath.Join(e.Root, ".env"), []byte("TOKEN=1\n"), 0o600)

	task := e.CreateTask("Set me up", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := e.Snapshot().Tasks[0]
	if got := readFile(filepath.Join(moved.WorktreePath, "init-ran")); got != "TOKEN=1\n"+moved.Branch+"\n" {
		t.Errorf("init script output = %q", got)
	}
	os.Remove(filepath.Join(moved.WorktreePath, "init-ran"))
	if e.WorktreeDirty(task.ID) {
		t.Error("the copied .env counts as an uncommitted change")
	}
}

func TestFailingInitScriptFailsTheMove(t *testing.T) {
	e := startWithBoard(t, worktreeBoard(`"initScript":"echo nope; exit 1",`))
	task := e.CreateTask("Broken setup", "")
	err := e.Move(task.ID, "exec", MoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "init script") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Move = %v, want the init script's failure", err)
	}
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s != nil {
		t.Errorf("agent started despite the failed init script: %+v", s)
	}
}

func TestShellOpensInWorktreeAndGoesWithIt(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	e := startWithBoard(t, worktreeBoard(""))
	task := e.CreateTask("Poke around", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	name, err := e.Shell(task.ID)
	if err != nil || !tmux.Has(name) {
		t.Fatalf("Shell = %q, %v", name, err)
	}
	out, _ := exec.Command("tmux", "-L", tmux.Socket, "display", "-p", "-t", "="+name+":", "#{pane_current_path}").Output()
	want, _ := filepath.EvalSymlinks(e.Snapshot().Tasks[0].WorktreePath)
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("shell runs in %q, want %q", got, want)
	}
	if again, _ := e.Shell(task.ID); again != name {
		t.Errorf("second Shell = %q, want the same session %q", again, name)
	}
	if err := e.Move(task.ID, "done", MoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if tmux.Has(name) {
		t.Error("shell outlived the worktree")
	}
}

func TestUpdateFromBase(t *testing.T) {
	e := startWithBoard(t, worktreeBoard(""))
	task := e.CreateTask("Keep up", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the agent to go idle", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && idle(s)
	})
	commit := func(dir, msg string) {
		cmd := exec.Command("git", "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", msg)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %s", out)
		}
	}

	if done, err := e.UpdateFromBase(task.ID); err != nil || !strings.Contains(done, "up to date with main") {
		t.Fatalf("UpdateFromBase = %q, %v", done, err)
	}
	commit(e.Root, "upstream one")
	commit(e.Root, "upstream two")
	if done, err := e.UpdateFromBase(task.ID); err != nil || !strings.Contains(done, "fast-forwarded 2 commits from main") {
		t.Fatalf("UpdateFromBase = %q, %v", done, err)
	}

	wt := e.Snapshot().Tasks[0].WorktreePath
	os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("x"), 0o644)
	commit(e.Root, "upstream three")
	if _, err := e.UpdateFromBase(task.ID); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("UpdateFromBase on a dirty worktree = %v", err)
	}
	os.Remove(filepath.Join(wt, "notes.txt"))

	commit(wt, "mine")
	if done, err := e.UpdateFromBase(task.ID); err != nil || !strings.Contains(done, "asked the agent to merge main") {
		t.Fatalf("UpdateFromBase = %q, %v", done, err)
	}
}

func TestUpdateFromBaseNeedsAnIdleWorktree(t *testing.T) {
	e := startWithBoard(t, worktreeBoard(""))
	waiting := e.CreateTask("Not started", "")
	if _, err := e.UpdateFromBase(waiting.ID); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Errorf("UpdateFromBase in To Do = %v", err)
	}
	busy := e.CreateTask("SLOW work", "")
	if err := e.Move(busy.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the agent to start working", func(tasks []*store.Task) bool {
		for _, task := range tasks {
			if s := task.Sessions[store.MainSession]; task.ID == busy.ID && s != nil && s.Activity == store.ActivityThinking {
				return true
			}
		}
		return false
	})
	if _, err := e.UpdateFromBase(busy.ID); err == nil || !strings.Contains(err.Error(), "working") {
		t.Errorf("UpdateFromBase while working = %v", err)
	}
}
