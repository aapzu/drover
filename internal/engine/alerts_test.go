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

func TestIdleMeansAStartedAgentWaitingForYouWithNothingQueued(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*store.Session)
		want   bool
	}{
		"waiting for you":        {func(*store.Session) {}, true},
		"a background agent":     {func(s *store.Session) { s.Background = []string{"toolu_1"} }, false},
		"a queued message":       {func(s *store.Session) { s.Pending = []store.Pending{{Text: "next"}} }, false},
		"a respawn pending":      {func(s *store.Session) { s.RespawnPending = true }, false},
		"not started yet":        {func(s *store.Session) { s.Started = false }, false},
		"working":                {func(s *store.Session) { s.Activity = store.ActivityThinking }, false},
		"waiting for permission": {func(s *store.Session) { s.Activity = store.ActivityPermission }, false},
		"suspended":              {func(s *store.Session) { s.Status = store.StatusSuspended }, false},
	} {
		s := &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true}
		tc.change(s)
		if got := idle(s); got != tc.want {
			t.Errorf("%s: idle = %v, want %v", name, got, tc.want)
		}
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

func TestIdleFor(t *testing.T) {
	busy := idleAgent("", time.Hour)
	busy.Activity = store.ActivityThinking
	for name, tc := range map[string]struct {
		s    *store.Session
		want bool
	}{
		"nil":           {nil, false},
		"idle long":     {idleAgent("", time.Hour), true},
		"idle just now": {idleAgent("", time.Second), false},
		"working":       {busy, false},
	} {
		if got := idleFor(tc.s, time.Minute); got != tc.want {
			t.Errorf("%s: idleFor = %v, want %v", name, got, tc.want)
		}
	}
}

func TestIdleTimeoutSuspendsOnlyUnfocusedAgentsThatAreNotKept(t *testing.T) {
	e := boardEngine(t, `{"idleTimeoutMinutes":1,"columns":[
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"review","name":"In review","keepAgent":true},
	  {"id":"done","name":"Done","role":"done"}]}`)
	sleepy, focused, recent, kept := agentCard("c1", 1, "exec", 2*time.Minute), agentCard("c2", 2, "exec", 2*time.Minute), agentCard("c3", 3, "exec", 10*time.Second), agentCard("c4", 4, "review", 2*time.Minute)
	e.state.Tasks = []*store.Task{sleepy, focused, recent, kept}
	e.focused = focused.ID

	followUps := e.idleTimeouts()
	if len(followUps) != 1 {
		t.Fatalf("%d agents due for suspension, want only the unfocused, unkept, long-idle one", len(followUps))
	}
	if again := e.idleTimeouts(); len(again) != 0 {
		t.Errorf("an agent already being suspended was picked again")
	}
	followUps[0]()

	for _, c := range []struct {
		card *store.Task
		want store.SessionStatus
	}{{sleepy, store.StatusSuspended}, {focused, store.StatusRunning}, {recent, store.StatusRunning}, {kept, store.StatusRunning}} {
		if got := c.card.Sessions[store.MainSession].Status; got != c.want {
			t.Errorf("#%d is %s, want %s", c.card.Number, got, c.want)
		}
	}
	if !hasLog(sleepy, "Agent suspended after 1 min idle") {
		t.Errorf("log = %v", sleepy.Log)
	}
}

func TestIdleTimeoutIsOffByDefault(t *testing.T) {
	e := boardEngine(t, `{"columns":[{"id":"exec","name":"Executing","autoSpawn":true},{"id":"done","name":"Done","role":"done"}]}`)
	e.state.Tasks = []*store.Task{agentCard("c1", 1, "exec", 24*time.Hour)}
	if followUps := e.idleTimeouts(); len(followUps) != 0 {
		t.Errorf("%d agents suspended without an idleTimeoutMinutes", len(followUps))
	}
}

func TestSuspendIdleChecksAgainBeforeStopping(t *testing.T) {
	e := boardEngine(t, `{"idleTimeoutMinutes":1,"columns":[{"id":"exec","name":"Executing","autoSpawn":true},{"id":"done","name":"Done","role":"done"}]}`)
	card := agentCard("c1", 1, "exec", 2*time.Minute)
	e.state.Tasks = []*store.Task{card}
	followUps := e.idleTimeouts()
	if len(followUps) != 1 {
		t.Fatalf("%d agents due, want 1", len(followUps))
	}

	e.SetFocused(card.ID)
	followUps[0]()
	if got := card.Sessions[store.MainSession].Status; got != store.StatusRunning {
		t.Errorf("an agent you focused after the check was suspended: %s", got)
	}
}

func TestSetFocusedResolvesToTheDrivingOwner(t *testing.T) {
	e := bareEngine(t)
	owner := &store.Task{ID: "o", Number: 1}
	member := &store.Task{ID: "m", Number: 2, GroupID: "o"}
	e.state.Tasks = []*store.Task{owner, member}
	e.SetFocused("m")
	if e.focused != "o" {
		t.Errorf("focused = %q, want the group's owner", e.focused)
	}
	e.SetFocused("")
	if e.focused != "" {
		t.Errorf("focused = %q after clearing", e.focused)
	}
}

