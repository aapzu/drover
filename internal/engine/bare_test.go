package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/inbox"
	"github.com/aapzu/drover/internal/store"
)

func bareEngine(t *testing.T) *Engine {
	t.Helper()
	isolateTmux(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stopping, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	return &Engine{Root: t.TempDir(), state: &store.State{NextNumber: 1}, board: config.Default(), notices: make(chan string, 8),
		alerted: map[string]time.Time{}, stopping: stopping, stop: stop}
}

// boardEngine is a bareEngine on the board given as drover.json text.
func boardEngine(t *testing.T, board string) *Engine {
	t.Helper()
	e := bareEngine(t)
	if err := os.WriteFile(filepath.Join(e.Root, config.FileName), []byte(board), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := config.Load(e.Root)
	if err != nil {
		t.Fatal(err)
	}
	e.board = loaded
	return e
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

func TestTalkedToCountsAPromptTheBoardHasNotReadYet(t *testing.T) {
	prompt := `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}` + "\n"
	start := `{"hook_event_name":"SessionStart","source":"startup"}` + "\n"
	for name, tc := range map[string]struct {
		started bool
		events  string
		noFile  bool
		asDir   bool
		want    bool
	}{
		"a started conversation":        {started: true, want: true},
		"nothing recorded yet":          {noFile: true, want: false},
		"only the session start":        {events: start, want: false},
		"a prompt not yet read":         {events: start + prompt, want: true},
		"an unreadable log is not a no": {asDir: true, want: true},
	} {
		e := bareEngine(t)
		s := &store.Session{Started: tc.started, Dir: t.TempDir()}
		card := &store.Task{ID: "a", Sessions: map[string]*store.Session{store.MainSession: s}}
		events := filepath.Join(s.Dir, "events.jsonl")
		switch {
		case tc.asDir:
			os.Mkdir(events, 0o755)
		case !tc.noFile:
			os.WriteFile(events, []byte(tc.events), 0o644)
		}
		if got := e.talkedTo(card, store.MainSession); got != tc.want {
			t.Errorf("%s: talkedTo = %v, want %v", name, got, tc.want)
		}
	}
}

func TestDropUnstartedForgetsOnlyTheSessionsNobodyTalkedTo(t *testing.T) {
	e := bareEngine(t)
	silent := &store.Session{Dir: t.TempDir()}
	talked := &store.Session{Dir: t.TempDir(), Started: true}
	external := &store.Session{Dir: t.TempDir(), Status: store.StatusExternal}
	card := &store.Task{ID: "a", Sessions: map[string]*store.Session{"silent": silent, "talked": talked, "external": external}}

	e.dropUnstarted(card)

	if _, ok := card.Sessions["silent"]; ok || card.Sessions["talked"] != talked || card.Sessions["external"] != external || len(card.Sessions) != 2 {
		t.Errorf("sessions left: %v", card.Sessions)
	}
}

func TestStopSessionsLeavesAConversationRunningInYourOwnTerminal(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	os.MkdirAll(filepath.Join(claudeDir, "sessions"), 0o755)
	entry, _ := json.Marshal(claude.Live{PID: os.Getpid(), SessionID: "sid-here", Cwd: "/somewhere", Status: "idle"})
	os.WriteFile(filepath.Join(claudeDir, "sessions", fmt.Sprintf("%d.json", os.Getpid())), entry, 0o644)

	e := bareEngine(t)
	card := &store.Task{ID: "a", Number: 5, Sessions: map[string]*store.Session{
		store.MainSession: {Status: store.StatusExternal, AgentSessionID: "sid-here", PID: os.Getpid()},
	}}
	e.state.Tasks = []*store.Task{card}

	stopped, leftRunning := e.stopSessions(card, "", store.StatusSuspended)

	if stopped != 0 || !leftRunning || !hasLog(card, "still runs in another terminal") || !strings.Contains(notice(e), "#5 still runs in another terminal") {
		t.Errorf("stopped %d, left running %v, log %v", stopped, leftRunning, card.Log)
	}
}

func TestAnImportedSessionWhoseDirectoryIsGoneCannotGetAWorktree(t *testing.T) {
	e := bareEngine(t)
	e.isRepo = true
	gone := filepath.Join(t.TempDir(), "gone")
	card := &store.Task{ID: "a", WorktreePath: gone, Sessions: map[string]*store.Session{store.MainSession: {Imported: true}}}

	_, err := e.ensureWorktree(card, config.Column{}, card.ID)

	if err == nil || !strings.Contains(err.Error(), gone+" no longer exists") {
		t.Errorf("err = %v", err)
	}
}

func TestImportSessionsMirrorsWhatEachConversationIsDoing(t *testing.T) {
	cwd := t.TempDir()
	transcript := filepath.Join(cwd, "sid-running.jsonl")
	os.WriteFile(transcript, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Halfway there."}]}}`+"\n"), 0o644)
	e := bareEngine(t)
	e.board = groupBoard()
	e.state.Tasks = []*store.Task{{ID: "x", Sessions: map[string]*store.Session{store.MainSession: {AgentSessionID: "sid-known"}}}}

	added := e.ImportSessions([]claude.Found{
		{SessionID: "sid-running", Title: "Busy elsewhere", Cwd: cwd, Running: true, Busy: true, PID: 4242, TranscriptPath: transcript},
		{SessionID: "sid-old", Title: "Finished long ago", Cwd: cwd},
		{SessionID: "sid-known", Title: "Already here", Cwd: cwd},
	})

	if added != 2 || len(e.state.Tasks) != 3 {
		t.Fatalf("added %d, %d cards; the conversation already on the board must be skipped", added, len(e.state.Tasks))
	}
	running, old := e.state.Tasks[1], e.state.Tasks[2]
	rs, oldS := running.Sessions[store.MainSession], old.Sessions[store.MainSession]
	if running.ColumnID != "exec" || rs.Status != store.StatusExternal || rs.Activity != store.ActivityThinking || rs.PID != 4242 || rs.LastReply != "Halfway there." || !rs.Imported || running.WorktreePath != cwd {
		t.Errorf("running conversation: column %q, %+v", running.ColumnID, rs)
	}
	if old.ColumnID != "plan" || oldS.Status != store.StatusSuspended || oldS.Activity != store.ActivityIdle || !oldS.Started {
		t.Errorf("finished conversation: column %q, %+v", old.ColumnID, oldS)
	}
}

func TestApplyRequestAnswersWithAnErrorWhateverGoesWrong(t *testing.T) {
	e := bareEngine(t)
	if res := e.applyRequest(inbox.Pending{ID: "x", Err: errors.New("bad json")}); !strings.HasPrefix(res.Error, "unreadable request: bad json") {
		t.Errorf("a request that could not be read: %+v", res)
	}
	broken := &Engine{}
	if res := broken.applyRequest(inbox.Pending{ID: "y", Request: inbox.Request{Op: inbox.OpEdit, Number: 1}}); !strings.HasPrefix(res.Error, "internal error") {
		t.Errorf("a request that panics: %+v", res)
	}
}

func TestAnswerTellsYouWhatTheCLIAskedFor(t *testing.T) {
	e := bareEngine(t)
	refused := inbox.Request{Op: inbox.OpMove, Number: 9, Column: "done"}

	e.answer(inbox.Pending{ID: "1", Request: refused})
	if got := notice(e); got != "drover move: no card #9" {
		t.Fatalf("notice after a refused move = %q", got)
	}
	if got := notice(e); !strings.HasPrefix(got, "drover move: could not answer the CLI:") {
		t.Errorf("notice = %q, want the failed answer reported", got)
	}

	os.MkdirAll(filepath.Join(store.Dir(e.Root), "inbox"), 0o755)
	e.answer(inbox.Pending{ID: "2", Request: inbox.Request{Op: inbox.OpAdd, Title: ptr("Fix login")}})
	if got := notice(e); got != "Added #1 Fix login" {
		t.Errorf("notice after an add = %q", got)
	}
	e.answer(inbox.Pending{ID: "3", Request: refused})
	res, ok, err := inbox.Wait(e.Root, "3", time.Second, nil)
	if err != nil || !ok || res.Error != "no card #9" {
		t.Errorf("the CLI's answer = %+v, %v, %v", res, ok, err)
	}
}
