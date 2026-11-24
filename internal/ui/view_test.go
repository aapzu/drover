package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

func TestStatusDescribesWhatTheAgentIsDoing(t *testing.T) {
	session := func(status store.SessionStatus, activity store.Activity) *store.Session {
		return &store.Session{Status: status, Activity: activity, UpdatedAt: time.Now()}
	}
	tests := []struct {
		name    string
		session *store.Session
		want    string
		wantNot string
	}{
		{"queued", session(store.StatusQueued, ""), "◷ queued (2/3 slots busy)", ""},
		{"suspended", session(store.StatusSuspended, ""), "⏸ suspended", ""},
		{"exited", session(store.StatusExited, ""), "■ exited", ""},
		{"working elsewhere", session(store.StatusExternal, store.ActivityThinking), "working elsewhere", ""},
		{"idle elsewhere", session(store.StatusExternal, store.ActivityIdle), "↗ idle elsewhere", ""},
		{"needs you", session(store.StatusRunning, store.ActivityPermission), "◆ needs you", ""},
		{"starting", session(store.StatusRunning, store.ActivityStarting), "starting", "waiting to start"},
		{"never reported starting", &store.Session{Status: store.StatusRunning, Activity: store.ActivityStarting, UpdatedAt: time.Now().Add(-time.Minute)}, "◆ waiting to start", "starting"},
		{"idle", session(store.StatusRunning, store.ActivityIdle), "● idle", "background"},
		{"idle with background tasks", &store.Session{Status: store.StatusRunning, Activity: store.ActivityIdle, Background: []string{"b"}}, "◌ background tasks", "● idle"},
		{"working", session(store.StatusRunning, store.ActivityThinking), "working", ""},
		{"messages waiting", &store.Session{Status: store.StatusRunning, Activity: store.ActivityThinking, Pending: []store.Pending{{Text: "a"}, {Text: "b"}}}, "working +2 queued", ""},
		{"settings change next turn", &store.Session{Status: store.StatusRunning, Activity: store.ActivityThinking, RespawnPending: true}, "→ acceptEdits next turn", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(120, 40, 1, 1)
			m.snap.Running, m.snap.MaxConcurrent = 2, 3
			m.snap.Board.Columns[0].AutoSpawn = true
			m.snap.Board.Columns[0].PermissionMode = "acceptEdits"
			card := m.snap.Tasks[0]
			card.Sessions = map[string]*store.Session{store.MainSession: tt.session}

			got := ansi.Strip(m.status(card))
			if !strings.Contains(got, tt.want) || (tt.wantNot != "" && strings.Contains(got, tt.wantNot)) {
				t.Errorf("status = %q, want %q (and not %q)", got, tt.want, tt.wantNot)
			}
		})
	}
}

func TestHeaderCountsAnAgentThatNeverStartedAsNeedingYou(t *testing.T) {
	m := testModel(120, 40, 1, 2)
	m.snap.Tasks[0].Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityStarting, UpdatedAt: time.Now().Add(-time.Minute)}}
	m.snap.Tasks[1].Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityStarting, UpdatedAt: time.Now()}}
	m.eng = realModel(t).eng

	got := ansi.Strip(m.renderHeader())
	if !strings.Contains(got, "1 need you") || !strings.Contains(got, "1 working") {
		t.Errorf("header = %q, want the stuck agent under need you and the fresh one working", got)
	}
}

