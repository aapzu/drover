package engine

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

func TestIdleSkipsBackgroundAgents(t *testing.T) {
	s := &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true}
	if !idle(s) {
		t.Fatal("an idle agent with nothing running should be idle")
	}
	s.Background = []string{"toolu_1"}
	if idle(s) {
		t.Error("an agent with a background agent still running should not be idle")
	}
}

const lifecycleBoard = `{%s"columns":[
  {"id":"plan","name":"Planning","autoSpawn":true},
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

func TestIdleTimeoutSparesKeptAgent(t *testing.T) {
	e := startWithBoard(t, `{"idleTimeoutMinutes":1,"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"review","name":"In review","keepAgent":true},
	  {"id":"done","name":"Done","role":"done"}]}`)
	task := startIdleAgent(t, e, "Watched PR")
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	e.with(func() { e.find(task.ID).Sessions[store.MainSession].UpdatedAt = time.Now().Add(-2 * time.Minute) })
	plain := startIdleAgent(t, e, "Plain agent")
	e.with(func() { e.find(plain.ID).Sessions[store.MainSession].UpdatedAt = time.Now().Add(-2 * time.Minute) })
	waitForAll(t, e, "the plain agent to be suspended", func(tasks []*store.Task) bool {
		for _, other := range tasks {
			if other.ID == plain.ID {
				return other.Sessions[store.MainSession].Status == store.StatusSuspended
			}
		}
		return false
	})
	if task := e.Snapshot().Tasks[0]; task.Sessions[store.MainSession].Status != store.StatusRunning || hasLog(task, "suspended after") {
		t.Fatalf("idle timeout suspended a kept agent: %+v", task.Sessions[store.MainSession])
	}
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
	ready := waitFor(t, e, "the new session to start", func(t *store.Task) bool {
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

func TestEnabledPicksTheNotificationForTheKind(t *testing.T) {
	kinds := map[string]alertKind{"turn done": alertTurnDone, "needs you": alertNeedsYou, "plan ready": alertPlanReady, "exited": alertExited}
	only := map[alertKind]config.Notifications{
		alertTurnDone:  {TurnDone: true},
		alertNeedsYou:  {NeedsYou: true},
		alertPlanReady: {PlanReady: true},
		alertExited:    {Exited: true},
	}
	for name, kind := range kinds {
		if enabled(config.Notifications{}, kind) {
			t.Errorf("%s: enabled with every notification off", name)
		}
		for otherName, other := range kinds {
			if got := enabled(only[other], kind); got != (other == kind) {
				t.Errorf("%s with only %s on = %v", name, otherName, got)
			}
		}
	}
}

func TestIdleFor(t *testing.T) {
	idleSince := func(ago time.Duration) *store.Session {
		return &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true, UpdatedAt: time.Now().Add(-ago)}
	}
	busy := idleSince(time.Hour)
	busy.Activity = store.ActivityThinking
	for name, tc := range map[string]struct {
		s    *store.Session
		want bool
	}{
		"nil":           {nil, false},
		"idle long":     {idleSince(time.Hour), true},
		"idle just now": {idleSince(time.Second), false},
		"working":       {busy, false},
	} {
		if got := idleFor(tc.s, time.Minute); got != tc.want {
			t.Errorf("%s: idleFor = %v, want %v", name, got, tc.want)
		}
	}
}
