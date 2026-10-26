package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/linear"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

func TestNewCardsLandInTheFirstColumnWithoutAnAgent(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	e.CreateTask("By hand", "")
	e.ImportIssues([]linear.Issue{{ID: "i1", Identifier: "DX-1", Title: "Speed up CI", URL: "https://linear.app/x/issue/DX-1"}})
	e.ImportSessions([]claude.Found{{SessionID: "abcdef12-past", Cwd: e.Root, Title: "Past work", FirstPrompt: "Past work"}})

	snap := e.Snapshot()
	if len(snap.Tasks) != 3 {
		t.Fatalf("want 3 cards, got %d", len(snap.Tasks))
	}
	for _, task := range snap.Tasks {
		if task.ColumnID != "planning" {
			t.Errorf("%q landed in %q, want planning", task.Title, task.ColumnID)
		}
		if past := task.Title == "Past work"; past == (len(task.Sessions) == 0) {
			t.Errorf("%q has %d sessions; only the imported conversation has one", task.Title, len(task.Sessions))
		}
		if task.WorktreePath != "" && task.Title != "Past work" {
			t.Errorf("%q got a worktree without an agent", task.Title)
		}
	}
	if snap.Running != 0 {
		t.Errorf("%d agents running, want none", snap.Running)
	}
}

func TestNewSessionStartsItsAgentInAWorktree(t *testing.T) {
	root, log := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	if _, err := e.NewSession(); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, e, "the agent to start", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Status == store.StatusRunning
	})
	if got.ColumnID != "planning" || got.WorktreePath == "" || got.WorktreePath == root {
		t.Errorf("new session: column %q, worktree %q; want Planning in its own worktree", got.ColumnID, got.WorktreePath)
	}
	if s := got.Sessions[store.MainSession]; s.PermissionMode != "plan" || !tmux.Has(s.Tmux) {
		t.Errorf("want a running agent in plan mode, got %+v", s)
	}
	waitForAll(t, e, "the agent to launch in plan mode", func([]*store.Task) bool {
		return strings.Contains(readFile(log), "--permission-mode\nplan")
	})
}

