package engine

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/store"
)

const lifecycleBoard = `{%s"columns":[
  {"id":"todo","name":"To Do","role":"todo"},
  {"id":"exec","name":"Executing","autoSpawn":true},
  {"id":"done","name":"Done","role":"done"}]}`

func startIdleAgent(t *testing.T, e *Engine, title string) *store.Task {
	t.Helper()
	task := e.CreateTask(title, "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, title+" to finish its turn", func(tasks []*store.Task) bool {
		for _, other := range tasks {
			if s := other.Sessions[store.MainSession]; other.ID == task.ID && s != nil && idle(s) {
				return true
			}
		}
		return false
	})
	return task
}

func TestTurnDoneNotifiesUnlessFocused(t *testing.T) {
	e := startWithBoard(t, fmt.Sprintf(lifecycleBoard, ""))
	loudBefore, quietBefore := len(desktopNotified("#2 Loud agent here:")), len(desktopNotified("#1 Quiet agent here:"))
	quiet := e.CreateTask("Quiet agent here", "")
	e.SetFocused(quiet.ID)
	if err := e.Move(quiet.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	startIdleAgent(t, e, "Loud agent here")

	deadline := time.Now().Add(5 * time.Second)
	for len(desktopNotified("#2 Loud agent here:")) == loudBefore && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := desktopNotified("#2 Loud agent here:")[loudBefore:]; len(got) != 1 || got[0] != "#2 Loud agent here: finished: Did the work." {
		t.Errorf("loud notifications = %q", got)
	}
	if got := desktopNotified("#1 Quiet agent here:")[quietBefore:]; len(got) != 0 {
		t.Errorf("the focused agent notified: %q", got)
	}
}

func TestIdleTimeoutSuspendsUnfocusedAgents(t *testing.T) {
	e := startWithBoard(t, fmt.Sprintf(lifecycleBoard, `"idleTimeoutMinutes":1,`))
	task := startIdleAgent(t, e, "Sleepy agent")
	e.SetFocused(task.ID)
	backdate := func() {
		e.with(func() { e.find(task.ID).Sessions[store.MainSession].UpdatedAt = time.Now().Add(-2 * time.Minute) })
	}
	backdate()
	time.Sleep(time.Second)
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.Status != store.StatusRunning {
		t.Fatalf("focused agent was suspended: %+v", s)
	}

	e.SetFocused("")
	backdate()
	waitFor(t, e, "the idle agent to be suspended", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Status == store.StatusSuspended && hasLog(t, "Agent suspended after 1 min idle")
	})
}

func TestBoardSetsAgentLimit(t *testing.T) {
	e := startWithBoard(t, fmt.Sprintf(lifecycleBoard, `"maxConcurrent":3,`))
	if got := e.Snapshot().MaxConcurrent; got != 3 {
		t.Errorf("limit = %d, want 3", got)
	}
}

func TestUsageAddsUpAcrossTranscripts(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e := startWithBoard(t, fmt.Sprintf(lifecycleBoard, ""))
	task := startIdleAgent(t, e, "Count my tokens")
	write := func(id string) {
		var path string
		e.with(func() {
			s := e.find(task.ID).Sessions[store.MainSession]
			s.AgentSessionID = id
			path = e.transcriptOf(e.find(task.ID), s)
		})
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(`{"type":"assistant","message":{"id":"`+id+`","model":"claude-sonnet-5-5","usage":{"input_tokens":100000,"output_tokens":100000}}}`+"\n"), 0o644)
		e.scanUsage(task.ID, store.MainSession)
	}
	write("first")
	write("after-clear")
	u := e.Snapshot().Tasks[0].Sessions[store.MainSession].Usage
	if math.Abs(u.Cost-2.4) > 1e-9 || u.Context != 100_000 {
		t.Errorf("usage = %+v, want $2.40 and the last prompt's 100K tokens", u)
	}
}