func TestPermissionRequestsAlertOncePerCooldown(t *testing.T) {
	e := bareEngine(t)
	task := &store.Task{ID: "t1", Number: 41, Title: "Asks a lot"}
	e.state.Tasks = []*store.Task{task}
	prefix := "#41 Asks a lot:"

	consume(t, e, task, &store.Session{Activity: store.ActivityThinking},
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
		`{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode"}`)

	want := []string{prefix + " needs you: permission requested", prefix + " has a plan ready for review"}
	if got := desktopNotified(prefix); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("notifications = %q, want %q", got, want)
	}
}

func TestAlertsRespectTheNotificationSettingsAndFocus(t *testing.T) {
	request := `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`
	for name, tc := range map[string]struct {
		setup func(*Engine, *store.Task)
		want  int
	}{
		"on":                   {func(*Engine, *store.Task) {}, 1},
		"switched off":         {func(e *Engine, _ *store.Task) { e.board.Notifications.NeedsYou = false }, 0},
		"you are in that card": {func(e *Engine, t *store.Task) { e.focused = t.ID }, 0},
	} {
		e := bareEngine(t)
		task := &store.Task{ID: "t1", Number: 42, Title: "Settings " + name}
		e.state.Tasks = []*store.Task{task}
		tc.setup(e, task)
		consume(t, e, task, &store.Session{Activity: store.ActivityThinking}, request)
		if got := desktopNotified("#42 Settings " + name + ":"); len(got) != tc.want {
			t.Errorf("%s: notified %q, want %d", name, got, tc.want)
		}
	}
}

func TestEachNotificationKindHasItsOwnSwitch(t *testing.T) {
	kinds := []struct {
		name      string
		send      func(*Engine, *store.Task)
		switchOff func(*config.Notifications)
	}{
		{"turn done", func(e *Engine, t *store.Task) { e.alertTurnDone(t.ID, store.MainSession, "Did the work.") },
			func(n *config.Notifications) { n.TurnDone = false }},
		{"needs you", func(e *Engine, t *store.Task) { e.alert(t, alertNeedsYou, "needs you: permission requested") },
			func(n *config.Notifications) { n.NeedsYou = false }},
		{"plan ready", func(e *Engine, t *store.Task) { e.alert(t, alertPlanReady, "has a plan ready for review") },
			func(n *config.Notifications) { n.PlanReady = false }},
		{"exited", func(e *Engine, t *store.Task) { e.alert(t, alertExited, "exited") },
			func(n *config.Notifications) { n.Exited = false }},
	}
	for _, off := range kinds {
		for _, sent := range kinds {
			e := bareEngine(t)
			e.board.Notifications = config.Notifications{TurnDone: true, NeedsYou: true, PlanReady: true, Exited: true}
			off.switchOff(&e.board.Notifications)
			card := agentCard("c45", 45, "exec", 0)
			card.Title = "Switches"
			e.state.Tasks = []*store.Task{card}
			sent.send(e, card)
			want := 1
			if sent.name == off.name {
				want = 0
			}
			if got := len(desktopNotified("#45 Switches:")); got != want {
				t.Errorf("with %s off, a %s alert sent %d notifications, want %d", off.name, sent.name, got, want)
			}
		}
	}
}

func TestStopFailureAlerts(t *testing.T) {
	e := bareEngine(t)
	e.board.Notifications.CooldownSeconds = 0
	task := &store.Task{ID: "t1", Number: 43, Title: "Hits errors"}
	e.state.Tasks = []*store.Task{task}
	prefix := "#43 Hits errors:"

	consume(t, e, task, &store.Session{Activity: store.ActivityThinking},
		`{"hook_event_name":"StopFailure","error":"rate_limit"}`,
		`{"hook_event_name":"StopFailure"}`)

	want := []string{prefix + " turn failed: rate_limit", prefix + " turn failed: unknown error"}
	if got := desktopNotified(prefix); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("notifications = %q, want %q", got, want)
	}
}

func TestAlertTurnDoneOnlyWhenTheAgentIsStillIdle(t *testing.T) {
	e := bareEngine(t)
	e.board.Notifications.CooldownSeconds = 0
	card := agentCard("c44", 44, "exec", 0)
	card.Title = "Finishing up"
	e.state.Tasks = []*store.Task{card}
	prefix := "#44 Finishing up:"

	e.alertTurnDone(card.ID, store.MainSession, "Did the work.\n\nMore detail")
	if got := desktopNotified(prefix); len(got) != 1 || got[0] != prefix+" finished: Did the work." {
		t.Fatalf("notifications = %q", got)
	}

	card.Sessions[store.MainSession].Activity = store.ActivityThinking
	e.alertTurnDone(card.ID, store.MainSession, "Another reply")
	if got := desktopNotified(prefix); len(got) != 1 {
		t.Errorf("notified for an agent that began another turn: %q", got)
	}
}
