package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

func worktreeBoard(extra string) string {
	return `{` + extra + `"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`
}

func TestNewWorktreeGetsLinkedFilesAndInitScript(t *testing.T) {
	e := startWithBoard(t, worktreeBoard(`"linkUntracked":true,"linkFiles":[".env"],"initScript":"cat .env > init-ran && echo \"$DROVER_BRANCHNAME\" >> init-ran",`))
	os.WriteFile(filepath.Join(e.Root, ".env"), []byte("TOKEN=1\n"), 0o600)

	os.MkdirAll(filepath.Join(e.Root, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(e.Root, "node_modules", "pkg", "x.js"), []byte("x"), 0o644)
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
		t.Error("the linked .env counts as an uncommitted change")
	}
	if target, _ := os.Readlink(filepath.Join(moved.WorktreePath, ".env")); target != filepath.Join(e.Root, ".env") {
		t.Errorf(".env links to %q", target)
	}
	if _, err := os.Lstat(filepath.Join(moved.WorktreePath, "node_modules")); err == nil {
		t.Error("node_modules was linked")
	}
}

func TestNewWorktreeLinksNothingByDefault(t *testing.T) {
	e := startWithBoard(t, worktreeBoard(`"linkFiles":[".env"],`))
	os.WriteFile(filepath.Join(e.Root, ".env"), []byte("TOKEN=1\n"), 0o600)
	os.MkdirAll(filepath.Join(e.Root, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(e.Root, "node_modules", "pkg", "x.js"), []byte("x"), 0o644)

	task := e.CreateTask("Plain", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	wt := e.Snapshot().Tasks[0].WorktreePath
	for _, rel := range []string{".env", "node_modules"} {
		if _, err := os.Lstat(filepath.Join(wt, rel)); err == nil {
			t.Errorf("%s was linked without linkUntracked", rel)
		}
	}
	if e.WorktreeDirty(task.ID) {
		t.Error("a fresh worktree is dirty")
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

// paneDir returns the session pane's directory once it is want. A freshly
// started tmux server reports its own directory until the shell has moved into
// the session's.
func paneDir(name, want string) string {
	var got string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		out, _ := exec.Command("tmux", "-L", tmux.Socket, "display", "-p", "-t", "="+name+":", "#{pane_current_path}").Output()
		if got = strings.TrimSpace(string(out)); got == want {
			break
		}
	}
	return got
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
	want, _ := filepath.EvalSymlinks(e.Snapshot().Tasks[0].WorktreePath)
	if got := paneDir(name, want); got != want {
		t.Errorf("shell runs in %q, want %q", got, want)
	}
	if again, _ := e.Shell(task.ID); again != name {
		t.Errorf("second Shell = %q, want the same session %q", again, name)
	}
	if err := e.Move(task.ID, "done", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if !tmux.Has(name) {
		t.Error("shell did not survive the move to Done")
	}
	if err := e.ArchiveTask(task.ID, true, true); err != nil {
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
		t.Errorf("UpdateFromBase on a not-started card = %v", err)
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

func TestUpdateFromBaseAsksYouToStartTheAgentWhenTheBranchDiverged(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git(root, "init", "-q", "-b", "main")
	git(root, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	git(root, "worktree", "add", "-q", "-b", "feature", wt)
	git(wt, "commit", "-q", "--allow-empty", "-m", "mine")
	git(root, "commit", "-q", "--allow-empty", "-m", "upstream")

	card := &store.Task{ID: "abcdefgh", Number: 1, WorktreePath: wt, Branch: "feature", BaseBranch: "main"}
	e := &Engine{Root: root, isRepo: true, state: &store.State{Tasks: []*store.Task{card}}, board: &config.Board{}}
	if _, err := e.UpdateFromBase(card.ID); err == nil || !strings.Contains(err.Error(), "commits of its own") {
		t.Errorf("UpdateFromBase on a diverged branch without an agent = %v", err)
	}

	if _, err := e.UpdateFromBase("missing"); err == nil || !strings.Contains(err.Error(), "task not found") {
		t.Errorf("UpdateFromBase on an unknown card = %v", err)
	}
	e.isRepo = false
	if _, err := e.UpdateFromBase(card.ID); err == nil || !strings.Contains(err.Error(), "no worktree") {
		t.Errorf("UpdateFromBase outside a repository = %v", err)
	}
}

func TestShellOpensInTheMainCheckoutBeforeTheCardHasAWorktree(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	e := startWithBoard(t, worktreeBoard(""))
	task := e.CreateTask("Not started", "")
	name, err := e.Shell(task.ID)
	if err != nil || !tmux.Has(name) {
		t.Fatalf("Shell = %q, %v", name, err)
	}
	want, _ := filepath.EvalSymlinks(e.Root)
	if got := paneDir(name, want); got != want {
		t.Errorf("shell runs in %q, want the main checkout %q", got, want)
	}
	if _, err := e.Shell("missing"); err == nil {
		t.Error("Shell on an unknown card succeeded")
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func TestUpdateFromBaseSaysWhichBranchItUsed(t *testing.T) {
	for name, tc := range map[string]struct {
		origin func(t *testing.T, root string)
		want   string
	}{
		"no origin means the local branch": {nil, "up to date with main"},
		"a reachable origin": {func(t *testing.T, root string) {
			remote := filepath.Join(t.TempDir(), "origin.git")
			gitIn(t, root, "clone", "-q", "--bare", root, remote)
			gitIn(t, root, "remote", "add", "origin", remote)
		}, "up to date with origin/main"},
		"an origin that cannot be fetched": {func(t *testing.T, root string) {
			gitIn(t, root, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
		}, "up to date with local main (fetch failed"},
	} {
		t.Run(name, func(t *testing.T) {
			root, _, _ := releaseRepo(t)
			if tc.origin != nil {
				tc.origin(t, root)
			}
			worktree := filepath.Join(t.TempDir(), "wt")
			gitIn(t, root, "worktree", "add", "-q", "-b", "wt", worktree, "main")
			e := bareEngine(t)
			e.Root, e.isRepo = root, true
			card := &store.Task{ID: "a", Number: 1, WorktreePath: worktree, BaseBranch: "main"}
			e.state.Tasks = []*store.Task{card}

			got, err := e.UpdateFromBase(card.ID)

			if err != nil || !strings.Contains(got, tc.want) {
				t.Errorf("UpdateFromBase = %q, %v; want it to say %q", got, err, tc.want)
			}
		})
	}
}

const deferredWorktreeBoard = `{"initScript":%q,"columns":[
  {"id":"backlog","name":"Backlog"},
  {"id":"plan","name":"Planning","autoSpawn":true,"noWorktree":true},
  {"id":"triage","name":"Triage","lane":"review","autoSpawn":true%s},
  {"id":"done","name":"Done","role":"done"}]}`

func startedInPlanning(t *testing.T, initScript, triageExtra string) (e *Engine, task *store.Task, id, log string) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	root, log := setupProject(t)
	writeBoard(root, []byte(fmt.Sprintf(deferredWorktreeBoard, initScript, triageExtra)))
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)
	task = e.CreateTask("Review it", "")
	if err := e.Move(task.ID, "plan", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	started := waitFor(t, e, "first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	id = started.Sessions[store.MainSession].AgentSessionID
	transcript := claude.TranscriptPath(id, root)
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return e, task, id, log
}

func gatedInitScript(t *testing.T) (script, gate string) {
	t.Helper()
	gate = filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { os.WriteFile(gate, nil, 0o644) })
	return `while [ ! -f ` + gate + ` ]; do sleep 0.05; done`, gate
}

func releaseGate(t *testing.T, gate string) {
	t.Helper()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireInRoot(t *testing.T, e *Engine) {
	t.Helper()
	got := e.Snapshot().Tasks[0]
	if s := got.Sessions[store.MainSession]; got.ColumnID != "triage" || got.WorktreePath != "" || s.Status != store.StatusRunning || s.Cwd != e.Root {
		t.Fatalf("column %q, worktree %q, session %+v; want the card in Triage with its agent running in the root and no worktree", got.ColumnID, got.WorktreePath, s)
	}
}

func TestReviewLaneMoveKeepsTheAgentInTheRootWhileTheWorktreeIsBuilt(t *testing.T) {
	script, gate := gatedInitScript(t)
	e, task, id, _ := startedInPlanning(t, script, "")

	if err := e.Move(task.ID, "triage", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	requireInRoot(t, e)
	releaseGate(t, gate)

	got := waitFor(t, e, "the agent to resume in the worktree", inWorktree)
	if s := got.Sessions[store.MainSession]; s.AgentSessionID != id {
		t.Errorf("session id changed from %s to %s; the conversation should resume", id, s.AgentSessionID)
	}
	if _, err := os.Stat(claude.TranscriptPath(id, got.WorktreePath)); err != nil {
		t.Errorf("the conversation did not follow the agent into the worktree: %v", err)
	}
}

func TestReviewLaneMoveOfABusyAgentMovesItWhenItsTurnEnds(t *testing.T) {
	script, gate := gatedInitScript(t)
	e, task, id, _ := startedInPlanning(t, script, "")
	if err := e.Send(task.ID, "SLOW"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the turn to start", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Activity == store.ActivityThinking
	})

	if err := e.Move(task.ID, "triage", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	releaseGate(t, gate)
	got := waitFor(t, e, "the worktree to be built", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return t.WorktreePath != "" && s.Cwd != t.WorktreePath && s.RespawnPending
	})
	if s := got.Sessions[store.MainSession]; s.Cwd != e.Root || s.AgentSessionID != id {
		t.Errorf("the busy agent was moved mid-turn: %+v", s)
	}

	got = waitFor(t, e, "the agent to resume in the worktree", func(t *store.Task) bool {
		return inWorktree(t) && !t.Sessions[store.MainSession].RespawnPending
	})
	if s := got.Sessions[store.MainSession]; s.AgentSessionID != id {
		t.Errorf("session id changed from %s to %s; the conversation should resume", id, s.AgentSessionID)
	}
}

func TestReviewLaneMoveWithNewSettingsRestartsTheAgentOnce(t *testing.T) {
	e, task, id, log := startedInPlanning(t, "", `,"effortOverride":"high"`)

	if err := e.Move(task.ID, "triage", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, e, "the agent to resume in the worktree", inWorktree)
	if s := got.Sessions[store.MainSession]; s.AgentSessionID != id || s.Effort != "high" {
		t.Errorf("session %+v; want the same conversation at high effort", s)
	}
	waitForAll(t, e, "the resumed agent to launch", func([]*store.Task) bool {
		return strings.Count(readFile(log), "---") >= 2
	})
	time.Sleep(time.Second)
	if n := strings.Count(readFile(log), "---"); n != 2 {
		t.Errorf("the agent launched %d times, want the first start and one resume", n)
	}
}

func TestReviewLaneMoveWithFailingInitScriptLeavesTheAgentInTheRoot(t *testing.T) {
	e, task, _, _ := startedInPlanning(t, "echo nope; exit 1", "")

	if err := e.Move(task.ID, "triage", MoveOptions{}); err != nil {
		t.Fatalf("Move = %v, want the move to succeed", err)
	}
	waitFor(t, e, "the worktree failure to be logged", func(t *store.Task) bool {
		return slices.ContainsFunc(t.Log, func(l store.LogEntry) bool { return strings.Contains(l.Text, "the agent stays in the project root") })
	})
	requireInRoot(t, e)
}
