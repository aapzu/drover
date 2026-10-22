package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/inbox"
	"github.com/aapzu/drover/internal/linear"
	"github.com/aapzu/drover/internal/store"
)

func ptr(s string) *string { return &s }

// ask queues req the way the CLI does and waits for the open board's answer.
func ask(t *testing.T, root string, req inbox.Request) inbox.Result {
	t.Helper()
	id, err := inbox.Send(root, req)
	if err != nil {
		t.Fatal(err)
	}
	res, ok, err := inbox.Wait(root, id, 20*time.Second, nil)
	if err != nil || !ok {
		t.Fatalf("no answer to %+v: %v", req, err)
	}
	return res
}

// running starts an engine on a fresh project and returns it with its root.
func running(t *testing.T) (*Engine, string) {
	t.Helper()
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
	return e, root
}

func cardNumbered(e *Engine, n int) *store.Task {
	for _, c := range e.Snapshot().Tasks {
		if c.Number == n {
			return c
		}
	}
	return nil
}

func TestCLIRequestsChangeTheOpenBoard(t *testing.T) {
	e, root := running(t)

	res := ask(t, root, inbox.Request{Op: inbox.OpAdd, Title: ptr("Fix login"), Prompt: ptr("It is broken")})
	if res.Error != "" || res.Number != 1 || res.Column != "To Do" {
		t.Fatalf("add = %+v", res)
	}
	if c := cardNumbered(e, 1); c == nil || c.Title != "Fix login" || c.Prompt != "It is broken" || c.ColumnID != "todo" {
		t.Fatalf("card after add = %+v", c)
	}

	if res := ask(t, root, inbox.Request{Op: inbox.OpEdit, Number: 1, Title: ptr("Fix signup")}); res.Error != "" {
		t.Fatalf("edit = %+v", res)
	}
	if c := cardNumbered(e, 1); c.Title != "Fix signup" || c.Prompt != "It is broken" {
		t.Errorf("edit changed the wrong fields: %+v", c)
	}

	if res := ask(t, root, inbox.Request{Op: inbox.OpMove, Number: 1, Column: "executing"}); res.Error != "" || res.Column != "Executing" {
		t.Fatalf("move by name = %+v", res)
	}
	waitNotBusy(t, e)
	c := cardNumbered(e, 1)
	if c.ColumnID != "exec" || c.Sessions[store.MainSession] == nil || c.WorktreePath == "" {
		t.Fatalf("move did not start the agent: column=%s worktree=%q sessions=%v", c.ColumnID, c.WorktreePath, c.Sessions)
	}

	if res := ask(t, root, inbox.Request{Op: inbox.OpRemove, Number: 1}); res.Error != "" {
		t.Fatalf("rm = %+v", res)
	}
	if c := cardNumbered(e, 1); c != nil {
		t.Errorf("card kept after rm: %+v", c)
	}
	if exists(filepath.Join(root, config.DefaultWorktreesDir, "1")) {
		t.Error("worktree kept after rm")
	}
}

func TestCLIAddIntoAColumnStartsTheCard(t *testing.T) {
	e, root := running(t)

	res := ask(t, root, inbox.Request{Op: inbox.OpAdd, Title: ptr("Dark mode"), Column: "exec"})
	if res.Error != "" || res.Column != "Executing" {
		t.Fatalf("add = %+v", res)
	}
	waitNotBusy(t, e)
	if c := cardNumbered(e, 1); c.ColumnID != "exec" || c.Sessions[store.MainSession] == nil {
		t.Errorf("card was not started: %+v", c)
	}
}

