package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

// consumeLines writes hook event lines for a session and applies them.
func consumeLines(t *testing.T, s *store.Session, lines ...string) {
	t.Helper()
	s.Dir = t.TempDir()
	path := filepath.Join(s.Dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if events, _, _ := claude.ReadEvents(path, 0); len(events) != len(lines) {
		t.Fatalf("parsed %d of %d event lines", len(events), len(lines))
	}
	task := &store.Task{ID: "t1", Sessions: map[string]*store.Session{store.MainSession: s}}
	(&Engine{}).consumeEvents(task, store.MainSession, s)
}

func TestStopClearsBackgroundOnlyWhenNoneRunning(t *testing.T) {
	for _, c := range []struct {
		name string
		stop string
		want []string
	}{
		{"no tasks listed", `{"hook_event_name":"Stop","background_tasks":[]}`, nil},
		{"agent still running", `{"hook_event_name":"Stop","background_tasks":[{"id":"a1","status":"running"}]}`, []string{"toolu_1"}},
	} {
		s := &store.Session{Background: []string{"toolu_1"}}
		consumeLines(t, s, c.stop)
		if !slices.Equal(s.Background, c.want) {
			t.Errorf("%s: background %v, want %v", c.name, s.Background, c.want)
		}
	}
}

func TestRespawnWaitsForBackgroundAgents(t *testing.T) {
	s := &store.Session{Status: store.StatusRunning, RespawnPending: true, Relocate: "/wt", Background: []string{"toolu_1"}}
	task := &store.Task{ID: "t1", Sessions: map[string]*store.Session{store.MainSession: s}}
	e := &Engine{state: &store.State{Tasks: []*store.Task{task}}, board: &config.Board{}}
	e.respawnPending("t1", store.MainSession)
	if !s.RespawnPending {
		t.Error("respawn ran while a background agent was still running")
	}
}

func TestEventsFollowTheMovedTranscript(t *testing.T) {
	s := &store.Session{TranscriptPath: "/old/a.jsonl"}
	consumeLines(t, s, `{"hook_event_name":"PostToolUse","tool_name":"Bash","transcript_path":"/new/a.jsonl"}`)
	if s.TranscriptPath != "/new/a.jsonl" {
		t.Errorf("transcript %q, want /new/a.jsonl", s.TranscriptPath)
	}
}
