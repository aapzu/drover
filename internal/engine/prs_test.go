package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/gh"
	"github.com/aapzu/drover/internal/store"
)

const prFlowBoard = `{"columns":[
  {"id":"backlog","name":"Backlog"},
  {"id":"exec","name":"Executing","autoSpawn":true},
  {"id":"review","name":"In review"},
  {"id":"parked","name":"Parked"},
  {"id":"done","name":"Done","role":"done"}],
  "prFlow":{"opened":"In review","merged":"done"}}`

// prCard is a card with an open PR waiting in the review column.
func prCard(e *Engine) *store.Task {
	card := &store.Task{ID: "a", Number: 1, ColumnID: "review", PR: &store.PR{URL: "https://github.com/o/r/pull/7", Number: 7, State: "OPEN"}}
	e.state.Tasks = []*store.Task{card}
	return card
}

func TestPRStepMovesMergedAndClosedCards(t *testing.T) {
	for name, tc := range map[string]struct {
		change      func(*config.Board)
		state, prev string
		pending     bool
		busy        bool
		wantTo      string
		wantLog     string
	}{
		"a merge moves the card to the merged column": {state: "MERGED", prev: "OPEN", wantTo: "done", wantLog: "PR #7 merged"},
		"a blocked merge move is retried":             {state: "MERGED", prev: "MERGED", pending: true, wantTo: "done"},
		"a merge with nowhere to go leaves the card": {
			change: func(b *config.Board) { b.PRFlow.Merged = "" }, state: "MERGED", prev: "OPEN", pending: true},
		"a merged card back in review moves on":         {state: "MERGED", prev: "MERGED", wantTo: "done"},
		"a merged card back in review waits for agents": {state: "MERGED", prev: "MERGED", busy: true},
		"a close moves the card to done":                {state: "CLOSED", prev: "OPEN", wantTo: "done", wantLog: "closed without merging"},
		"a blocked close move is retried":               {state: "CLOSED", prev: "CLOSED", pending: true, wantTo: "done"},
		"a close of a card outside the opened column": {
			change: func(b *config.Board) { b.PRFlow.Opened = "parked" }, state: "CLOSED", prev: "OPEN"},
		"a close with no done column": {
			change: func(b *config.Board) { b.Columns[len(b.Columns)-1].Role = "" }, state: "CLOSED", prev: "OPEN"},
		"a close with no prFlow": {
			change: func(b *config.Board) { b.PRFlow = nil }, state: "CLOSED", prev: "OPEN"},
		"an already seen close needs no move": {state: "CLOSED", prev: "CLOSED"},
	} {
		t.Run(name, func(t *testing.T) {
			e := boardEngine(t, prFlowBoard)
			if tc.change != nil {
				tc.change(e.board)
			}
			card := prCard(e)
			card.PR.MergeMovePending = tc.pending
			if tc.busy {
				card.Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityThinking}}
			}

			var step prMove
			e.with(func() { step = e.prStep(card, card, tc.prev, gh.PR{State: tc.state}) })

			moves := tc.wantTo != ""
			wantRetry := moves && tc.prev == tc.state
			if step.to.ID != tc.wantTo || step.retry != wantRetry || card.PR.MergeMovePending != moves {
				t.Errorf("move to %q (retry %v), pending %v; want %q (retry %v), pending %v",
					step.to.ID, step.retry, card.PR.MergeMovePending, tc.wantTo, wantRetry, moves)
			}
			if tc.wantLog != "" && !hasLog(card, tc.wantLog) {
				t.Errorf("log = %v, want %q", card.Log, tc.wantLog)
			}
		})
	}
}