func TestCLIRequestErrorsComeBackAndChangeNothing(t *testing.T) {
	e, root := running(t)
	ask(t, root, inbox.Request{Op: inbox.OpAdd, Title: ptr("Keep me")})

	for name, tc := range map[string]struct {
		req  inbox.Request
		want string
	}{
		"missing card":   {inbox.Request{Op: inbox.OpMove, Number: 9, Column: "done"}, "no card #9"},
		"unknown column": {inbox.Request{Op: inbox.OpMove, Number: 1, Column: "nowhere"}, "unknown column"},
		"no title":       {inbox.Request{Op: inbox.OpAdd, Title: ptr("  ")}, "needs a title"},
		"bad column add": {inbox.Request{Op: inbox.OpAdd, Title: ptr("Ghost"), Column: "nowhere"}, "unknown column"},
		"unknown op":     {inbox.Request{Op: "explode"}, "unknown request"},
		"blank edit":     {inbox.Request{Op: inbox.OpEdit, Number: 1, Title: ptr(" ")}, "needs a title"},
		"edit missing":   {inbox.Request{Op: inbox.OpEdit, Number: 9, Title: ptr("x")}, "no card #9"},
	} {
		if res := ask(t, root, tc.req); !strings.Contains(res.Error, tc.want) {
			t.Errorf("%s: error = %q, want %q", name, res.Error, tc.want)
		}
	}
	if tasks := e.Snapshot().Tasks; len(tasks) != 1 || tasks[0].ColumnID != "todo" {
		t.Errorf("failed requests changed the board: %+v", tasks)
	}
}

