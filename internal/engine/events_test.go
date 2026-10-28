package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
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
		session   *store.Session
		title     string
		line      string
		activity  store.Activity
		followUps int
		check     func(t *testing.T, task *store.Task, s *store.Session)
	}{
		"startup ends starting": {
			session: &store.Session{Activity: store.ActivityStarting}, line: `{"hook_event_name":"SessionStart","source":"startup"}`,
			activity: store.ActivityIdle, followUps: 1,
		},
		"clear starts a new conversation": {
			session: &store.Session{Activity: store.ActivityThinking, TurnInVisit: true, TurnEndRow: "Row"}, line: `{"hook_event_name":"SessionStart","source":"clear"}`,
			activity: store.ActivityIdle, followUps: 1,
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
			activity: store.ActivityThinking, followUps: 1,
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
			activity: store.ActivityThinking, followUps: 1,
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
			activity: store.ActivityIdle, followUps: 3,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if !s.Started || s.LastReply != "All done." || s.Summary != "" {
					t.Errorf("after the stop: %+v", s)
				}
			},
		},
		"the turn of a visit ends": {
			session: &store.Session{Activity: store.ActivityThinking, TurnInVisit: true}, line: `{"hook_event_name":"Stop"}`,
			activity: store.ActivityIdle, followUps: 3,
			check: func(t *testing.T, _ *store.Task, s *store.Session) {
				if s.TurnInVisit {
					t.Error("the turn still counts as in the visit")
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
			followUps := consume(t, e, task, tc.session, tc.line)
			if tc.session.Activity != tc.activity || len(followUps) != tc.followUps {
				t.Errorf("activity = %q with %d follow-ups, want %q with %d", tc.session.Activity, len(followUps), tc.activity, tc.followUps)
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
