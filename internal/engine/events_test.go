package engine

import (
	"fmt"
	"os"
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

// consume writes hook event lines for a session and applies them on e,
// returning the follow-ups consumeEvents asks for.
func consume(t *testing.T, e *Engine, task *store.Task, s *store.Session, lines ...string) []func() {
	t.Helper()
	s.Dir = t.TempDir()
	path := filepath.Join(s.Dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if events, _, _ := claude.ReadEvents(path, 0); len(events) != len(lines) {
		t.Fatalf("parsed %d of %d event lines", len(events), len(lines))
	}
	task.Sessions = map[string]*store.Session{store.MainSession: s}
	return e.consumeEvents(task, store.MainSession, s)
}

func consumeLines(t *testing.T, s *store.Session, lines ...string) {
	t.Helper()
	consume(t, &Engine{}, &store.Task{ID: "t1"}, s, lines...)
}

func TestStopHookLoopMovesToWaiting(t *testing.T) {
	const hookStop = `{"hook_event_name":"Stop","stop_hook_active":true,"last_assistant_message":"Still waiting on you."}`
	const tool = `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`
	const prompt = `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`
	const failure = `{"hook_event_name":"StopFailure","error":"server_error"}`
	const turnEnd = 3
	for name, tc := range map[string]struct {
		lines     []string
		hookStops int
		followUps int
		logged    int
	}{
		"a Stop forced by a hook ends the turn": {[]string{hookStop}, 1, turnEnd, 0},
		"the second forced Stop is skipped":     {[]string{hookStop, hookStop}, 2, turnEnd, 0},
		"the loop is detected":                  {[]string{hookStop, hookStop, hookStop}, 3, 2 * turnEnd, 1},
		"the loop is reported once":             {[]string{hookStop, hookStop, hookStop, hookStop, hookStop}, 5, 2 * turnEnd, 1},
		"a tool call breaks the run":            {[]string{hookStop, hookStop, tool, hookStop}, 1, 2 * turnEnd, 0},
		"a prompt breaks the run":               {[]string{hookStop, hookStop, prompt, hookStop}, 1, 2 * turnEnd, 0},
		"a failed turn breaks the run":          {[]string{hookStop, hookStop, failure, hookStop}, 1, 2 * turnEnd, 0},
		"a normal Stop ends the run":            {[]string{hookStop, hookStop, `{"hook_event_name":"Stop"}`}, 0, 2 * turnEnd, 0},
	} {
		t.Run(name, func(t *testing.T) {
			e := &Engine{board: &config.Board{}}
			task := &store.Task{ID: "t1"}
			e.state = &store.State{Tasks: []*store.Task{task}}
			s := &store.Session{Activity: store.ActivityThinking}
			followUps := consume(t, e, task, s, tc.lines...)
			if s.HookStops != tc.hookStops || len(followUps) != tc.followUps {
				t.Errorf("%d hook stops with %d follow-ups, want %d with %d", s.HookStops, len(followUps), tc.hookStops, tc.followUps)
			}
			logged := 0
			for _, l := range task.Log {
				if strings.Contains(l.Text, "can't stop") {
					logged++
				}
			}
			if logged != tc.logged {
				t.Errorf("logged the loop %d times, want %d", logged, tc.logged)
			}
		})
	}
}

func TestStopTracksRunningBackgroundTasks(t *testing.T) {
	for _, c := range []struct {
		name string
		stop string
		want []string
	}{
		{"no tasks listed", `{"hook_event_name":"Stop","background_tasks":[]}`, nil},
		{"none running", `{"hook_event_name":"Stop","background_tasks":[{"id":"a1","status":"completed"}]}`, nil},
		{"shell running", `{"hook_event_name":"Stop","background_tasks":[{"id":"b1","type":"shell","status":"running"},{"id":"a1","status":"completed"}]}`, []string{"b1"}},
		{"agent still running", `{"hook_event_name":"Stop","background_tasks":[{"id":"a1","status":"running"}]}`, []string{"a1"}},
		{"field absent", `{"hook_event_name":"Stop"}`, []string{"toolu_1"}},
	} {
		s := &store.Session{Background: []string{"toolu_1"}}
		consumeLines(t, s, c.stop)
		if !slices.Equal(s.Background, c.want) {
			t.Errorf("%s: background %v, want %v", c.name, s.Background, c.want)
		}
	}
}

func TestRespawnWaitsForBackgroundAgents(t *testing.T) {
	s := &store.Session{Status: store.StatusRunning, RespawnPending: true, Background: []string{"toolu_1"}}
	task := &store.Task{ID: "t1", Sessions: map[string]*store.Session{store.MainSession: s}}
	e := &Engine{state: &store.State{Tasks: []*store.Task{task}}, board: &config.Board{}}
	e.respawnPending("t1", store.MainSession)
	if !s.RespawnPending {
		t.Error("respawn ran while a background agent was still running")
	}
}

func TestWaitsForBackgroundExceptWhenEnteringPlan(t *testing.T) {
	for _, c := range []struct {
		name       string
		background []string
		current    string
		mode       string
		want       bool
	}{
		{"model change in plan mode", []string{"toolu_1"}, "plan", "plan", true},
		{"other mode", []string{"toolu_1"}, "plan", "acceptEdits", true},
		{"entering plan", []string{"toolu_1"}, "acceptEdits", "plan", false},
		{"nothing running", nil, "plan", "plan", false},
	} {
		s := &store.Session{Background: c.background, PermissionMode: c.current}
		if got := waitsForBackground(s, c.mode); got != c.want {
			t.Errorf("%s: waits %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEventsFollowTheMovedTranscript(t *testing.T) {
	s := &store.Session{TranscriptPath: "/old/a.jsonl"}
	consumeLines(t, s, `{"hook_event_name":"PostToolUse","tool_name":"Bash","transcript_path":"/new/a.jsonl"}`)
	if s.TranscriptPath != "/new/a.jsonl" {
		t.Errorf("transcript %q, want /new/a.jsonl", s.TranscriptPath)
	}
}

func TestConsumeEventsTracksTheAgentsActivity(t *testing.T) {
	for name, tc := range map[string]struct {
		session  *store.Session
		title    string
		line     string
		activity store.Activity
		check    func(t *testing.T, task *store.Task, s *store.Session)
	}{
		"startup ends starting": {
			session: &store.Session{Activity: store.ActivityStarting}, line: `{"hook_event_name":"SessionStart","source":"startup"}`,
			activity: store.ActivityIdle,
		},
		"clear starts a new conversation": {
			session: &store.Session{Activity: store.ActivityThinking, TurnInVisit: true, TurnEndRow: "Row"}, line: `{"hook_event_name":"SessionStart","source":"clear"}`,
			activity: store.ActivityIdle,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if s.TurnInVisit || s.TurnEndRow != "" {
					t.Errorf("clear kept the old turn: %+v", s)
				}
			},
		},
		"resume under a new id": {
			session: &store.Session{Activity: store.ActivityIdle, AgentSessionID: "old", PRScan: 5, Background: []string{"b1"}},
			line:    `{"hook_event_name":"SessionStart","source":"resume","session_id":"new"}`, activity: store.ActivityIdle,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if s.AgentSessionID != "new" || s.PRScan != 0 || s.Background != nil {
					t.Errorf("session after resume = %+v", s)
				}
			},
		},
		"a blank startup in a started pane is ignored": {
			session: &store.Session{Activity: store.ActivityThinking, Started: true, AgentSessionID: "old"},
			line:    `{"hook_event_name":"SessionStart","source":"startup","session_id":"other"}`, activity: store.ActivityThinking,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if s.AgentSessionID != "old" {
					t.Errorf("followed the blank conversation: %q", s.AgentSessionID)
				}
			},
		},
		"a prompt starts a turn and names the card": {
			session: &store.Session{Activity: store.ActivityIdle}, line: `{"hook_event_name":"UserPromptSubmit","prompt":"Fix the typo\nin the readme"}`,
			activity: store.ActivityThinking,
			check: func(t *testing.T, task *store.Task, s *store.Session) {
				if !s.Started || !s.TurnInVisit || task.Title != "Fix the typo" || task.Prompt != "Fix the typo\nin the readme" {
					t.Errorf("after the prompt: session %+v, title %q, prompt %q", s, task.Title, task.Prompt)
				}
			},
		},
		"a slash command does not name the card": {
			session: &store.Session{Activity: store.ActivityIdle}, line: `{"hook_event_name":"UserPromptSubmit","prompt":"/review"}`,
			activity: store.ActivityThinking,
			check: func(t *testing.T, task *store.Task, _ *store.Session) {
				if task.Title != "" {
					t.Errorf("title = %q", task.Title)
				}
			},
		},
		"a titled card keeps its title": {
			session: &store.Session{Activity: store.ActivityIdle}, title: "Mine", line: `{"hook_event_name":"UserPromptSubmit","prompt":"something else"}`,
			activity: store.ActivityThinking,
			check: func(t *testing.T, task *store.Task, _ *store.Session) {
				if task.Title != "Mine" {
					t.Errorf("title = %q", task.Title)
				}
			},
		},
		"a tool call": {
			session: &store.Session{Activity: store.ActivityIdle}, line: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, activity: store.ActivityThinking,
		},
		"a background agent launches": {
			session: &store.Session{Activity: store.ActivityIdle}, activity: store.ActivityThinking,
			line: `{"hook_event_name":"PostToolUse","tool_name":"Agent","tool_use_id":"toolu_9","tool_response":{"status":"async_launched"}}`,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if !slices.Equal(s.Background, []string{"toolu_9"}) {
					t.Errorf("background = %v", s.Background)
				}
			},
		},
		"the plan is approved": {
			session: &store.Session{Activity: store.ActivityIdle}, line: `{"hook_event_name":"PostToolUse","tool_name":"ExitPlanMode"}`,
			activity: store.ActivityThinking,
		},
		"a permission request": {
			session: &store.Session{Activity: store.ActivityThinking}, line: `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`, activity: store.ActivityPermission,
		},
		"a plan ready for review": {
			session: &store.Session{Activity: store.ActivityThinking}, line: `{"hook_event_name":"PermissionRequest","tool_name":"ExitPlanMode"}`, activity: store.ActivityPermission,
		},
		"a permission notification": {
			session: &store.Session{Activity: store.ActivityThinking}, line: `{"hook_event_name":"Notification","notification_type":"permission_prompt"}`, activity: store.ActivityPermission,
		},
		"another notification": {
			session: &store.Session{Activity: store.ActivityThinking}, line: `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, activity: store.ActivityThinking,
		},
		"the turn ends": {
			session: &store.Session{Activity: store.ActivityThinking, Summary: "old"}, line: `{"hook_event_name":"Stop","last_assistant_message":"All done."}`,
			activity: store.ActivityIdle,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if !s.Started || s.LastReply != "All done." || s.Summary != "" {
					t.Errorf("after the stop: %+v", s)
				}
			},
		},
		"the turn of a visit ends": {
			session: &store.Session{Activity: store.ActivityThinking, TurnInVisit: true}, line: `{"hook_event_name":"Stop"}`,
			activity: store.ActivityIdle,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if s.TurnInVisit {
					t.Error("the turn still counts as in the visit")
				}
			},
		},
		"a failed turn goes idle and holds the queue": {
			session: &store.Session{Activity: store.ActivityThinking, TurnInVisit: true, TurnEndRow: "Row", Pending: []store.Pending{{Text: "next"}}},
			line:    `{"hook_event_name":"StopFailure","error":"rate_limit"}`, activity: store.ActivityIdle,
			check: func(t *testing.T, task *store.Task, s *store.Session) {
				if !s.Started || s.TurnInVisit || s.TurnEndRow != "" || len(s.Pending) != 1 || !hasLog(task, "Turn failed: rate_limit; 1 queued message(s) held") {
					t.Errorf("after the failure: %+v, log %v", s, task.Log)
				}
			},
		},
		"the permission mode follows the agent": {
			session: &store.Session{Activity: store.ActivityThinking, PermissionMode: "plan"}, line: `{"hook_event_name":"PreToolUse","permission_mode":"acceptEdits"}`,
			activity: store.ActivityThinking,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if s.PermissionMode != "acceptEdits" {
					t.Errorf("permission mode = %q", s.PermissionMode)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := &Engine{board: &config.Board{AutoName: true}}
			task := &store.Task{ID: "t1", Title: tc.title}
			e.state = &store.State{Tasks: []*store.Task{task}}
			before := time.Now()
			consume(t, e, task, tc.session, tc.line)
			if tc.session.Activity != tc.activity {
				t.Errorf("activity = %q, want %q", tc.session.Activity, tc.activity)
			}
			if tc.session.UpdatedAt.Before(before) || tc.session.EventsOffset == 0 {
				t.Errorf("the session was not marked as updated: %+v", tc.session)
			}
			if tc.check != nil {
				tc.check(t, task, tc.session)
			}
		})
	}
}

func TestBackgroundScansDropFinishedTasks(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "sid.jsonl")
	os.WriteFile(transcript, []byte(`{"type":"user","message":"<task-notification><task-id>b1</task-id></task-notification>"}`+"\n"), 0o644)
	running := func() *store.Session {
		return &store.Session{Status: store.StatusRunning, AgentSessionID: "sid", TranscriptPath: filepath.Join(dir, "other.jsonl"), Background: []string{"b1", "b2"}}
	}
	s := running()
	skipped := []*store.Session{
		{Status: store.StatusSuspended, AgentSessionID: "sid", TranscriptPath: transcript, Background: []string{"b1"}},
		{Status: store.StatusRunning, AgentSessionID: "sid", TranscriptPath: transcript},
		{Status: store.StatusRunning, Background: []string{"b1"}},
	}
	card := &store.Task{ID: "a", Sessions: map[string]*store.Session{store.MainSession: s}}
	others := &store.Task{ID: "b", Sessions: map[string]*store.Session{"1": skipped[0], "2": skipped[1], "3": skipped[2]}}
	e := bareEngine(t)
	e.state.Tasks = []*store.Task{card, others}

	scans := e.backgroundScans()
	if len(scans) != 1 {
		t.Fatalf("%d scans, want one for the running session with background tasks", len(scans))
	}
	if again := e.backgroundScans(); len(again) != 0 {
		t.Errorf("a session already being scanned got %d more scans", len(again))
	}
	scans[0]()
	if !slices.Equal(s.Background, []string{"b2"}) || s.BackgroundScanPath != transcript || s.BackgroundScan == 0 {
		t.Errorf("after the scan: background %v, scanned %q to %d", s.Background, s.BackgroundScanPath, s.BackgroundScan)
	}
	if again := e.backgroundScans(); len(again) != 1 {
		t.Errorf("the finished scan was not released: %d scans", len(again))
	}
}

func TestTickMarksAnAgentWhoseTmuxSessionIsGoneAsExited(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for name, tc := range map[string]struct {
		quietFor     time.Duration
		launching    bool
		want         store.SessionStatus
		wantActivity store.Activity
	}{
		"gone for a while":     {time.Minute, false, store.StatusExited, store.ActivityIdle},
		"just updated":         {0, false, store.StatusRunning, store.ActivityThinking},
		"still being launched": {time.Minute, true, store.StatusRunning, store.ActivityThinking},
	} {
		t.Run(name, func(t *testing.T) {
			e := bareEngine(t)
			card := &store.Task{ID: "t1", Number: 51, Title: "Vanishing agent " + name, ColumnID: e.board.FirstColumn().ID, Sessions: map[string]*store.Session{
				store.MainSession: {Key: store.MainSession, Status: store.StatusRunning, Activity: store.ActivityThinking, Started: true,
					Tmux: "repo-51-gone", UpdatedAt: time.Now().Add(-tc.quietFor)},
			}}
			e.state.Tasks = []*store.Task{card}
			if tc.launching {
				e.launching.Store("repo-51-gone", true)
			}

			e.Tick()
			e.work.Wait()

			s := card.Sessions[store.MainSession]
			if s.Status != tc.want {
				t.Fatalf("status = %s, want %s", s.Status, tc.want)
			}
			exited := tc.want == store.StatusExited
			if hasLog(card, "Agent exited") != exited || s.Activity != tc.wantActivity {
				t.Errorf("activity %s, log %v", s.Activity, card.Log)
			}
			if got := desktopNotified("#51 " + card.Title + ":"); exited != (len(got) == 1 && strings.HasSuffix(got[0], "agent exited")) {
				t.Errorf("notifications = %q", got)
			}
		})
	}
}

func TestTickSaysHowACrashedAgentDied(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e := bareEngine(t)
	name := "repo-52-crashed"
	if err := tmux.NewAgentSession(name, e.Root, []string{"sh", "-c", "echo worker crashed; sleep 0.3; kill -9 $$"}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tmux.Kill(name) })
	for range 100 {
		if !tmux.Has(name) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	card := &store.Task{ID: "t1", Number: 52, Title: "Crashing agent", ColumnID: e.board.FirstColumn().ID, Sessions: map[string]*store.Session{
		store.MainSession: {Key: store.MainSession, Status: store.StatusRunning, Started: true, Tmux: name, UpdatedAt: time.Now().Add(-time.Minute)},
	}}
	e.state.Tasks = []*store.Task{card}

	e.Tick()
	e.work.Wait()

	if s := card.Sessions[store.MainSession]; s.Status != store.StatusExited {
		t.Errorf("status = %s", s.Status)
	}
	if !hasLog(card, "Agent exited: killed by SIGKILL, last output: worker crashed") {
		t.Errorf("log = %v", card.Log)
	}
	if got := desktopNotified("#52 " + card.Title + ":"); len(got) != 1 || !strings.Contains(got[0], "agent exited: killed by SIGKILL") {
		t.Errorf("notifications = %q", got)
	}
	if tmux.Has(name) || len(tmux.List()) != 0 {
		t.Error("the dead session was not cleaned up")
	}
	if _, ok := tmux.Died(name); ok {
		t.Error("the dead session was not killed")
	}
}

func TestReconcileMarksVanishedAgentsExitedSoTheyCanResume(t *testing.T) {
	e := bareEngine(t)
	running := &store.Session{Status: store.StatusRunning, Activity: store.ActivityThinking, Tmux: "repo-1-gone"}
	suspended := &store.Session{Status: store.StatusSuspended, Tmux: "repo-2-gone"}
	e.state.Tasks = []*store.Task{
		{ID: "a", Sessions: map[string]*store.Session{store.MainSession: running}},
		{ID: "b", Sessions: map[string]*store.Session{store.MainSession: suspended}},
	}
	e.reconcile()
	if running.Status != store.StatusExited || running.Activity != store.ActivityIdle || suspended.Status != store.StatusSuspended {
		t.Errorf("running %s/%s, suspended %s", running.Status, running.Activity, suspended.Status)
	}
}

func TestBlankPaneAfterABackgroundForkHandsTheConversationToPollExternal(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e := bareEngine(t)
	task := &store.Task{ID: "t1", Number: 1}
	e.state.Tasks = []*store.Task{task}
	s := &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true, Forked: true, AgentSessionID: "bg-conversation",
		Tmux: "repo-1-gone", Pending: []store.Pending{{Text: "queued"}}, Background: []string{"toolu_1"}}

	consume(t, e, task, s, `{"hook_event_name":"SessionStart","source":"startup","session_id":"blank"}`)

	if s.Status != store.StatusExternal || !s.Imported || s.Forked || s.Pending != nil || s.Background != nil || s.AgentSessionID != "bg-conversation" {
		t.Errorf("session after the fork moved to the background: %+v", s)
	}
	if !hasLog(task, "Conversation moved to background session") {
		t.Errorf("log = %v", task.Log)
	}
}

func TestClaimTurn(t *testing.T) {
	const move = `{"name":"Go","type":"move_to_column","targetColumn":"Review"}`
	for name, tc := range map[string]struct {
		status     store.SessionStatus
		activity   store.Activity
		noSession  bool
		wantColumn string
		wantTurn   bool
	}{
		"a turn in progress joins the visit":          {store.StatusRunning, store.ActivityThinking, false, "exec", true},
		"a turn that already ended runs its turn end": {store.StatusRunning, store.ActivityIdle, false, "review", false},
		"a suspended agent is left alone":             {store.StatusSuspended, store.ActivityThinking, false, "exec", false},
		"no session":                                  {noSession: true, wantColumn: "exec"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			e, card := turnEndEngine(t, move)
			s := card.Sessions[store.MainSession]
			s.Status, s.Activity = tc.status, tc.activity
			if tc.noSession {
				card.Sessions = nil
			}

			e.claimTurn(card, card.ID)
			e.work.Wait()

			if card.ColumnID != tc.wantColumn || s.TurnInVisit != tc.wantTurn {
				t.Errorf("column %q, turn in visit %v; want %q, %v", card.ColumnID, s.TurnInVisit, tc.wantColumn, tc.wantTurn)
			}
		})
	}
}

func TestQueuedSessionWhoseWorktreeFailsExitsAndTellsYou(t *testing.T) {
	e := bareEngine(t)
	e.isRepo = true
	card := &store.Task{ID: "t1", Number: 3, Title: "Queued", ColumnID: "exec", Sessions: map[string]*store.Session{
		store.MainSession: {Key: store.MainSession, Status: store.StatusRunning, Activity: store.ActivityStarting, Tmux: "repo-3-gone"},
	}}
	e.state.Tasks = []*store.Task{card}
	e.board = &config.Board{Columns: []config.Column{{ID: "exec", Name: "Executing", AutoSpawn: true}}}

	e.launchQueued(card, store.MainSession)

	if s := card.Sessions[store.MainSession]; s.Status != store.StatusExited || s.Activity != store.ActivityIdle || !hasLog(card, "Worktree failed") {
		t.Errorf("session %+v, log %v", s, card.Log)
	}
	if got := notice(e); !strings.HasPrefix(got, "#3: ") {
		t.Errorf("notice = %q", got)
	}
}

func TestReviewPromptNominatesTheCardsOwnPR(t *testing.T) {
	e := bareEngine(t)
	task := &store.Task{ID: "t1", Number: 1, Title: "Review it", ColumnID: e.board.FirstColumn().ID}
	e.state.Tasks = []*store.Task{task}

	consume(t, e, task, &store.Session{Activity: store.ActivityIdle},
		`{"hook_event_name":"UserPromptSubmit","prompt":"/code-review https://github.com/o/r/pull/7"}`)

	if task.PR == nil || task.PR.Number != 7 || !task.PR.Review || !hasLog(task, "Reviewing PR #7") {
		t.Errorf("PR = %+v, log %v", task.PR, task.Log)
	}
}

func TestPRCreatedByTheAgentIsTrackedWhenTheToolFinishes(t *testing.T) {
	e := bareEngine(t)
	task := &store.Task{ID: "t1", Number: 1, Title: "Ship it", ColumnID: "exec"}
	e.state.Tasks = []*store.Task{task}

	consume(t, e, task, &store.Session{Activity: store.ActivityThinking},
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"gh pr create --fill"},"tool_response":{"stdout":"https://github.com/o/r/pull/12\n"}}`)

	if task.PR == nil || task.PR.Number != 12 || task.PR.Review {
		t.Errorf("PR = %+v, log %v", task.PR, task.Log)
	}
}

func TestStopPendingGuards(t *testing.T) {
	for _, c := range []struct {
		name   string
		col    config.Column
		status store.SessionStatus
		key    string
		want   bool
	}{
		{"column without an agent", config.Column{ID: "c"}, store.StatusRunning, store.MainSession, true},
		{"agent column", config.Column{ID: "c", AutoSpawn: true}, store.StatusRunning, store.MainSession, false},
		{"keepAgent column", config.Column{ID: "c", KeepAgent: true}, store.StatusRunning, store.MainSession, false},
		{"already suspended", config.Column{ID: "c"}, store.StatusSuspended, store.MainSession, false},
		{"other session", config.Column{ID: "c"}, store.StatusRunning, "other", false},
	} {
		s := &store.Session{Status: c.status, StopPending: true}
		task := &store.Task{ID: "t1", ColumnID: "c", Sessions: map[string]*store.Session{c.key: s}}
		e := &Engine{Root: t.TempDir(), state: &store.State{Tasks: []*store.Task{task}}, board: &config.Board{Columns: []config.Column{c.col}}}
		if got := e.stopPending("t1", c.key); got != c.want {
			t.Errorf("%s: stopped=%v, want %v", c.name, got, c.want)
		}
		if s.StopPending {
			t.Errorf("%s: the flag should be consumed", c.name)
		}
		if stopped := s.Status == store.StatusSuspended; c.want && !stopped {
			t.Errorf("%s: the session should be suspended, status %s", c.name, s.Status)
		}
	}
}

func TestScheduleToolsFillTheSessionsSchedules(t *testing.T) {
	s := &store.Session{}
	consumeLines(t, s,
		`{"hook_event_name":"PostToolUse","tool_name":"ScheduleWakeup","tool_use_id":"tu1","tool_input":{"delaySeconds":600,"prompt":"check"},"tool_response":{"scheduledFor":4102444800000}}`,
		`{"hook_event_name":"PostToolUse","tool_name":"CronCreate","tool_input":{"cron":"*/3 * * * *","prompt":"poll"},"tool_response":{"id":"c1","recurring":true,"nextFireAt":4102444900000}}`,
	)
	if len(s.Schedules) != 2 || !s.Polling() {
		t.Fatalf("schedules = %+v", s.Schedules)
	}
	s.EventsOffset = 0
	consumeLines(t, s, `{"hook_event_name":"PostToolUse","tool_name":"CronDelete","tool_input":{"id":"c1"},"tool_response":{"id":"c1"}}`)
	if len(s.Schedules) != 1 || s.Schedules[0].ID != "tu1" {
		t.Errorf("schedules after the delete = %+v", s.Schedules)
	}
}

func TestExpireSchedulesClearsStoppedAgents(t *testing.T) {
	e := bareEngine(t)
	now := time.Now()
	sessions := map[string]*store.Session{}
	for _, status := range []store.SessionStatus{store.StatusRunning, store.StatusSuspended, store.StatusExited, store.StatusQueued, store.StatusExternal} {
		s := &store.Session{Status: status}
		s.ApplySchedule(claude.ScheduleCall{Wakeup: true, ID: "w", Prompt: "p", NextAt: now.Add(time.Hour)})
		sessions[string(status)] = s
	}
	e.state.Tasks = []*store.Task{{ID: "t1", Sessions: sessions}}
	e.expireSchedules(now)
	for status, s := range sessions {
		if want := status == string(store.StatusRunning); s.Polling() != want {
			t.Errorf("%s agent polls %v, want %v", status, s.Polling(), want)
		}
	}
}

func TestAScheduledPromptRunsItsSchedule(t *testing.T) {
	s := &store.Session{}
	e, task := bareEngine(t), &store.Task{ID: "t1", Title: "loop"}
	feed := func(lines ...string) {
		s.EventsOffset = 0
		consume(t, e, task, s, lines...)
	}
	soon := time.Now().Add(time.Second).UnixMilli()
	call := func(id string) string {
		return fmt.Sprintf(`{"hook_event_name":"PostToolUse","tool_name":"ScheduleWakeup","tool_use_id":%q,"tool_input":{"delaySeconds":1,"prompt":"check the build"},"tool_response":{"scheduledFor":%d}}`, id, soon)
	}
	cron := fmt.Sprintf(`{"hook_event_name":"PostToolUse","tool_name":"CronCreate","tool_input":{"cron":"*/5 * * * *","prompt":"poll"},"tool_response":{"id":"c1","recurring":true,"nextFireAt":%d}}`, soon)
	feed(call("tu1"), cron)

	feed(`{"hook_event_name":"UserPromptSubmit","prompt":"something typed"}`)
	if len(s.Schedules) != 2 {
		t.Fatalf("an unrelated prompt ran a schedule: %+v", s.Schedules)
	}

	feed(`{"hook_event_name":"UserPromptSubmit","prompt":"check the build"}`, call("tu2"))
	if len(s.Schedules) != 2 {
		t.Fatalf("schedules = %+v, want the cron and one wakeup", s.Schedules)
	}
	for _, sc := range s.Schedules {
		if sc.ID == "tu1" {
			t.Error("the wakeup that ran is still scheduled")
		}
	}

	feed(`{"hook_event_name":"UserPromptSubmit","prompt":"poll"}`)
	for _, sc := range s.Schedules {
		if sc.ID == "c1" && (sc.Runs != 1 || !sc.NextAt.After(time.Now())) {
			t.Errorf("cron after its run = %+v, want 1 run and a later time", sc)
		}
	}
}

func pollingPair(e *Engine) (polling, plain *store.Session) {
	long := time.Now().Add(-time.Hour)
	polling = &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true, UpdatedAt: long, Tmux: "polling"}
	polling.ApplySchedule(claude.ScheduleCall{Wakeup: true, ID: "w", Prompt: "p", NextAt: time.Now().Add(time.Hour)})
	plain = &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true, UpdatedAt: long, Tmux: "plain"}
	e.state.Tasks = []*store.Task{
		{ID: "polling", Sessions: map[string]*store.Session{store.MainSession: polling}},
		{ID: "plain", Sessions: map[string]*store.Session{store.MainSession: plain}},
	}
	return polling, plain
}

func TestPollingAgentsAreNotSuspendedForIdleness(t *testing.T) {
	e := bareEngine(t)
	e.board.IdleTimeoutMinutes = 1
	pollingPair(e)
	if got := len(e.idleTimeouts()); got != 1 {
		t.Errorf("%d agents to suspend, want only the one that doesn't poll", got)
	}
}

func TestPollingAgentsAreNotEvictedForASlot(t *testing.T) {
	e := bareEngine(t)
	polling, _ := pollingPair(e)
	if victim := e.freeSlot(&store.Task{ID: "new"}); victim != "plain" || polling.Status != store.StatusRunning {
		t.Errorf("freeSlot suspended %q, want only the agent that doesn't poll", victim)
	}
}
