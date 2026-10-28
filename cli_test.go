package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/inbox"
	"github.com/aapzu/drover/internal/store"
)

func TestParseArgsAcceptsFlagsAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"Fix it", "-c", "exec", "--prompt", "now"},
		{"-c", "exec", "Fix it", "--prompt", "now"},
		{"--prompt=now", "-c=exec", "Fix it"},
	} {
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		prompt, column := fs.String("prompt", "", ""), fs.String("c", "", "")
		rest, err := parseArgs(fs, args)
		if err != nil || !slices.Equal(rest, []string{"Fix it"}) || *prompt != "now" || *column != "exec" {
			t.Errorf("parseArgs(%q) = %q, prompt=%q column=%q, err=%v", args, rest, *prompt, *column, err)
		}
	}
}

func TestParseNumber(t *testing.T) {
	for in, want := range map[string]int{"12": 12, "#12": 12} {
		if n, err := parseNumber(in); err != nil || n != want {
			t.Errorf("parseNumber(%q) = %d, %v", in, n, err)
		}
	}
	for _, in := range []string{"", "x", "0", "-1", "#"} {
		if _, err := parseNumber(in); err == nil {
			t.Errorf("parseNumber(%q) accepted", in)
		}
	}
}

func TestValidateRejectsWhatCouldNeverApply(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(root, 0o755)
	state := &store.State{NextNumber: 2, Tasks: []*store.Task{{ID: "a", Number: 1, ColumnID: "planning"}}}
	if err := state.Save(root); err != nil {
		t.Fatal(err)
	}
	title := "x"

	for name, tc := range map[string]struct {
		req  inbox.Request
		want string
	}{
		"unknown column": {inbox.Request{Op: inbox.OpMove, Number: 1, Column: "nowhere"}, `unknown column "nowhere"; columns are planning, executing`},
		"unknown card":   {inbox.Request{Op: inbox.OpRemove, Number: 7}, "no card #7"},
		"bad linear":     {inbox.Request{Op: inbox.OpAdd, Linear: "banana"}, "not a Linear issue key"},
		"fine move":      {inbox.Request{Op: inbox.OpMove, Number: 1, Column: "In review"}, ""},
		"fine add":       {inbox.Request{Op: inbox.OpAdd, Title: &title, Column: "planning"}, ""},
		"fine linear":    {inbox.Request{Op: inbox.OpAdd, Linear: "DX-1"}, ""},
	} {
		err := validate(root, tc.req)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error = %v, want %q", name, err, tc.want)
		}
	}
}

func TestAgentState(t *testing.T) {
	session := func(status store.SessionStatus, act store.Activity) *store.Task {
		return &store.Task{ID: "a", Sessions: map[string]*store.Session{store.MainSession: {Status: status, Activity: act}}}
	}
	agentColumn := config.Column{ID: "planning", AutoSpawn: true}
	for name, tc := range map[string]struct {
		task *store.Task
		col  config.Column
		want string
	}{
		"no agent":               {&store.Task{ID: "a"}, config.Column{}, ""},
		"working":                {session(store.StatusRunning, store.ActivityThinking), config.Column{}, "working"},
		"idle":                   {session(store.StatusRunning, store.ActivityIdle), config.Column{}, "idle"},
		"background":             {&store.Task{ID: "a", Sessions: map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityIdle, Background: []string{"b1"}}}}, config.Column{}, "background"},
		"permission":             {session(store.StatusRunning, store.ActivityPermission), config.Column{}, "needs you"},
		"suspended":              {session(store.StatusSuspended, store.ActivityIdle), config.Column{}, "suspended"},
		"queued":                 {session(store.StatusQueued, store.ActivityStarting), config.Column{}, "queued"},
		"external working":       {session(store.StatusExternal, store.ActivityThinking), config.Column{}, "working"},
		"external waiting":       {session(store.StatusExternal, store.ActivityPermission), config.Column{}, "needs you"},
		"exited":                 {session(store.StatusExited, store.ActivityIdle), config.Column{}, "exited"},
		"starting":               {session(store.StatusRunning, store.ActivityStarting), config.Column{}, "starting"},
		"agent column, no start": {&store.Task{ID: "a"}, agentColumn, "not started"},
		"plain column, no start": {&store.Task{ID: "a"}, config.Column{ID: "review"}, ""},
		"done column, no start":  {&store.Task{ID: "a"}, config.Column{ID: "done", Role: "done"}, ""},
	} {
		if got := agentState(tc.task, tc.col); got != tc.want {
			t.Errorf("%s: agentState = %q, want %q", name, got, tc.want)
		}
	}
}

