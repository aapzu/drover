package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/store"
)

func idleAgent(tmuxName string, idleFor time.Duration) *store.Session {
	return &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Started: true, Tmux: tmuxName, UpdatedAt: time.Now().Add(-idleFor)}
}

func slotEngine(t *testing.T, limit int, tasks ...*store.Task) *Engine {
	t.Helper()
	e := bareEngine(t)
	e.board = groupBoard()
	e.board.MaxConcurrent = limit
	e.state.Tasks = tasks
	return e
}

func TestFreeSlotSuspendsTheLongestIdleAgent(t *testing.T) {
	asker := &store.Task{ID: "asker", Number: 1, ColumnID: "exec"}
	task := func(id string, n int, column string, s *store.Session) *store.Task {
		return &store.Task{ID: id, Number: n, ColumnID: column, Sessions: map[string]*store.Session{store.MainSession: s}}
	}
	thinking := &store.Session{Status: store.StatusRunning, Activity: store.ActivityThinking, Started: true, Tmux: "thinking"}
	for name, tc := range map[string]struct {
		others []*store.Task
		want   string
		victim int
	}{
		"nothing idle":                 {[]*store.Task{task("a", 2, "exec", thinking)}, "", -1},
		"the longest idle one":         {[]*store.Task{task("a", 2, "exec", idleAgent("recent", time.Minute)), task("b", 3, "exec", idleAgent("oldest", time.Hour))}, "oldest", 1},
		"a keepAgent column is spared": {[]*store.Task{task("a", 2, "hold", idleAgent("held", time.Hour)), task("b", 3, "exec", idleAgent("recent", time.Minute))}, "recent", 1},
		"only kept agents are idle":    {[]*store.Task{task("a", 2, "hold", idleAgent("held", time.Hour))}, "", -1},
	} {
		t.Run(name, func(t *testing.T) {
			e := slotEngine(t, 1, append([]*store.Task{asker}, tc.others...)...)
			e.mu.Lock()
			got := e.freeSlot(asker)
			e.mu.Unlock()
			if got != tc.want {
				t.Fatalf("freeSlot = %q, want %q", got, tc.want)
			}
			for i, o := range tc.others {
				s := o.Sessions[store.MainSession]
				if suspended := s.Status == store.StatusSuspended; suspended != (i == tc.victim) {
					t.Errorf("session %q suspended = %v", s.Tmux, suspended)
				}
			}
			if tc.want != "" && !strings.Contains(notice(e), "to free a slot for #1") {
				t.Error("no notice that an agent was suspended")
			}
		})
	}
}

func TestStartQueuedNow(t *testing.T) {
	queued := func() *store.Task {
		return &store.Task{ID: "q", Number: 1, ColumnID: "exec", Sessions: map[string]*store.Session{"side": {Status: store.StatusQueued, Tmux: "q-session"}}}
	}
	busy := &store.Task{ID: "busy", Number: 2, ColumnID: "exec", Sessions: map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityThinking, Started: true}}}
	idler := &store.Task{ID: "idler", Number: 3, ColumnID: "exec", Sessions: map[string]*store.Session{store.MainSession: idleAgent("idler-session", time.Hour)}}

	t.Run("a free slot starts it", func(t *testing.T) {
		card := queued()
		e := slotEngine(t, 2, card, busy)
		e.startQueuedNow(card, "side")
		if s := card.Sessions["side"]; s.Status != store.StatusRunning || s.Activity != store.ActivityStarting {
			t.Errorf("session = %+v, want it started", s)
		}
		if _, launching := e.launching.Load("q-session"); launching {
			t.Error("the launch marker was left behind")
		}
	})

	t.Run("a full board with every agent busy leaves it queued", func(t *testing.T) {
		card := queued()
		e := slotEngine(t, 1, card, busy)
		e.startQueuedNow(card, "side")
		if s := card.Sessions["side"]; s.Status != store.StatusQueued {
			t.Errorf("status = %s, want queued", s.Status)
		}
		if got := notice(e); !strings.Contains(got, "#1 queued: 1 agent") {
			t.Errorf("notice = %q", got)
		}
	})

	t.Run("a full board suspends the idle agent for it", func(t *testing.T) {
		card := queued()
		victim := idleAgent("idler-session", time.Hour)
		idler.Sessions[store.MainSession] = victim
		e := slotEngine(t, 1, card, idler)
		e.startQueuedNow(card, "side")
		if s := card.Sessions["side"]; s.Status != store.StatusRunning || victim.Status != store.StatusSuspended {
			t.Errorf("queued %s, idle agent %s, want the idle one suspended and the queued one running", s.Status, victim.Status)
		}
		if !hasLog(idler, "Agent suspended to free a slot for #1") || !hasLog(card, "Suspended #3 (idle longest)") {
			t.Errorf("logs: %v / %v", idler.Log, card.Log)
		}
	})

	t.Run("a session that is not queued is left alone", func(t *testing.T) {
		card := queued()
		card.Sessions["side"].Status = store.StatusSuspended
		e := slotEngine(t, 2, card)
		e.startQueuedNow(card, "side")
		e.startQueuedNow(card, "missing")
		if s := card.Sessions["side"]; s.Status != store.StatusSuspended {
			t.Errorf("status = %s", s.Status)
		}
	})
}
