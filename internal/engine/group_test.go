package engine

import (
	"fmt"
	"slices"
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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

const addPRBoard = `{"columns":[
  {"id":"plan","name":"Planning","autoSpawn":true},
  {"id":"exec","name":"Executing","autoSpawn":true},
  {"id":"review","name":"In review"},
  {"id":"reviewing","name":"Reviewing"},
  {"id":"done","name":"Done","role":"done"}],
  %s
  "prFlow":{"opened":"In review"},
  "reviewFlow":{"started":"reviewing"}}`

const (
	prOne   = "https://github.com/o/r/pull/1"
	prTwo   = "https://github.com/o/r/pull/2"
	prThree = "https://github.com/o/r/pull/3"
)

func addPRsTo(t *testing.T, board string, owner *store.Task, review bool, urls ...string) *Engine {
	t.Helper()
	e := boardEngine(t, fmt.Sprintf(addPRBoard, board))
	e.state.NextNumber = 10
	e.state.Tasks = []*store.Task{owner}
	e.with(func() { e.addPRs(owner, urls, review) })
	return e
}

func TestAddPRsGivesTheFirstToTheOwnerAndSplitsTheRest(t *testing.T) {
	owner := &store.Task{ID: "o", Number: 1, Title: "Auth", ColumnID: "exec", Issue: &store.Issue{Key: "DX-1"}}
	e := addPRsTo(t, "", owner, false, prOne, prTwo, prOne)

	if owner.PR == nil || owner.PR.Number != 1 || owner.PR.Review || !owner.PRMovePending || !hasLog(owner, "Opened PR #1") {
		t.Errorf("owner after the first PR: %+v, pending %v, log %v", owner.PR, owner.PRMovePending, owner.Log)
	}
	if len(e.state.Tasks) != 2 {
		t.Fatalf("%d cards, want the owner and one split card (the repeated URL adds nothing)", len(e.state.Tasks))
	}
	split := e.state.Tasks[1]
	if split.GroupID != "o" || split.ColumnID != "review" || split.PR.Number != 2 || split.Number != 10 || split.Title != "Auth · PR #2" ||
		split.Issue == nil || split.Issue.Key != "DX-1" || split.Issue == owner.Issue {
		t.Errorf("split card = %+v, PR %+v", split, split.PR)
	}
	if !slices.Contains(owner.SeenPRs, prTwo) || e.state.NextNumber != 11 {
		t.Errorf("seen %v, next number %d", owner.SeenPRs, e.state.NextNumber)
	}
}

func TestAddPRsWithSplittingOffOnlyLogsTheExtraOnes(t *testing.T) {
	owner := &store.Task{ID: "o", Number: 1, Title: "Auth", ColumnID: "exec"}
	e := addPRsTo(t, `"splitByPR":false,`, owner, false, prOne, prTwo, prThree)

	if len(e.state.Tasks) != 1 || owner.PR.Number != 1 || !slices.Equal(owner.SeenPRs, []string{prTwo, prThree}) || !hasLog(owner, "PR #2, not split off: splitByPR is off") {
		t.Errorf("%d cards, owner PR %+v, seen %v, log %v", len(e.state.Tasks), owner.PR, owner.SeenPRs, owner.Log)
	}
}

func TestAddPRsReviewNominationInTheFirstColumnGoesToTheOwnersCard(t *testing.T) {
	owner := &store.Task{ID: "o", Number: 1, Title: "Auth", ColumnID: "plan"}
	e := addPRsTo(t, "", owner, true, prOne)

	if owner.PR == nil || !owner.PR.Review || !owner.PRMovePending || len(e.state.Tasks) != 1 || !hasLog(owner, "Reviewing PR #1") {
		t.Errorf("owner PR %+v, pending %v, %d cards", owner.PR, owner.PRMovePending, len(e.state.Tasks))
	}
}

func TestAddPRsReviewNominationOnAWorkingCardGetsACardOfItsOwn(t *testing.T) {
	owner := &store.Task{ID: "o", Number: 1, Title: "Auth", ColumnID: "exec"}
	e := addPRsTo(t, "", owner, true, prOne)

	if owner.PR != nil || len(e.state.Tasks) != 2 || e.state.Tasks[1].ColumnID != "reviewing" || !e.state.Tasks[1].PR.Review {
		t.Errorf("owner PR %+v, %d cards, split %+v", owner.PR, len(e.state.Tasks), e.state.Tasks[len(e.state.Tasks)-1])
	}
}

func TestAddPRsLeavesTheOwnerPRlessOnceItHasSplitCards(t *testing.T) {
	owner := &store.Task{ID: "o", Number: 1, Title: "Auth", ColumnID: "exec"}
	sibling := &store.Task{ID: "s", Number: 2, GroupID: "o", ColumnID: "review", PR: &store.PR{URL: prOne, Number: 1}}
	e := boardEngine(t, fmt.Sprintf(addPRBoard, ""))
	e.state.NextNumber = 10
	e.state.Tasks = []*store.Task{owner, sibling}

	e.with(func() { e.addPRs(owner, []string{prTwo}, false) })

	if owner.PR != nil || len(e.state.Tasks) != 3 || e.state.Tasks[2].PR.Number != 2 {
		t.Errorf("owner PR %+v, %d cards; a group's new PR gets a card of its own", owner.PR, len(e.state.Tasks))
	}
}
