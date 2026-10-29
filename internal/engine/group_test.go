package engine

import (
	"testing"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

func groupBoard() *config.Board {
	return &config.Board{Columns: []config.Column{
		{ID: "plan", Name: "Planning", AutoSpawn: true, PermissionMode: "plan"},
		{ID: "exec", Name: "Executing", AutoSpawn: true},
		{ID: "hold", Name: "Hold", AutoSpawn: true, KeepAgent: true},
		{ID: "done", Name: "Done", Role: "done"},
	}}
}

func TestSessionColumnFollowsTheDrivingCard(t *testing.T) {
	owner := &store.Task{ID: "o", ColumnID: "done", ActiveCard: "s"}
	split := &store.Task{ID: "s", GroupID: "o", ColumnID: "hold"}
	e := &Engine{board: groupBoard(), state: &store.State{Tasks: []*store.Task{owner, split}}}

	if col := e.sessionColumn(owner); col.ID != "hold" || !e.keepsAgent(owner) {
		t.Errorf("session column = %q, keeps agent = %v, want the split card's keepAgent column", col.ID, e.keepsAgent(owner))
	}
	owner.ActiveCard = ""
	if col := e.sessionColumn(owner); col.ID != "done" || e.keepsAgent(owner) {
		t.Errorf("without an active card the session follows the owner: %q, keeps agent = %v", col.ID, e.keepsAgent(owner))
	}
	owner.ColumnID = "exec"
	if e.keepsAgent(owner) {
		t.Error("an ordinary agent column keeps no agent")
	}
}

func TestImportColumn(t *testing.T) {
	withWork := groupBoard()
	noWork := &config.Board{Columns: []config.Column{{ID: "inbox", Name: "Inbox"}, {ID: "plan", Name: "Planning", AutoSpawn: true, PermissionMode: "plan"}}}
	for name, tc := range map[string]struct {
		board   *config.Board
		running bool
		want    string
	}{
		"running goes to the work column":  {withWork, true, "exec"},
		"stopped goes to the first column": {withWork, false, "plan"},
		"running without a work column":    {noWork, true, "inbox"},
	} {
		if got := importColumn(tc.board, tc.running); got.ID != tc.want {
			t.Errorf("%s: importColumn = %q, want %q", name, got.ID, tc.want)
		}
	}
}

func TestHandOverMovesTheGroupToTheHeir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := &store.Task{ID: "old", Number: 1, WorktreePath: "/wt/1", Branch: "feat", BaseBranch: "main", ActiveCard: "old", Visit: 3, SeenPRs: []string{"x"},
		Sessions: map[string]*store.Session{store.MainSession: {Status: store.StatusSuspended}}}
	heir := &store.Task{ID: "heir", Number: 2, GroupID: "old"}
	third := &store.Task{ID: "third", Number: 3, GroupID: "old"}
	e := &Engine{Root: root, state: &store.State{Tasks: []*store.Task{old, heir, third}}, board: groupBoard()}

	e.handOver(old, heir)

	if heir.GroupID != "" || third.GroupID != "heir" {
		t.Errorf("group ids: heir %q, third %q, want the heir to own the group", heir.GroupID, third.GroupID)
	}
	if heir.WorktreePath != "/wt/1" || heir.Branch != "feat" || heir.BaseBranch != "main" || heir.Visit != 3 || len(heir.SeenPRs) != 1 || heir.ActiveCard != "heir" {
		t.Errorf("heir did not take over: %+v", heir)
	}
	if old.Sessions != nil || heir.Sessions[store.MainSession] == nil || heir.Sessions[store.MainSession].Dir == "" {
		t.Errorf("sessions moved wrongly: old %v, heir %v", old.Sessions, heir.Sessions)
	}
	if len(heir.Log) == 0 {
		t.Error("the heir logged nothing about the takeover")
	}
}