func TestCLIRequestsQueuedWhileClosedApplyOnOpen(t *testing.T) {
	root, _ := setupProject(t)
	for _, title := range []string{"first", "second"} {
		if _, err := inbox.Send(root, inbox.Request{Op: inbox.OpAdd, Title: ptr(title)}); err != nil {
			t.Fatal(err)
		}
	}
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	waitForAll(t, e, "both queued cards", func(tasks []*store.Task) bool { return len(tasks) == 2 })
	tasks := e.Snapshot().Tasks
	if len(tasks) != 2 || tasks[0].Title != "first" || tasks[1].Title != "second" {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestCLIEditChangesOnlyWhatItIsGiven(t *testing.T) {
	e, root := running(t)
	ask(t, root, inbox.Request{Op: inbox.OpAdd, Title: ptr("Title"), Prompt: ptr("Prompt")})

	ask(t, root, inbox.Request{Op: inbox.OpEdit, Number: 1, Prompt: ptr("New prompt")})
	if c := cardNumbered(e, 1); c.Title != "Title" || c.Prompt != "New prompt" {
		t.Errorf("prompt-only edit: %+v", c)
	}
	ask(t, root, inbox.Request{Op: inbox.OpEdit, Number: 1, Prompt: ptr("")})
	if c := cardNumbered(e, 1); c.Title != "Title" || c.Prompt != "" {
		t.Errorf("clearing the prompt: %+v", c)
	}
}

func TestCLIAddByColumnNameStaysInTodo(t *testing.T) {
	e, root := running(t)
	for _, column := range []string{"todo", "to do"} {
		if res := ask(t, root, inbox.Request{Op: inbox.OpAdd, Title: ptr(column), Column: column}); res.Error != "" || res.Column != "To Do" {
			t.Fatalf("add -c %q = %+v", column, res)
		}
	}
	for _, c := range e.Snapshot().Tasks {
		if c.ColumnID != "todo" || len(c.Sessions) != 0 {
			t.Errorf("card %q left To Do or started: %+v", c.Title, c)
		}
	}
}

// startedCard adds a card to Executing and makes its worktree dirty.
func startedCard(t *testing.T, e *Engine, root string) *store.Task {
	t.Helper()
	if res := ask(t, root, inbox.Request{Op: inbox.OpAdd, Title: ptr("Work"), Column: "exec"}); res.Error != "" {
		t.Fatalf("add = %+v", res)
	}
	waitNotBusy(t, e)
	c := cardNumbered(e, 1)
	if c.WorktreePath == "" {
		t.Fatal("no worktree")
	}
	if err := os.WriteFile(filepath.Join(c.WorktreePath, "wip.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCLIRemoveRefusesADirtyWorktreeUnlessForced(t *testing.T) {
	e, root := running(t)
	c := startedCard(t, e, root)

	if res := ask(t, root, inbox.Request{Op: inbox.OpRemove, Number: 1}); !strings.Contains(res.Error, "uncommitted changes") {
		t.Fatalf("rm of a dirty card = %+v", res)
	}
	if cardNumbered(e, 1) == nil || !exists(c.WorktreePath) {
		t.Fatal("a refused rm still removed the card or its worktree")
	}
	if res := ask(t, root, inbox.Request{Op: inbox.OpRemove, Number: 1, Force: true}); res.Error != "" {
		t.Fatalf("forced rm = %+v", res)
	}
	if cardNumbered(e, 1) != nil || exists(c.WorktreePath) {
		t.Error("forced rm left the card or its worktree")
	}
}

func TestCLIMoveToDoneKeepsADirtyWorktree(t *testing.T) {
	e, root := running(t)
	c := startedCard(t, e, root)

	if res := ask(t, root, inbox.Request{Op: inbox.OpMove, Number: 1, Column: "done"}); res.Error != "" {
		t.Fatalf("move = %+v", res)
	}
	if got := cardNumbered(e, 1); got.ColumnID != "done" || !exists(c.WorktreePath) {
		t.Fatalf("move to Done should keep the worktree: %+v", got)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fakeLinearIssue(t *testing.T) *linear.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"issue":{"id":"i1","identifier":"DX-1","title":"Speed up CI","description":"Too slow.",
		  "url":"https://linear.app/x/issue/DX-1","branchName":"aapeli/dx-1-speed-up-ci",
		  "state":{"name":"Todo","type":"unstarted"},"team":{"id":"t1","key":"DX"}}}}`))
	}))
	t.Cleanup(srv.Close)
	return &linear.Client{Key: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}
}

func TestCLIAddLinearImportsTheIssueOnce(t *testing.T) {
	e, root := running(t)
	e.linear = fakeLinearIssue(t)

	res := ask(t, root, inbox.Request{Op: inbox.OpAdd, Linear: "DX-1"})
	if res.Error != "" || res.Number != 1 || res.Title != "Speed up CI" {
		t.Fatalf("add --linear = %+v", res)
	}
	c := cardNumbered(e, 1)
	if c.Issue == nil || c.Issue.Key != "DX-1" || c.Branch != "aapeli/dx-1-speed-up-ci" || !strings.Contains(c.Prompt, "Too slow.") {
		t.Errorf("imported card = %+v", c)
	}
	if res := ask(t, root, inbox.Request{Op: inbox.OpAdd, Linear: "DX-1"}); !strings.Contains(res.Error, "already on the board") {
		t.Errorf("second import = %+v", res)
	}
	if n := len(e.Snapshot().Tasks); n != 1 {
		t.Errorf("%d cards after importing the same issue twice", n)
	}
}

func TestCLIAddLinearNeedsAnAPIKey(t *testing.T) {
	e, root := running(t)
	e.linear = nil
	if res := ask(t, root, inbox.Request{Op: inbox.OpAdd, Linear: "DX-1"}); !strings.Contains(res.Error, "LINEAR_API_KEY") {
		t.Errorf("add --linear without a key = %+v", res)
	}
}

func TestSecondBoardLeavesTheInboxAlone(t *testing.T) {
	root, _ := setupProject(t)
	release, ok := inbox.Hold(root)
	if !ok {
		t.Fatal("could not hold the lock")
	}
	defer release()
	second, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go second.Run(ctx)

	id, _ := inbox.Send(root, inbox.Request{Op: inbox.OpAdd, Title: ptr("Mine")})
	if _, ok, _ := inbox.Wait(root, id, 1500*time.Millisecond, nil); ok {
		t.Error("a board without the lock applied the request")
	}
	if n := len(second.Snapshot().Tasks); n != 0 {
		t.Errorf("%d cards added by a board without the lock", n)
	}
}

func TestClaimedRequestIsAppliedAfterARestart(t *testing.T) {
	root, _ := setupProject(t)
	id, _ := inbox.Send(root, inbox.Request{Op: inbox.OpAdd, Title: ptr("Interrupted")})
	if _, ok, _ := inbox.Next(root); !ok {
		t.Fatal("nothing to claim")
	}
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, ok, err := inbox.Wait(root, id, 10*time.Second, nil)
	if err != nil || !ok || res.Error != "" || res.Title != "Interrupted" {
		t.Fatalf("Wait = %+v, %v, %v", res, ok, err)
	}
}

func TestBoardHoldsTheLockWhileRunning(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !inbox.Running(root) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !inbox.Running(root) {
		t.Fatal("a running board did not mark itself open")
	}
	cancel()
	<-done
	if inbox.Running(root) {
		t.Error("the lock outlived Run")
	}
}