func TestMergedOrClosedMoveWaitsOutAnExternalSession(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			e := boardEngine(t, prFlowBoard)
			card := prCard(e)
			card.Sessions = map[string]*store.Session{store.MainSession: {Key: store.MainSession, Status: store.StatusExternal, Imported: true, PID: os.Getpid()}}
			step := func(prev string) prMove {
				var s prMove
				e.with(func() { s = e.prStep(card, card, prev, gh.PR{State: state}) })
				return s
			}
			blockedLogs := func() int { return strings.Count(logText(card), "Moving to Done failed") }

			e.applyPRMove(card, card, step("OPEN"))
			e.applyPRMove(card, card, step(state))
			e.applyPRMove(card, card, step(state))
			if card.ColumnID != "review" || !card.PR.MergeMovePending || blockedLogs() != 1 {
				t.Fatalf("column %q, pending %v, blocked move logged %d times; want it held in review, retried silently, logged once",
					card.ColumnID, card.PR.MergeMovePending, blockedLogs())
			}

			card.Sessions[store.MainSession].Status = store.StatusExited
			e.applyPRMove(card, card, step(state))
			if card.ColumnID != "done" || card.PR.MergeMovePending {
				t.Errorf("column %q, pending %v after the external session ended", card.ColumnID, card.PR.MergeMovePending)
			}
		})
	}
}

func TestPollPRsFollowsOnlyCardsThatCanStillChange(t *testing.T) {
	for name, tc := range map[string]struct {
		column, state string
		pending       bool
		change        func(*config.Board)
		want          bool
	}{
		"an open PR":                        {column: "review", state: "OPEN", want: true},
		"a merged PR whose move is pending": {column: "review", state: "MERGED", pending: true, want: true},
		"a merged PR stranded in review":    {column: "review", state: "MERGED", want: true},
		"a merged PR you moved elsewhere":   {column: "parked", state: "MERGED"},
		"a closed PR whose move is pending": {column: "review", state: "CLOSED", pending: true, want: true},
		"a closed PR you moved elsewhere":   {column: "parked", state: "CLOSED"},
		"a merged PR with nowhere to go":    {column: "review", state: "MERGED", change: func(b *config.Board) { b.PRFlow.Merged = "" }},
		"a card in done":                    {column: "done", state: "OPEN"},
	} {
		t.Run(name, func(t *testing.T) {
			e := boardEngine(t, prFlowBoard)
			if tc.change != nil {
				tc.change(e.board)
			}
			card := prCard(e)
			card.ColumnID, card.PR.State, card.PR.MergeMovePending = tc.column, tc.state, tc.pending

			var followUps []func()
			e.with(func() { followUps = e.pollPRs() })
			if got := len(followUps) > 0; got != tc.want {
				t.Errorf("polled = %v, want %v", got, tc.want)
			}
		})
	}
}

// branchCard is a card in the executing column whose branch has no PR yet.
func branchCard(e *Engine) *store.Task {
	card := &store.Task{ID: "a", Number: 1, ColumnID: "exec", Branch: "me/feature", BaseBranch: "main"}
	e.state.Tasks = []*store.Task{card}
	return card
}