func TestCardsReportTheOwnersBranchAndAgent(t *testing.T) {
	board := &config.Board{Columns: []config.Column{{ID: "inbox", Name: "Inbox"}, {ID: "exec", Name: "Executing"}}}
	owner := &store.Task{ID: "a", Number: 1, ColumnID: "exec", Branch: "b/one", WorktreePath: "/wt/1", Sessions: map[string]*store.Session{
		store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityThinking},
	}}
	split := &store.Task{ID: "b", Number: 2, ColumnID: "exec", GroupID: "a", PR: &store.PR{URL: "https://x/pr/2"}}
	bare := &store.Task{ID: "c", Number: 3, ColumnID: "inbox", Issue: &store.Issue{Key: "DX-1"}}

	cards := buildCards(board, []*store.Task{split, owner, bare})
	if got := []int{cards[0].Number, cards[1].Number, cards[2].Number}; !slices.Equal(got, []int{3, 1, 2}) {
		t.Fatalf("order = %v, want board order then number", got)
	}
	for _, c := range cards[1:] {
		if c.Branch != "b/one" || c.Worktree != "/wt/1" || c.Agent != "working" {
			t.Errorf("#%d = %+v, want the owner's branch, worktree and agent", c.Number, c)
		}
	}
	data, _ := json.Marshal(cards[0])
	if strings.Contains(string(data), "branch") || strings.Contains(string(data), "agent") || !strings.Contains(string(data), `"issue":"DX-1"`) {
		t.Errorf("a card with no worktree should omit the empty fields: %s", data)
	}
	data, _ = json.Marshal(cards[1])
	for _, key := range []string{`"branch":"b/one"`, `"worktree":"/wt/1"`, `"agent":"working"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("json lacks %s: %s", key, data)
		}
	}
}

func TestCardsReportThePRState(t *testing.T) {
	const url = "https://x/pr/1"
	board := &config.Board{Columns: []config.Column{{ID: "review", Name: "In review"}}}
	tests := map[string]struct {
		pr   *store.PR
		want *cardPR
	}{
		"open":        {&store.PR{URL: url, Number: 1, State: "OPEN"}, &cardPR{URL: url, Number: 1, State: "open"}},
		"merged":      {&store.PR{URL: url, State: "MERGED", Released: true}, &cardPR{URL: url, State: "merged", Released: true}},
		"closed":      {&store.PR{URL: url, State: "CLOSED"}, &cardPR{URL: url, State: "closed"}},
		"other state": {&store.PR{URL: url, State: "DRAFT"}, &cardPR{URL: url, State: "unknown"}},
		"not polled":  {&store.PR{URL: url}, &cardPR{URL: url, State: "unknown"}},
		"approved":    {&store.PR{URL: url, State: "OPEN", ReviewDecision: "APPROVED"}, &cardPR{URL: url, State: "open", ReviewDecision: "approved"}},
		"changes":     {&store.PR{URL: url, State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}, &cardPR{URL: url, State: "open", ReviewDecision: "changes requested"}},
		"queued":      {&store.PR{URL: url, State: "OPEN", InMergeQueue: true}, &cardPR{URL: url, State: "open", InMergeQueue: true}},
		"undecided":   {&store.PR{URL: url, State: "OPEN", ReviewDecision: "REVIEW_REQUIRED"}, &cardPR{URL: url, State: "open"}},
		"gh failed":   {&store.PR{URL: url, State: "MERGED", Error: "gh: boom"}, &cardPR{URL: url, State: "merged", Error: "gh: boom"}},
		"reviewing":   {&store.PR{URL: url, State: "OPEN", Review: true}, &cardPR{URL: url, State: "open", Reviewing: true}},
		"long error":  {&store.PR{URL: url, State: "OPEN", Error: "gh: boom\nstack trace"}, &cardPR{URL: url, State: "open", Error: "gh: boom"}},
		"no PR":       {nil, nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cards := buildCards(board, []*store.Task{{ID: "a", Number: 1, ColumnID: "review", PR: tc.pr}})
			if !reflect.DeepEqual(cards[0].PR, tc.want) {
				t.Errorf("pr = %+v, want %+v", cards[0].PR, tc.want)
			}
			data, err := json.Marshal(cards[0])
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil && strings.Contains(string(data), `"pr"`) {
				t.Errorf("a card with no PR should omit pr: %s", data)
			}
			if tc.want != nil && tc.want.ReviewDecision == "" && strings.Contains(string(data), "reviewDecision") {
				t.Errorf("an undecided PR should omit reviewDecision: %s", data)
			}
		})
	}
}

func TestListRefJoinsTheIssueAndPR(t *testing.T) {
	pr := &cardPR{URL: "https://x/pr/1"}
	for name, tc := range map[string]struct {
		card card
		want string
	}{
		"both":       {card{Issue: "DX-1", PR: pr}, "DX-1 https://x/pr/1"},
		"issue only": {card{Issue: "DX-1"}, "DX-1"},
		"PR only":    {card{PR: pr}, "https://x/pr/1"},
		"neither":    {card{}, ""},
	} {
		if got := tc.card.ref(); got != tc.want {
			t.Errorf("%s: ref = %q, want %q", name, got, tc.want)
		}
	}
}

func fakeAsk(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	orig := ask
	t.Cleanup(func() { ask = orig })
	ask = func(_ context.Context, _, _, question string, fresh bool) (string, error) {
		calls = append(calls, fmt.Sprintf("%q fresh=%v", question, fresh))
		return "answer", nil
	}
	return &calls
}

func TestAsk(t *testing.T) {
	for name, tc := range map[string]struct {
		args    []string
		stdin   string
		want    []string
		wantErr string
	}{
		"joins the words and passes --new": {args: []string{"where", "is", "#2?", "--new"}, want: []string{`"where is #2?" fresh=true`}},
		"reads the question from stdin":    {args: []string{"-"}, stdin: "what needs me?", want: []string{`"what needs me?" fresh=false`}},
		"needs a question":                 {wantErr: "usage: drover ask"},
		"rejects an empty stdin":           {args: []string{"-"}, wantErr: "stdin is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			calls := fakeAsk(t)
			withStdin(t, tc.stdin)
			var err error
			out := stdout(t, func() { err = cmdAsk(t.TempDir(), tc.args) })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || len(*calls) != 0 {
					t.Errorf("err = %v, ask calls = %q, want error %q", err, *calls, tc.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(*calls, tc.want) || out != "answer\n" {
				t.Errorf("err = %v, ask calls = %q, stdout = %q, want %q", err, *calls, out, tc.want)
			}
		})
	}
}

func TestAskNewAloneForgetsTheConversation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	calls := fakeAsk(t)
	file := filepath.Join(store.Dir(root), "assistant-session")
	os.MkdirAll(filepath.Dir(file), 0o755)
	os.WriteFile(file, []byte("e6a0c6f0-5c0e-4b53-9a52-0e0f0a5a2f11\n"), 0o644)

	if err := cmdAsk(root, []string{"--new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) || len(*calls) != 0 {
		t.Errorf("--new alone should forget the conversation without asking: stat err %v, calls %q", err, *calls)
	}
}

func TestAskPrintsTheAnswerEvenWhenItCouldNotBeSaved(t *testing.T) {
	orig := ask
	t.Cleanup(func() { ask = orig })
	notSaved := errors.New("could not save")
	ask = func(context.Context, string, string, string, bool) (string, error) { return "moved #2", notSaved }
	var err error
	out := stdout(t, func() { err = cmdAsk(t.TempDir(), []string{"move #2"}) })
	if err != notSaved || !strings.Contains(out, "moved #2") {
		t.Errorf("err = %v, stdout = %q", err, out)
	}
}

func stdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = orig
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func withStdin(t *testing.T, s string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; r.Close() })
	w.WriteString(s)
	w.Close()
}

// project is a repo root with two cards, on the default board.
func project(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(root, 0o755)
	state := &store.State{NextNumber: 3, Tasks: []*store.Task{
		{ID: "a", Number: 1, Title: "Fix login", ColumnID: "executing", Issue: &store.Issue{Key: "DX-1"}},
		{ID: "b", Number: 2, ColumnID: "planning"},
	}}
	if err := state.Save(root); err != nil {
		t.Fatal(err)
	}
	return root
}

// openBoard holds the project's lock like a running board and answers every
// request with reply. It returns the requests seen so far.
func openBoard(t *testing.T, root string, reply func(inbox.Request) inbox.Result) func() []inbox.Request {
	t.Helper()
	release, ok := inbox.Hold(root)
	if !ok {
		t.Fatal("could not hold the board lock")
	}
	var mu sync.Mutex
	var seen []inbox.Request
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			p, ok, _ := inbox.Next(root)
			if !ok {
				continue
			}
			mu.Lock()
			seen = append(seen, p.Request)
			mu.Unlock()
			if reply == nil {
				release()
				continue
			}
			inbox.Reply(root, p.ID, reply(p.Request))
			inbox.Finish(root, p.ID)
		}
	}()
	t.Cleanup(func() { close(stop); <-stopped; release() })
	return func() []inbox.Request {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

func TestCommandsSendTheRequestAndConfirm(t *testing.T) {
	title, prompt := "Fix it", "now"
	for name, tc := range map[string]struct {
		cmd   func(root string, args []string) error
		args  []string
		stdin string
		reply inbox.Result
		want  inbox.Request
		out   string
	}{
		"add":              {cmdAdd, []string{"Fix it", "-p", "now", "-c", "executing"}, "", inbox.Result{Number: 3, Title: "Fix it", Column: "Executing"}, inbox.Request{Op: inbox.OpAdd, Title: &title, Prompt: &prompt, Column: "executing"}, "Added #3 Fix it (Executing)\n"},
		"add from stdin":   {cmdAdd, []string{"Fix it", "--prompt", "-"}, "now", inbox.Result{Number: 3, Title: "Fix it", Column: "Planning"}, inbox.Request{Op: inbox.OpAdd, Title: &title, Prompt: &prompt}, "Added #3 Fix it (Planning)\n"},
		"add linear":       {cmdAdd, []string{"--linear", "DX-9", "-c", "planning"}, "", inbox.Result{Number: 3, Title: "From Linear", Column: "Planning"}, inbox.Request{Op: inbox.OpAdd, Linear: "DX-9", Column: "planning"}, "Added #3 From Linear (Planning)\n"},
		"move":             {cmdMove, []string{"#1", "In review"}, "", inbox.Result{Number: 1, Column: "In review"}, inbox.Request{Op: inbox.OpMove, Number: 1, Column: "In review"}, "Moved #1 to In review\n"},
		"edit title":       {cmdEdit, []string{"1", "--title", "Fix it"}, "", inbox.Result{Number: 1}, inbox.Request{Op: inbox.OpEdit, Number: 1, Title: &title}, "Updated #1\n"},
		"edit from stdin":  {cmdEdit, []string{"1", "--prompt", "-"}, "now", inbox.Result{Number: 1}, inbox.Request{Op: inbox.OpEdit, Number: 1, Prompt: &prompt}, "Updated #1\n"},
		"remove":           {cmdRemove, []string{"2"}, "", inbox.Result{Number: 2}, inbox.Request{Op: inbox.OpRemove, Number: 2}, "Deleted #2\n"},
		"remove and force": {cmdRemove, []string{"2", "--force"}, "", inbox.Result{Number: 2}, inbox.Request{Op: inbox.OpRemove, Number: 2, Force: true}, "Deleted #2\n"},
	} {
		t.Run(name, func(t *testing.T) {
			root := project(t)
			seen := openBoard(t, root, func(inbox.Request) inbox.Result { return tc.reply })
			withStdin(t, tc.stdin)
			var err error
			out := stdout(t, func() { err = tc.cmd(root, tc.args) })
			if err != nil || out != tc.out {
				t.Errorf("err = %v, stdout = %q, want %q", err, out, tc.out)
			}
			if got := seen(); !reflect.DeepEqual(got, []inbox.Request{tc.want}) {
				t.Errorf("board saw %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCommandsRejectBadInputBeforeSending(t *testing.T) {
	for name, tc := range map[string]struct {
		cmd   func(root string, args []string) error
		args  []string
		stdin string
		want  string
	}{
		"add without a title":      {cmdAdd, nil, "", "usage: drover add"},
		"add with a blank title":   {cmdAdd, []string{"  "}, "", "usage: drover add"},
		"add with two titles":      {cmdAdd, []string{"a", "b"}, "", "usage: drover add"},
		"add linear with a title":  {cmdAdd, []string{"--linear", "DX-1", "Fix"}, "", "takes the title and prompt from the issue"},
		"add linear with a prompt": {cmdAdd, []string{"--linear", "DX-1", "-p", "x"}, "", "takes the title and prompt from the issue"},
		"add with an empty stdin":  {cmdAdd, []string{"Fix", "-p", "-"}, "", "stdin is empty"},
		"add with an unknown flag": {cmdAdd, []string{"Fix", "--nope"}, "", "flag provided but not defined"},
		"add to an unknown column": {cmdAdd, []string{"Fix", "-c", "nowhere"}, "", `unknown column "nowhere"`},
		"move with one argument":   {cmdMove, []string{"1"}, "", "usage: drover move"},
		"move a bad number":        {cmdMove, []string{"x", "done"}, "", `"x" is not a card number`},
		"move an unknown card":     {cmdMove, []string{"9", "done"}, "", "no card #9"},
		"edit without a number":    {cmdEdit, []string{"--title", "x"}, "", "usage: drover edit"},
		"edit a bad number":        {cmdEdit, []string{"0", "--title", "x"}, "", `"0" is not a card number`},
		"edit nothing":             {cmdEdit, []string{"1"}, "", "nothing to change"},
		"edit to a blank title":    {cmdEdit, []string{"1", "--title", " "}, "", "a card needs a title"},
		"edit with an empty stdin": {cmdEdit, []string{"1", "--prompt", "-"}, "", "stdin is empty"},
		"remove without a number":  {cmdRemove, nil, "", "usage: drover rm"},
		"remove a bad number":      {cmdRemove, []string{"x"}, "", `"x" is not a card number`},
		"remove an unknown card":   {cmdRemove, []string{"9"}, "", "no card #9"},
	} {
		t.Run(name, func(t *testing.T) {
			root := project(t)
			withStdin(t, tc.stdin)
			err := tc.cmd(root, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if _, ok, _ := inbox.Next(root); ok {
				t.Error("a rejected command should not queue a request")
			}
		})
	}
}

func TestSendWithoutAnOpenBoardQueuesTheRequest(t *testing.T) {
	root := project(t)
	var err error
	out := stdout(t, func() { err = cmdMove(root, []string{"1", "done"}) })
	if err != nil || !strings.Contains(out, "Queued:") {
		t.Fatalf("err = %v, stdout = %q", err, out)
	}
	p, ok, err := inbox.Next(root)
	if err != nil || !ok || p.Request.Op != inbox.OpMove || p.Request.Number != 1 || p.Request.Column != "done" {
		t.Errorf("queued request = %+v, ok=%v, err=%v", p.Request, ok, err)
	}
}

func TestSendReportsWhatTheBoardAnswered(t *testing.T) {
	short := func(t *testing.T) {
		orig := waitForBoard
		t.Cleanup(func() { waitForBoard = orig })
		waitForBoard = 150 * time.Millisecond
	}
	for name, tc := range map[string]struct {
		reply func(inbox.Request) inbox.Result
		want  string
	}{
		"an error result":      {func(inbox.Request) inbox.Result { return inbox.Result{Error: "card is busy"} }, "card is busy"},
		"a board that closes":  {nil, "the board closed before answering"},
		"a board that is slow": {func(inbox.Request) inbox.Result { time.Sleep(time.Second); return inbox.Result{} }, "no answer from the board after 150ms"},
	} {
		t.Run(name, func(t *testing.T) {
			short(t)
			root := project(t)
			openBoard(t, root, tc.reply)
			err := cmdRemove(root, []string{"2"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPromptArg(t *testing.T) {
	for name, tc := range map[string]struct {
		in, stdin, want, wantErr string
	}{
		"literal":         {in: "do it", want: "do it"},
		"empty":           {in: "", want: ""},
		"from stdin":      {in: "-", stdin: "line one\nline two\n", want: "line one\nline two\n"},
		"blank stdin":     {in: "-", stdin: " \n", wantErr: "stdin is empty"},
		"literal dash id": {in: "-x", want: "-x"},
	} {
		t.Run(name, func(t *testing.T) {
			withStdin(t, tc.stdin)
			got, err := promptArg(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("promptArg(%q) = %q, %v, want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestListPrintsTheCards(t *testing.T) {
	root := project(t)
	var err error
	out := stdout(t, func() { err = cmdList(root, nil) })
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("list printed %q, want two lines", out)
	}
	for i, want := range [][]string{{"#2", "Planning", "(untitled)"}, {"#1", "Executing", "Fix login", "DX-1"}} {
		if got := strings.Fields(lines[i]); !slices.Equal(got[:2], want[:2]) || !strings.Contains(lines[i], want[2]) || (len(want) > 3 && got[len(got)-1] != want[3]) {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

func TestListJSON(t *testing.T) {
	root := project(t)
	var err error
	out := stdout(t, func() { err = cmdList(root, []string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var cards []card
	if err := json.Unmarshal([]byte(out), &cards); err != nil {
		t.Fatalf("list --json = %q: %v", out, err)
	}
	if len(cards) != 2 || cards[0].Number != 2 || cards[0].ColumnID != "planning" || cards[0].Agent != "not started" ||
		cards[1].Number != 1 || cards[1].Title != "Fix login" || cards[1].Issue != "DX-1" || cards[1].Column != "Executing" {
		t.Errorf("cards = %+v", cards)
	}
}

func TestListFailsOnABadBoard(t *testing.T) {
	root := project(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte("{"), 0o644)
	if err := cmdList(root, nil); err == nil || !strings.Contains(err.Error(), "drover.json") {
		t.Errorf("err = %v, want a drover.json parse error", err)
	}
	if err := cmdList(t.TempDir(), []string{"--nope"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

func TestColumnNames(t *testing.T) {
	board := &config.Board{Columns: []config.Column{{ID: "inbox", Name: "Inbox"}, {ID: "exec", Name: "Executing"}}}
	if got := columnNames(board); got != "inbox, exec" {
		t.Errorf("columnNames = %q", got)
	}
	if got := columnNames(&config.Board{}); got != "" {
		t.Errorf("columnNames of no columns = %q", got)
	}
}

func TestErrorLine(t *testing.T) {
	long := strings.Repeat("é", 250)
	for name, tc := range map[string]struct{ in, want string }{
		"empty":        {"", ""},
		"one line":     {"gh: boom", "gh: boom"},
		"first line":   {"gh: boom\nstack trace", "gh: boom"},
		"trimmed":      {"\n  gh: boom  \n", "gh: boom"},
		"cut by runes": {long, strings.Repeat("é", 200) + "…"},
	} {
		if got := errorLine(tc.in); got != tc.want {
			t.Errorf("%s: errorLine = %q, want %q", name, got, tc.want)
		}
	}
}