func TestNewSessionNeedsAnAgentInTheFirstColumn(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"inbox","name":"Inbox"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`)
	if _, err := e.NewSession(); err == nil || !strings.Contains(err.Error(), "Inbox") {
		t.Errorf("NewSession on a board whose first column runs no agent = %v", err)
	}
	if n := len(e.Snapshot().Tasks); n != 0 {
		t.Errorf("a refused session left %d cards", n)
	}
}

func TestDiscardSilentDropsOnlyUntouchedNewSessions(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	newSession := func() *store.Task {
		task, err := e.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	silent := newSession()
	silentWorktree, silentBranch := e.find(silent.ID).WorktreePath, e.find(silent.ID).Branch
	titled := newSession()
	e.EditTask(titled.ID, "Titled", "")
	started := newSession()
	e.with(func() { e.find(started.ID).Sessions[store.MainSession].Started = true })
	moved := newSession()
	if err := e.Move(moved.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	notStarted := e.CreateTask("", "")
	withPrompt := newSession()
	e.EditTask(withPrompt.ID, "", "Do the thing")

	for _, task := range []*store.Task{silent, titled, started, moved, notStarted, withPrompt} {
		if err := e.DiscardSilent(task.ID); err != nil {
			t.Fatal(err)
		}
	}
	found := map[string]bool{}
	for _, x := range e.Snapshot().Tasks {
		found[x.ID] = true
	}
	if found[silent.ID] {
		t.Error("a new session left without a message should be dropped")
	}
	if _, err := os.Stat(silentWorktree); !os.IsNotExist(err) {
		t.Errorf("the dropped session's worktree is still there: %v", err)
	}
	branches, _ := exec.Command("git", "-C", e.Root, "branch", "--list", silentBranch).Output()
	if len(branches) != 0 {
		t.Errorf("the dropped session's branch is still there: %s", branches)
	}
	for name, task := range map[string]*store.Task{"titled": titled, "started": started, "moved out of Planning": moved, "never started": notStarted, "with a prompt": withPrompt} {
		if !found[task.ID] {
			t.Errorf("a %s card should be kept", name)
		}
	}
}

func TestMovingBackToTheFirstColumnKeepsTheWorktreeAndConversation(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Round trip", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	before := waitFor(t, e, "the first turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	if err := e.Move(task.ID, "planning", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	after := waitFor(t, e, "the agent to switch to Planning's settings", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return t.ColumnID == "planning" && s != nil && s.Status == store.StatusRunning && s.PermissionMode == "plan"
	})
	if after.WorktreePath != before.WorktreePath || after.Sessions[store.MainSession].AgentSessionID != before.Sessions[store.MainSession].AgentSessionID {
		t.Errorf("moving back reset the card: worktree %q → %q, conversation %q → %q", before.WorktreePath, after.WorktreePath,
			before.Sessions[store.MainSession].AgentSessionID, after.Sessions[store.MainSession].AgentSessionID)
	}
	if _, err := os.Stat(after.WorktreePath); err != nil {
		t.Errorf("worktree gone after moving back: %v", err)
	}
	if hasLog(after, "Worktree removed") {
		t.Errorf("moving back removed the worktree: %+v", after.Log)
	}
}

func TestNotStartedCardHoldsNoSlotAndQueuesWhenStarted(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"maxConcurrent":1,"columns":[
	  {"id":"planning","name":"Planning","autoSpawn":true},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	busy := e.CreateTask("Busy SLOW", "")
	if err := e.Move(busy.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waiting := e.CreateTask("Waiting", "")
	snap := e.Snapshot()
	if snap.Running != 1 || len(snap.Tasks[1].Sessions) != 0 || snap.Tasks[1].ColumnID != "planning" {
		t.Fatalf("a not-started card should sit in Planning with no session and no slot: running %d, %+v", snap.Running, snap.Tasks[1])
	}

	if err := e.Start(waiting.ID); err != nil {
		t.Fatal(err)
	}
	queued := e.Snapshot().Tasks[1].Sessions[store.MainSession]
	if queued == nil || queued.Status != store.StatusQueued {
		t.Fatalf("starting at the limit should queue the agent, got %+v", queued)
	}
	e.Suspend(busy.ID)
	waitForAll(t, e, "the queued agent to take the slot", func(tasks []*store.Task) bool {
		s := tasks[1].Sessions[store.MainSession]
		return s != nil && s.Status == store.StatusRunning
	})
}

const legacyBoard = `{"columns":[
  {"id":"todo","name":"Sessions","role":"todo"},
  {"id":"planning","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
  {"id":"exec","name":"Executing","autoSpawn":true},
  {"id":"done","name":"Done","role":"done"}]}`

func TestLegacyTodoCardsMoveToTheFirstColumn(t *testing.T) {
	root, _ := setupProject(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(legacyBoard), 0o644)

	card := func(n int, title, column string, s *store.Session) *store.Task {
		task := &store.Task{ID: uuid.NewString(), Number: n, Title: title, ColumnID: column, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if s != nil {
			s.Key = store.MainSession
			task.Sessions = map[string]*store.Session{store.MainSession: s}
		}
		return task
	}
	liveName := "repo-1-scratch"
	if err := tmux.NewSession(liveName, root, []string{"sleep", "60"}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tmux.Kill(liveName) })
	live := card(1, "Talked, still running", "todo", &store.Session{AgentSessionID: uuid.NewString(), Tmux: liveName, Status: store.StatusRunning, Started: true})
	vanished := card(2, "Talked, tmux gone", "todo", &store.Session{AgentSessionID: uuid.NewString(), Tmux: "repo-2-gone", Status: store.StatusRunning, Started: true})
	empty := card(3, "", "todo", &store.Session{AgentSessionID: uuid.NewString(), Tmux: "repo-3-empty", Status: store.StatusSuspended})
	reset := card(4, "Reset", "todo", nil)
	working := card(5, "Working", "exec", nil)
	state := &store.State{NextNumber: 6, Tasks: []*store.Task{live, vanished, empty, reset, working}}
	if err := state.Save(root); err != nil {
		t.Fatal(err)
	}

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	byNumber := map[int]*store.Task{}
	for _, task := range e.Snapshot().Tasks {
		byNumber[task.Number] = task
	}
	for n := 1; n <= 4; n++ {
		if got := byNumber[n].ColumnID; got != "planning" {
			t.Errorf("#%d is in %q, want planning", n, got)
		}
		if !hasLog(byNumber[n], "the To Do column was removed") {
			t.Errorf("#%d didn't log the move: %+v", n, byNumber[n].Log)
		}
	}
	if byNumber[5].ColumnID != "exec" {
		t.Errorf("#5 moved to %q", byNumber[5].ColumnID)
	}
	for n, want := range map[int]store.SessionStatus{1: store.StatusSuspended, 2: store.StatusExited} {
		if s := byNumber[n].Sessions[store.MainSession]; s == nil || s.Status != want || !s.Started {
			t.Errorf("#%d session = %+v, want a started %s one to resume", n, s, want)
		}
	}
	if tmux.Has(liveName) {
		t.Error("the scratch agent still runs in the main checkout")
	}
	if len(byNumber[3].Sessions) != 0 || len(byNumber[4].Sessions) != 0 {
		t.Errorf("cards that never talked should read as not started: %v %v", byNumber[3].Sessions, byNumber[4].Sessions)
	}
}

func TestDefaultBoardWithAnOldTodoCardMovesItToPlanning(t *testing.T) {
	root, _ := setupProject(t)
	os.Remove(filepath.Join(root, "drover.json"))
	state := &store.State{NextNumber: 2, Tasks: []*store.Task{{ID: uuid.NewString(), Number: 1, Title: "Old", ColumnID: "todo", CreatedAt: time.Now(), UpdatedAt: time.Now()}}}
	if err := state.Save(root); err != nil {
		t.Fatal(err)
	}
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Snapshot().Tasks[0].ColumnID; got != "planning" {
		t.Errorf("an old To Do card is in %q, want planning", got)
	}
}

func TestStartingANewCardRunsTheFirstColumnsEnterAutomations(t *testing.T) {
	root, _ := setupProject(t)
	marker := filepath.Join(t.TempDir(), "entered")
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(`{"columns":[
	  {"id":"planning","name":"Planning","autoSpawn":true,"automations":{"onEnter":[
	    {"name":"Mark","type":"run_script","script":"echo x >> `+marker+`"}]}},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	task := e.CreateTask("Enter once", "")
	if err := e.Start(task.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the agent to start", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Status == store.StatusRunning
	})
	e.Suspend(task.ID)
	if err := e.Start(task.ID); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the enter script to run", func([]*store.Task) bool { return strings.Count(readFile(marker), "x") >= 1 })
	time.Sleep(500 * time.Millisecond)
	if got := strings.Count(readFile(marker), "x"); got != 1 {
		t.Errorf("the enter script ran %d times; resuming must not run it again", got)
	}
}

func TestDiscardSilentKeepsASessionWhoseFirstPromptIsUnread(t *testing.T) {
	e := newEngineWithClaudeDir(t)
	task, err := e.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	card := e.Snapshot().Tasks[0]
	events, err := os.OpenFile(filepath.Join(e.sessionDir(card, store.MainSession), "events.jsonl"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	events.WriteString(`{"hook_event_name":"UserPromptSubmit","prompt":"fix the typo"}` + "\n")
	events.Close()
	if err := e.DiscardSilent(task.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.Snapshot().Tasks) != 1 {
		t.Error("a session that was already typed into was discarded")
	}
}