func TestColumnSummaryDescribesTheColumnsAgent(t *testing.T) {
	tests := []struct {
		name string
		col  config.Column
		want string
	}{
		{"done", config.Column{Role: "done"}, "finished · archive removes worktree"},
		{"keeps the agent", config.Column{KeepAgent: true}, "agent kept running"},
		{"no agent", config.Column{}, "agent paused"},
		{"plain agent", config.Column{AutoSpawn: true}, "agent"},
		{"the default model is not worth a mention", config.Column{AutoSpawn: true, ModelOverride: config.DefaultAgentModel}, "agent"},
		{"settings and automations", config.Column{AutoSpawn: true, PermissionMode: "plan", ModelOverride: "opus", EffortOverride: "high",
			Automations: config.Automations{OnEnter: []config.Automation{{}}, OnTurnEnd: []config.Automation{{}}}}, "plan · opus · high · ⚡2"},
		{"automations alone", config.Column{AutoSpawn: true, Automations: config.Automations{OnEnter: []config.Automation{{}}}}, "⚡1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := columnSummary(tt.col); got != tt.want {
				t.Errorf("columnSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetailListsEverythingAboutTheCard(t *testing.T) {
	m := testModel(120, 40, 2, 0)
	owner := &store.Task{ID: "owner", Number: 1, Title: "Refactor", ColumnID: "c1", Prompt: "tidy it up",
		Branch: "refactor-1", BaseBranch: "main", WorktreePath: "/wt/refactor", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Issue: &store.Issue{Key: "DX-1", State: "Todo", URL: "https://linear.app/x/issue/DX-1"},
		PR:    &store.PR{Number: 7, State: "OPEN", Branch: "refactor-1", URL: "https://github.com/x/y/pull/7", Review: true, Author: "them"},
		Sessions: map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityIdle, AgentSessionID: "abc-123", Tmux: "drover-1",
			LastReply: "All done here."}},
		Log: []store.LogEntry{{At: time.Now(), Text: "first event"}, {At: time.Now(), Text: "second event"}}}
	split := &store.Task{ID: "split", Number: 2, Title: "PR two", ColumnID: "c0", GroupID: "owner"}
	m.snap.Tasks = []*store.Task{owner, split}

	got := ansi.Strip(m.renderDetail(owner))
	for _, want := range []string{"#1 Refactor", "Col 1", "DX-1  Todo", "#7  OPEN  refactor-1", "Reviewing  @them", "refactor-1", "main", "/wt/refactor",
		"2026-01-02 03:04:05", "tidy it up", "Shares its agent with", "#2 PR two", "abc-123", "tmux:drover-1", "All done here."} {
		if !strings.Contains(got, want) {
			t.Errorf("detail misses %q:\n%s", want, got)
		}
	}
	if newest, oldest := strings.Index(got, "second event"), strings.Index(got, "first event"); newest < 0 || oldest < 0 || newest > oldest {
		t.Errorf("the activity log should list the newest event first:\n%s", got)
	}

	if got := ansi.Strip(m.renderDetail(&store.Task{ID: "new", Number: 3, ColumnID: "c0"})); !strings.Contains(got, "#3 New session") {
		t.Errorf("a card with no title should read New session:\n%s", got)
	}
}

func TestPreviewShowsWhatTheSelectedAgentIsDoing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *Model, card *store.Task)
		want  []string
		not   []string
	}{
		{"no card", func(m *Model, _ *store.Task) { m.snap.Tasks = nil }, []string{"no task selected"}, nil},
		{"an agent in another terminal", func(_ *Model, card *store.Task) {
			card.Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusExternal, PID: 4242, LastReply: "Nearly there."}}
		}, []string{"pid 4242", "Nearly there."}, nil},
		{"no agent but a last reply", func(_ *Model, card *store.Task) {
			card.Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusSuspended, LastReply: "Stopped here."}}
		}, []string{"No running agent", "Last reply:", "Stopped here."}, nil},
		{"a running agent", func(m *Model, card *store.Task) {
			m.snap.Live = map[string]string{card.ID: "sess"}
			m.previewFor, m.previewTxt = card.ID, "agent output"
		}, []string{"agent output", "tab to type here"}, []string{"lines up"}},
		{"scrolled back", func(m *Model, card *store.Task) {
			m.snap.Live = map[string]string{card.ID: "sess"}
			m.previewFor, m.previewTxt, m.previewScroll = card.ID, "agent output", 5
		}, []string{"agent output", "↑ 5 lines up"}, []string{"tab to type here"}},
		{"typing into it", func(m *Model, card *store.Task) {
			m.snap.Live = map[string]string{card.ID: "sess"}
			m.previewFor, m.previewTxt, m.mode = card.ID, "agent output", modeLive
		}, []string{"LIVE", "ctrl+q back to board"}, []string{"tab to type here"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(120, 40, 1, 1)
			tt.setup(&m, m.snap.Tasks[0])
			got := ansi.Strip(m.renderPreview(12))
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("preview misses %q:\n%s", want, got)
				}
			}
			for _, not := range tt.not {
				if strings.Contains(got, not) {
					t.Errorf("preview shows %q:\n%s", not, got)
				}
			}
		})
	}
}

func TestColumnWithMoreCardsThanRoomFollowsTheSelection(t *testing.T) {
	const cards = 30
	m := testModel(120, 40, 1, cards)
	box := m.layout().cols[0]
	if box.slots <= 2 || box.slots >= cards {
		t.Fatalf("%d slots, the test needs a column that can't show %d cards", box.slots, cards)
	}
	top := ansi.Strip(m.renderBoard(m.layout()))
	if !strings.Contains(top, "#1 ") || !strings.Contains(top, fmt.Sprintf("+%d more", cards-box.slots)) {
		t.Fatalf("the unscrolled column should show the first card and how many are below:\n%s", top)
	}

	sel := box.slots + 3
	m.selected["c0"] = m.snap.Tasks[sel].ID
	l := m.layout()
	box = l.cols[0]
	view := ansi.Strip(m.renderBoard(l))
	if !strings.Contains(view, fmt.Sprintf("#%d ", sel+1)) || strings.Contains(view, "#1 ") {
		t.Errorf("selecting card %d should scroll it into view and the first one out:\n%s", sel+1, view)
	}
	if !strings.Contains(view, fmt.Sprintf("+%d more", cards-box.first-box.slots)) {
		t.Errorf("the column should count the cards below the window:\n%s", view)
	}
	if h := l.hitTest(m, 3, firstCardY(m)); h.kind != hitCard || h.task.ID != m.snap.Tasks[box.first].ID {
		t.Errorf("a click on the top row hits %+v, want the first card in view, %s", h, m.snap.Tasks[box.first].ID)
	}
}
