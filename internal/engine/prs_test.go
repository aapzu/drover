package engine

import (
	"os"
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