// pathGh puts a gh on PATH that runs script.
func pathGh(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func fakeBranchGh(t *testing.T, list string) {
	t.Helper()
	pathGh(t, "case \"$1 $2\" in\n\"pr list\") echo '"+list+"';;\nesac")
}

func TestAttachBranchPRShowsAPRTheAgentDidNotCreate(t *testing.T) {
	const url = "https://github.com/o/r/pull/9"
	fakeBranchGh(t, `[{"number":9,"title":"Feature","url":"`+url+`","author":{"login":"me"}}]`)
	e := boardEngine(t, prFlowBoard)
	card := branchCard(e)

	if !e.attachBranchPR(card.ID, card.Branch) {
		t.Fatal("the open PR on the card's branch was not attached")
	}
	if card.PR == nil || card.PR.URL != url || card.PR.Number != 9 || card.PR.Review || card.PRMovePending || card.ColumnID != "exec" {
		t.Errorf("PR %+v, move pending %v, column %q; want PR #9 attached and the card left in exec", card.PR, card.PRMovePending, card.ColumnID)
	}
	if !hasLog(card, "Found PR #9 on branch me/feature") {
		t.Errorf("log = %v", card.Log)
	}

	other := &store.Task{ID: "b", Number: 2, ColumnID: "exec", Branch: "me/feature"}
	e.state.Tasks = append(e.state.Tasks, other)
	if e.attachBranchPR(other.ID, other.Branch) || other.PR != nil {
		t.Error("a PR another card already holds was attached again")
	}
}

func TestAttachBranchPRLeavesCardsWithoutAPR(t *testing.T) {
	fakeBranchGh(t, `[]`)
	e := boardEngine(t, prFlowBoard)
	card := branchCard(e)
	if e.attachBranchPR(card.ID, card.Branch) || card.PR != nil {
		t.Errorf("PR %+v attached with none open", card.PR)
	}
}

func TestAttachBranchPRLogsALookupFailureOncePerError(t *testing.T) {
	pathGh(t, "echo 'not logged in' >&2\nexit 1")
	e := boardEngine(t, prFlowBoard)
	card := branchCard(e)

	e.attachBranchPR(card.ID, card.Branch)
	e.attachBranchPR(card.ID, card.Branch)
	if got := strings.Count(logText(card), "Looking for a PR on branch me/feature failed: gh pr list: not logged in"); got != 1 || card.PR != nil {
		t.Errorf("failure logged %d times, PR %+v; want once and no PR", got, card.PR)
	}

	fakeBranchGh(t, `[]`)
	e.attachBranchPR(card.ID, card.Branch)
	if len(e.branchPRErrs) != 0 {
		t.Errorf("a successful lookup left %v remembered", e.branchPRErrs)
	}
}

func TestPollPRsRefreshesTheCardItAttachedAPRTo(t *testing.T) {
	pathGh(t, `case "$1 $2" in
"pr list") echo '[{"number":9,"title":"Feature","url":"https://github.com/o/r/pull/9","author":{"login":"me"}}]';;
"pr view") echo '{"title":"Feature","headRefName":"me/feature","state":"OPEN","author":{"login":"me"}}';;
esac`)
	e := boardEngine(t, prFlowBoard)
	e.isRepo = true
	card := branchCard(e)

	var followUps []func()
	e.with(func() { followUps = e.pollPRs() })
	for _, f := range followUps {
		f()
	}
	if card.PR == nil || card.PR.State != "OPEN" || card.PR.Branch != "me/feature" || card.PR.Error != "" || card.ColumnID != "exec" {
		t.Errorf("PR %+v, column %q; want PR #9 attached, refreshed and left in exec", card.PR, card.ColumnID)
	}
}

func TestPollPRsLooksUpTheBranchOfCardsWithoutAPR(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*store.Task)
		want   bool
	}{
		"a card on a branch":      {want: true},
		"a card with a PR":        {change: func(c *store.Task) { c.PR = &store.PR{Number: 7, State: "MERGED"} }},
		"a card without a branch": {change: func(c *store.Task) { c.Branch = "" }},
		"a card on the base":      {change: func(c *store.Task) { c.Branch = "main" }},
		"a card in done":          {change: func(c *store.Task) { c.ColumnID = "done" }},
		"a quick task":            {change: func(c *store.Task) { c.Quick = true }},
		"a split card":            {change: func(c *store.Task) { c.GroupID = "owner" }},
		"a card mid-turn": {change: func(c *store.Task) {
			c.Sessions = map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityThinking}}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			e := boardEngine(t, prFlowBoard)
			e.isRepo = true
			card := branchCard(e)
			if tc.change != nil {
				tc.change(card)
			}
			var followUps []func()
			e.with(func() { followUps = e.pollPRs() })
			if got := len(followUps) > 0; got != tc.want {
				t.Errorf("polled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMovingAMergedOrClosedCardByHandStopsTheRetry(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			e := boardEngine(t, prFlowBoard)
			card := prCard(e)
			card.PR.State, card.PR.MergeMovePending = state, true

			if err := e.Move(card.ID, "parked", MoveOptions{}); err != nil {
				t.Fatal(err)
			}

			var followUps []func()
			e.with(func() { followUps = e.pollPRs() })
			if card.ColumnID != "parked" || card.PR.MergeMovePending || len(followUps) != 0 {
				t.Errorf("column %q, pending %v, %d polls; the card you moved must be left alone", card.ColumnID, card.PR.MergeMovePending, len(followUps))
			}
		})
	}
}