func TestAutoNameTitlesNewSessions(t *testing.T) {
	t.Setenv("FAKE_TITLE", `"Fix Safari login."`)
	e := startWithBoard(t, fmt.Sprintf(lifecycleBoard, ""))
	task, err := e.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	ready := waitFor(t, e, "scratch session to start", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle
	})
	events, err := os.OpenFile(filepath.Join(e.sessionDir(ready, store.MainSession), "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	events.WriteString(`{"hook_event_name":"UserPromptSubmit","prompt":"the login is broken on safari, can you look"}` + "\n")
	events.Close()
	waitFor(t, e, "the haiku title", func(t *store.Task) bool { return t.Title == "Fix Safari login" })

	e.EditTask(task.ID, "My own title", "")
	e.autoName(task.ID, "anything", "Fix Safari login")
	if got := e.Snapshot().Tasks[0].Title; got != "My own title" {
		t.Errorf("auto-naming overwrote an edited title: %q", got)
	}
}

const routingBoard = `{"columns":[
  {"id":"todo","name":"To Do","role":"todo"},
  {"id":"plan","name":"Planning","autoSpawn":true,"permissionMode":"plan"},
  {"id":"exec","name":"Executing","autoSpawn":true},
  {"id":"done","name":"Done","role":"done"}]}`

// promptScratch starts a session in the main checkout and has its agent emit
// the hook events in lines once it is idle.
func promptScratch(t *testing.T, e *Engine, lines ...string) {
	t.Helper()
	if _, err := e.NewSession(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "scratch session to start", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Activity == store.ActivityIdle
	})
	emitEvents(t, e, lines...)
}

// emitEvents has the only card's agent emit the hook events in lines.
func emitEvents(t *testing.T, e *Engine, lines ...string) {
	t.Helper()
	task := e.Snapshot().Tasks[0]
	events, err := os.OpenFile(filepath.Join(e.sessionDir(task, store.MainSession), "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	for _, line := range lines {
		events.WriteString(line + "\n")
	}
}

const typoPrompt = `{"hook_event_name":"UserPromptSubmit","prompt":"fix the typo in the readme"}`

func TestFirstPromptRoutesPastPlanning(t *testing.T) {
	for _, c := range []struct{ route, want string }{
		{"DIRECT", "exec"},
		{"PLAN", "plan"},
		{"MAYBE", "plan"},
		{"FAIL", "plan"},
	} {
		t.Run(c.route, func(t *testing.T) {
			t.Setenv("FAKE_ROUTE", c.route)
			e := startWithBoard(t, routingBoard)
			promptScratch(t, e, typoPrompt)
			waitFor(t, e, "the card to leave To Do", func(t *store.Task) bool { return t.ColumnID != "todo" })
			task := e.Snapshot().Tasks[0]
			if task.ColumnID != c.want {
				t.Errorf("routed %q to %q, want %q", c.route, task.ColumnID, c.want)
			}
			if wantLog := map[string]string{"FAIL": "using a worktree", "MAYBE": "unexpected answer"}[c.route]; wantLog != "" && !hasLog(task, wantLog) {
				t.Errorf("no %q in the log", wantLog)
			}
		})
	}
}

const questionPrompt = `{"hook_event_name":"UserPromptSubmit","prompt":"how does the board pick a column"}`

// stayingSession starts a session whose first prompt is judged to need no
// worktree and waits until it is idle and staying in the main checkout.
func stayingSession(t *testing.T, board string) *Engine {
	t.Helper()
	t.Setenv("FAKE_ROUTE", "STAY")
	e := startWithBoard(t, board)
	promptScratch(t, e, questionPrompt, `{"hook_event_name":"Stop"}`)
	waitFor(t, e, "the session to stay", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return hasLog(t, "no worktree needed") && s.Activity == store.ActivityIdle
	})
	return e
}

func assertStaying(t *testing.T, e *Engine) {
	t.Helper()
	task := e.Snapshot().Tasks[0]
	s := task.Sessions[store.MainSession]
	if task.ColumnID != "todo" || task.WorktreePath != "" || !s.Scratch || !s.Stay || s.Status != store.StatusRunning {
		t.Errorf("session left the main checkout: column %q, worktree %q, %+v", task.ColumnID, task.WorktreePath, s)
	}
}

func TestSessionsThatNeedNoWorktreeStayInTheMainCheckout(t *testing.T) {
	for _, c := range []struct{ name, board string }{
		{"with a plan column", routingBoard},
		{"without one", fmt.Sprintf(lifecycleBoard, "")},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := stayingSession(t, c.board)
			time.Sleep(500 * time.Millisecond)
			assertStaying(t, e)
		})
	}
}

func TestStayingSessionIgnoresSlashCommandsAndStops(t *testing.T) {
	e := stayingSession(t, routingBoard)
	emitEvents(t, e, `{"hook_event_name":"UserPromptSubmit","prompt":"/compact"}`, `{"hook_event_name":"Stop"}`)
	waitFor(t, e, "the turn to end", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Activity == store.ActivityIdle && !t.Sessions[store.MainSession].TurnInVisit
	})
	time.Sleep(500 * time.Millisecond)
	assertStaying(t, e)
}

