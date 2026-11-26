package engine

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/inbox"
	"github.com/aapzu/drover/internal/store"
)

func quickEngine(t *testing.T, extraConfig string) (*Engine, string) {
	t.Helper()
	root, log := setupProject(t)
	writeBoard(root, []byte(`{`+extraConfig+`"columns":[
	  {"id":"exec","name":"Executing","autoSpawn":true,
	   "automations":{"onTurnEnd":[{"type":"move_to_column","targetColumn":"Done"}]}},
	  {"id":"done","name":"Done","role":"done"}],
	  "prFlow":{"opened":"done"}}`))
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	e.PRPollInterval = 200 * time.Millisecond
	runEngine(t, e)
	return e, log
}

func TestQuickTaskRunsInAWorktreeAndIgnoresTheLifecycle(t *testing.T) {
	e, log := quickEngine(t, "")

	task, err := e.NewQuick()
	if err != nil {
		t.Fatal(err)
	}
	if !task.Quick || task.ColumnID != "" {
		t.Fatalf("quick = %v, column = %q", task.Quick, task.ColumnID)
	}
	if err := e.Send(task.ID, "ONEPR make the change"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	pollRounds(t, e, 2)
	got := e.Snapshot().Tasks[0]

	if got.PR != nil || got.PRMovePending || got.ColumnID != "" {
		t.Errorf("a PR, a move or a column reached the quick task: pr=%v pending=%v column=%q", got.PR, got.PRMovePending, got.ColumnID)
	}
	if got.WorktreePath == "" || got.Branch == "" {
		t.Errorf("worktree = %q, branch = %q; quick tasks run in a worktree by default", got.WorktreePath, got.Branch)
	}
	if !strings.Contains(readFile(log), "Don't open a pull request") {
		t.Error("the agent wasn't told it runs a quick task")
	}
	if strings.Contains(readFile(log), "kanban board") {
		t.Error("a quick task got the board prompt")
	}
	if err := e.Move(task.ID, "exec", MoveOptions{}); err == nil {
		t.Error("a quick task moved to a column")
	}
	if err := e.ArchiveTask(task.ID, true, false); err == nil {
		t.Error("a quick task was archived")
	}

	path := got.WorktreePath
	if err := e.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("closing the quick task left its worktree at %s", path)
	}
	if n := len(e.Snapshot().Tasks); n != 0 {
		t.Errorf("%d tasks left after closing the quick task", n)
	}
}

func TestQuickTaskRunsInTheMainCheckoutWhenAsked(t *testing.T) {
	e, _ := quickEngine(t, `"quickWorkdir":"root",`)

	task, err := e.NewQuick()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Send(task.ID, "TWEAK the footer"); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, e, "the turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	if got.WorktreePath != "" || got.Sessions[store.MainSession].Cwd != e.Root {
		t.Errorf("worktree = %q, cwd = %q; want the main checkout %s", got.WorktreePath, got.Sessions[store.MainSession].Cwd, e.Root)
	}
}

func TestQuickTaskAlwaysResumesAsAQuickTask(t *testing.T) {
	e, _ := quickEngine(t, "")

	task, err := e.NewQuick()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Send(task.ID, "TWEAK the footer"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	e.Suspend(task.ID)
	if err := e.Start(task.ID); err != nil {
		t.Fatal(err)
	}
	got := waitFor(t, e, "the agent to resume", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Status == store.StatusRunning
	})
	if got.ColumnID != "" || !got.Quick {
		t.Errorf("resuming changed the quick task: column %q, quick %v", got.ColumnID, got.Quick)
	}
}

func TestSilentQuickTaskIsDiscarded(t *testing.T) {
	e, _ := quickEngine(t, "")

	task, err := e.NewQuick()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DiscardSilent(task.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Snapshot().Tasks); n != 0 {
		t.Errorf("%d tasks left after leaving a quick task without a message", n)
	}
}

func TestQuickTaskIgnoresPromptsThatMoveOrAttachCards(t *testing.T) {
	e, _ := quickEngine(t, "")

	task, err := e.NewQuick()
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"open a PR", "REVIEWME"} {
		before := e.Snapshot().Tasks[0].Sessions[store.MainSession].UpdatedAt
		if err := e.Send(task.ID, prompt); err != nil {
			t.Fatal(err)
		}
		waitFor(t, e, "the turn for "+prompt+" to end", func(t *store.Task) bool {
			s := t.Sessions[store.MainSession]
			return s != nil && s.Started && s.Activity == store.ActivityIdle && s.UpdatedAt.After(before)
		})
	}
	pollRounds(t, e, 2)
	got := e.Snapshot().Tasks[0]
	if got.PR != nil || got.PRMovePending || got.ColumnID != "" {
		t.Errorf("a prompt reached the quick task's lifecycle: pr=%v pending=%v column=%q", got.PR, got.PRMovePending, got.ColumnID)
	}
}

func TestQuickTaskWithAMessageIsKeptWhenYouLeaveIt(t *testing.T) {
	e, _ := quickEngine(t, "")

	task, err := e.NewQuick()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Send(task.ID, "TWEAK the footer"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the turn to end", func(t *store.Task) bool {
		s := t.Sessions[store.MainSession]
		return s != nil && s.Started && s.Activity == store.ActivityIdle
	})
	if err := e.DiscardSilent(task.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Snapshot().Tasks); n != 1 {
		t.Errorf("%d tasks after leaving a quick task that was talked to, want it kept", n)
	}
}

func TestQuickTaskCannotBeEditedFromTheCLI(t *testing.T) {
	e, _ := quickEngine(t, "")

	task, err := e.NewQuick()
	if err != nil {
		t.Fatal(err)
	}
	title := "renamed"
	if _, err := e.inboxEdit(inbox.Request{Op: inbox.OpEdit, Number: task.Number, Title: &title}); err == nil {
		t.Error("the CLI edited a quick task")
	}
}