// releaseRepo is a git repository whose main branch holds one commit, which it
// returns along with a commit that is not on main.
func releaseRepo(t *testing.T) (root, onMain, offMain string) {
	t.Helper()
	root = t.TempDir()
	git := func(args ...string) string { return gitIn(t, root, args...) }
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "on main")
	onMain = git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "side")
	git("commit", "-q", "--allow-empty", "-m", "off main")
	offMain = git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	return root, onMain, offMain
}

func TestCheckReleaseMarksAMergedPRReleasedOnceItsCommitIsOnTheBranch(t *testing.T) {
	const board = `{"columns":[
	  {"id":"review","name":"In review"},
	  {"id":"merged","name":"Merged"},
	  {"id":"released","name":"Released"},
	  {"id":"done","name":"Done","role":"done"}],
	  "prFlow":{"opened":"In review","merged":"Merged","released":"Released"}}`
	root, onMain, offMain := releaseRepo(t)
	for name, tc := range map[string]struct {
		column, commit string
		pending        bool
		wantColumn     string
		wantReleased   bool
	}{
		"in the merged column it moves on":     {column: "merged", commit: onMain, wantColumn: "released", wantReleased: true},
		"elsewhere it is only marked":          {column: "review", commit: onMain, wantColumn: "review", wantReleased: true},
		"a commit not on the branch yet":       {column: "merged", commit: offMain, wantColumn: "merged"},
		"an unknown commit is not released":    {column: "merged", commit: strings.Repeat("0", 40), wantColumn: "merged"},
		"waits for a pending merge move first": {column: "merged", commit: onMain, pending: true, wantColumn: "merged"},
	} {
		t.Run(name, func(t *testing.T) {
			e := boardEngine(t, board)
			e.Root = root
			card := prCard(e)
			card.ColumnID, card.PR.State, card.PR.MergeCommit, card.PR.MergeMovePending = tc.column, "MERGED", tc.commit, tc.pending

			e.checkRelease(card.ID, "main")

			if card.ColumnID != tc.wantColumn || card.PR.Released != tc.wantReleased {
				t.Errorf("column %q, released %v; want %q, %v (log %v)", card.ColumnID, card.PR.Released, tc.wantColumn, tc.wantReleased, card.Log)
			}
		})
	}
}

func TestCheckReleaseReportsAGitFailureOnce(t *testing.T) {
	e := boardEngine(t, prFlowBoard)
	e.Root = t.TempDir()
	card := prCard(e)
	card.PR.State, card.PR.MergeCommit = "MERGED", "abc123"

	e.checkRelease(card.ID, "main")
	e.checkRelease(card.ID, "main")

	failures := strings.Count(logText(card), "Checking PR #7 failed")
	if failures != 1 || card.PR.Error == "" {
		t.Errorf("failure logged %d times, error %q; want it logged once and kept on the PR", failures, card.PR.Error)
	}
}

func TestEveryPRReleasedIsAnnouncedWhenTheLastCardFinishes(t *testing.T) {
	e := boardEngine(t, prFlowBoard)
	owner := &store.Task{ID: "o", Number: 1, ColumnID: "done", PR: &store.PR{Number: 7}}
	pending := &store.Task{ID: "m", Number: 2, GroupID: "o", ColumnID: "review", PR: &store.PR{Number: 8}}
	review := &store.Task{ID: "r", Number: 3, GroupID: "o", ColumnID: "review", PR: &store.PR{Number: 9, Review: true}}
	e.state.Tasks = []*store.Task{owner, pending, review}

	e.notifyAllReleased(owner)
	if got := notice(e); got != "" {
		t.Errorf("announced %q while card #2's PR is still open", got)
	}

	pending.ColumnID = "done"
	e.notifyAllReleased(owner)
	if got := notice(e); got != "#1: every PR is released" {
		t.Errorf("notice = %q; a PR you only review shouldn't hold the announcement back", got)
	}
}