func TestStayingSessionLeavesWhenALaterPromptNeedsAWorktree(t *testing.T) {
	e := stayingSession(t, routingBoard)
	t.Setenv("FAKE_ROUTE", "DIRECT")
	emitEvents(t, e, typoPrompt)
	moved := waitFor(t, e, "the card to leave To Do", func(t *store.Task) bool { return t.ColumnID != "todo" })
	if moved.ColumnID != "exec" {
		t.Errorf("routed to %q, want exec", moved.ColumnID)
	}
	waitFor(t, e, "the agent in its worktree", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return t.WorktreePath != "" && !s.Scratch && !s.Stay
	})
}

func TestPromptTypedDuringTheRouteCheckIsJudgedToo(t *testing.T) {
	t.Setenv("FAKE_ROUTE", "STAY")
	t.Setenv("FAKE_ROUTE_DELAY", "1")
	e := startWithBoard(t, routingBoard)
	promptScratch(t, e, questionPrompt)
	time.Sleep(300 * time.Millisecond)
	t.Setenv("FAKE_ROUTE", "DIRECT")
	emitEvents(t, e, typoPrompt)
	moved := waitFor(t, e, "the card to leave To Do", func(t *store.Task) bool { return t.ColumnID != "todo" })
	if moved.ColumnID != "exec" {
		t.Errorf("routed to %q, want exec", moved.ColumnID)
	}
}

func TestStayingSessionLeavesWhenTheAgentEditsAFile(t *testing.T) {
	e := stayingSession(t, routingBoard)
	emitEvents(t, e, `{"hook_event_name":"PreToolUse","tool_name":"Edit"}`)
	moved := waitFor(t, e, "the card to leave To Do", func(t *store.Task) bool { return t.ColumnID != "todo" })
	if moved.ColumnID != "exec" {
		t.Errorf("moved to %q, want exec: the agent is already working, so there is nothing to plan", moved.ColumnID)
	}
}

func TestMovingAStayingSessionGivesItAWorktree(t *testing.T) {
	e := stayingSession(t, routingBoard)
	if err := e.Move(e.Snapshot().Tasks[0].ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the agent in its worktree", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return t.WorktreePath != "" && !s.Scratch && !s.Stay && s.Status == store.StatusRunning
	})
}

func TestTurnEndWaitsForTheRoutingVerdict(t *testing.T) {
	t.Setenv("FAKE_ROUTE", "DIRECT")
	t.Setenv("FAKE_ROUTE_DELAY", "1")
	e := startWithBoard(t, routingBoard)
	promptScratch(t, e, typoPrompt, `{"hook_event_name":"Stop"}`)
	time.Sleep(500 * time.Millisecond)
	if got := e.Snapshot().Tasks[0].ColumnID; got != "todo" {
		t.Fatalf("card moved to %q before the verdict", got)
	}
	waitFor(t, e, "the card to leave To Do", func(t *store.Task) bool { return t.ColumnID != "todo" })
	if got := e.Snapshot().Tasks[0].ColumnID; got != "exec" {
		t.Errorf("routed to %q, want exec", got)
	}
}

func TestPermissionRequestsAlertOncePerCooldown(t *testing.T) {
	e := startWithBoard(t, fmt.Sprintf(lifecycleBoard, `"notifications":{"turnDone":false,"needsYou":true,"planReady":true,"cooldownSeconds":60},`))
	task := startIdleAgent(t, e, "Asks a lot")
	prefix := fmt.Sprintf("#%d Asks a lot:", task.Number)
	before := len(desktopNotified(prefix))
	var dir string
	e.with(func() { dir = e.sessionDir(e.find(task.ID), store.MainSession) })
	events, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"Bash", "Bash", "ExitPlanMode"} {
		events.WriteString(`{"hook_event_name":"PermissionRequest","tool_name":"` + tool + `"}` + "\n")
	}
	events.Close()

	want := []string{prefix + " needs you: permission requested", prefix + " has a plan ready for review"}
	deadline := time.Now().Add(5 * time.Second)
	for len(desktopNotified(prefix))-before < len(want) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	if got := desktopNotified(prefix)[before:]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("notifications = %q, want %q", got, want)
	}
}
