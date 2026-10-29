package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

func bareEngine(t *testing.T) *Engine {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return &Engine{Root: t.TempDir(), state: &store.State{NextNumber: 1}, board: config.Default(), notices: make(chan string, 8)}
}

func notice(e *Engine) string {
	select {
	case n := <-e.notices:
		return n
	default:
		return ""
	}
}

func TestReloadBoardKeepsTheOldBoardOnAnInvalidFile(t *testing.T) {
	e := bareEngine(t)
	file := filepath.Join(e.Root, config.FileName)
	os.WriteFile(file, []byte(`{"columns":[`), 0o644)

	e.ReloadBoard()
	if got := notice(e); !strings.HasPrefix(got, "board config:") || !strings.Contains(got, "drover.json") {
		t.Errorf("notice = %q, want the parse error", got)
	}
	if got := e.Board().FirstColumn().ID; got != "planning" || e.boardFound {
		t.Errorf("an invalid file replaced the board: first column %q, found %v", got, e.boardFound)
	}

	e.ReloadBoard()
	if got := notice(e); got != "" {
		t.Errorf("the same broken file reported again: %q", got)
	}

	os.WriteFile(file, []byte(`{"columns":[{"id":"inbox","name":"Inbox"},{"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e.ReloadBoard()
	if got := e.Board().FirstColumn().ID; got != "inbox" || !e.boardFound {
		t.Errorf("fixed file not loaded: first column %q, found %v", got, e.boardFound)
	}
	if got := notice(e); got != "" {
		t.Errorf("ReloadBoard announced a reload: %q", got)
	}
}

func TestReloadBoardAnnouncesWatchedChanges(t *testing.T) {
	e := bareEngine(t)
	os.WriteFile(filepath.Join(e.Root, config.FileName), []byte(`{"columns":[{"id":"inbox","name":"Inbox"},{"id":"done","name":"Done","role":"done"}]}`), 0o644)
	e.reloadBoard()
	if got := notice(e); got != "Reloaded drover.json" {
		t.Errorf("notice = %q", got)
	}
	e.reloadBoard()
	if got := notice(e); got != "" {
		t.Errorf("an unchanged file announced a reload: %q", got)
	}
}

func TestWriteDefaultBoard(t *testing.T) {
	e := bareEngine(t)
	if err := e.WriteDefaultBoard(); err != nil {
		t.Fatal(err)
	}
	board, found, err := config.Load(e.Root)
	if err != nil || !found || board.FirstColumn().ID != "planning" || !e.boardFound {
		t.Fatalf("after WriteDefaultBoard: found=%v, err=%v, first column %q, engine found=%v", found, err, board.FirstColumn().ID, e.boardFound)
	}

	custom := []byte(`{"columns":[{"id":"inbox","name":"Inbox"},{"id":"done","name":"Done","role":"done"}]}`)
	file := filepath.Join(e.Root, config.FileName)
	os.WriteFile(file, custom, 0o644)
	if err := e.WriteDefaultBoard(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != string(custom) {
		t.Errorf("WriteDefaultBoard overwrote an existing board: %s", got)
	}
}

func TestDropReviewNomination(t *testing.T) {
	const url = "https://github.com/o/r/pull/7"
	t.Run("the owner's own card forgets the PR", func(t *testing.T) {
		e := bareEngine(t)
		card := &store.Task{ID: "a", Number: 1, PR: &store.PR{URL: url, Number: 7, Review: true}, PRMovePending: true}
		e.state.Tasks = []*store.Task{card}
		e.dropReviewNomination(card, card)
		if card.PR != nil || card.PRMovePending || !hasLog(card, "PR #7 is yours, not tracking it as a review") || len(e.state.Tasks) != 1 {
			t.Errorf("card after the drop: %+v, log %v", card.PR, card.Log)
		}
	})
	t.Run("a split card is removed", func(t *testing.T) {
		e := bareEngine(t)
		owner := &store.Task{ID: "o", Number: 1, SeenPRs: []string{url, "https://github.com/o/r/pull/8"}}
		split := &store.Task{ID: "s", Number: 2, GroupID: "o", PR: &store.PR{URL: url, Number: 7, Review: true}}
		e.state.Tasks = []*store.Task{owner, split}
		e.dropReviewNomination(split, owner)
		if len(e.state.Tasks) != 1 || e.state.Tasks[0] != owner || len(owner.SeenPRs) != 1 || !hasLog(owner, "(card #2 removed)") {
			t.Errorf("tasks %d, seen %v, log %v", len(e.state.Tasks), owner.SeenPRs, owner.Log)
		}
	})
}

func TestClaimTurn(t *testing.T) {
	for name, tc := range map[string]struct {
		session *store.Session
		claimed bool
	}{
		"a turn in progress joins the visit": {&store.Session{Status: store.StatusRunning, Activity: store.ActivityThinking}, true},
		"a suspended agent is left alone":    {&store.Session{Status: store.StatusSuspended, Activity: store.ActivityThinking}, false},
		"no session":                         {nil, false},
	} {
		e := bareEngine(t)
		card := &store.Task{ID: "a", Sessions: map[string]*store.Session{}}
		if tc.session != nil {
			card.Sessions[store.MainSession] = tc.session
		}
		e.state.Tasks = []*store.Task{card}
		e.claimTurn(card, "a")
		if got := tc.session != nil && tc.session.TurnInVisit; got != tc.claimed {
			t.Errorf("%s: TurnInVisit = %v, want %v", name, got, tc.claimed)
		}
	}
}

func TestMoveConversation(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e := bareEngine(t)
	worktree := t.TempDir()
	card := &store.Task{ID: "a", Number: 1}
	e.state.Tasks = []*store.Task{card}

	e.moveConversation(card, claude.TranscriptPath("missing", e.Root), worktree)
	if !hasLog(card, "No conversation found at") {
		t.Errorf("log = %v, want a note that nothing was found", card.Log)
	}

	from := claude.TranscriptPath("sid", e.Root)
	os.MkdirAll(filepath.Dir(from), 0o755)
	os.WriteFile(from, []byte("{}\n"), 0o644)
	e.moveConversation(card, from, worktree)
	if _, err := os.Stat(claude.TranscriptPath("sid", worktree)); err != nil || exists(from) || !hasLog(card, "Moved the conversation into the worktree") {
		t.Errorf("transcript not moved: %v, old still there %v, log %v", err, exists(from), card.Log)
	}
}
