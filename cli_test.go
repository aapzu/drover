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
	"slices"
	"strings"
	"testing"

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
	state := &store.State{NextNumber: 2, Tasks: []*store.Task{{ID: "a", Number: 1, ColumnID: "todo"}}}
	if err := state.Save(root); err != nil {
		t.Fatal(err)
	}
	title := "x"

	for name, tc := range map[string]struct {
		req  inbox.Request
		want string
	}{
		"unknown column": {inbox.Request{Op: inbox.OpMove, Number: 1, Column: "nowhere"}, `unknown column "nowhere"; columns are todo, planning`},
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
	for name, tc := range map[string]struct {
		task *store.Task
		want string
	}{
		"no agent":         {&store.Task{ID: "a"}, ""},
		"working":          {session(store.StatusRunning, store.ActivityThinking), "working"},
		"idle":             {session(store.StatusRunning, store.ActivityIdle), "idle"},
		"permission":       {session(store.StatusRunning, store.ActivityPermission), "needs you"},
		"suspended":        {session(store.StatusSuspended, store.ActivityIdle), "suspended"},
		"queued":           {session(store.StatusQueued, store.ActivityStarting), "queued"},
		"external working": {session(store.StatusExternal, store.ActivityThinking), "working"},
		"external waiting": {session(store.StatusExternal, store.ActivityPermission), "needs you"},
		"exited":           {session(store.StatusExited, store.ActivityIdle), "exited"},
		"starting":         {session(store.StatusRunning, store.ActivityStarting), "starting"},
	} {
		if got := agentState(tc.task); got != tc.want {
			t.Errorf("%s: agentState = %q, want %q", name, got, tc.want)
		}
	}
}

func TestAskNeedsAQuestion(t *testing.T) {
	if err := cmdAsk(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "usage: drover ask") {
		t.Errorf("cmdAsk without a question = %v", err)
	}
}

func TestCardsReportTheOwnersBranchAndAgent(t *testing.T) {
	board := &config.Board{Columns: []config.Column{{ID: "todo", Name: "Todo"}, {ID: "exec", Name: "Executing"}}}
	owner := &store.Task{ID: "a", Number: 1, ColumnID: "exec", Branch: "b/one", WorktreePath: "/wt/1", Sessions: map[string]*store.Session{
		store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityThinking},
	}}
	split := &store.Task{ID: "b", Number: 2, ColumnID: "exec", GroupID: "a", PR: &store.PR{URL: "https://x/pr/2"}}
	bare := &store.Task{ID: "c", Number: 3, ColumnID: "todo", Issue: &store.Issue{Key: "DX-1"}}

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
	board := &config.Board{Columns: []config.Column{{ID: "review", Name: "In review"}}}
	tests := map[string]struct {
		pr   *store.PR
		want []string
		not  []string
	}{
		"open":      {&store.PR{URL: "https://x/pr/1", Number: 1, State: "OPEN"}, []string{`"url":"https://x/pr/1"`, `"number":1`, `"state":"open"`}, []string{`"reviewDecision"`, `"released"`}},
		"merged":    {&store.PR{URL: "https://x/pr/1", State: "MERGED", Released: true}, []string{`"state":"merged"`, `"released":true`}, nil},
		"closed":    {&store.PR{URL: "https://x/pr/1", State: "CLOSED"}, []string{`"state":"closed"`}, nil},
		"unknown":   {&store.PR{URL: "https://x/pr/1"}, []string{`"state":"unknown"`}, nil},
		"approved":  {&store.PR{URL: "https://x/pr/1", State: "OPEN", ReviewDecision: "APPROVED"}, []string{`"reviewDecision":"approved"`}, nil},
		"changes":   {&store.PR{URL: "https://x/pr/1", State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}, []string{`"reviewDecision":"changes requested"`}, nil},
		"undecided": {&store.PR{URL: "https://x/pr/1", State: "OPEN", ReviewDecision: "REVIEW_REQUIRED"}, nil, []string{`"reviewDecision"`}},
		"gh failed": {&store.PR{URL: "https://x/pr/1", State: "MERGED", Error: "gh: boom"}, []string{`"state":"merged"`, `"error":"gh: boom"`}, nil},
		"no PR":     {nil, nil, []string{`"pr"`}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cards := buildCards(board, []*store.Task{{ID: "a", Number: 1, ColumnID: "review", PR: tc.pr}})
			data, _ := json.Marshal(cards[0])
			for _, s := range tc.want {
				if !strings.Contains(string(data), s) {
					t.Errorf("json lacks %s: %s", s, data)
				}
			}
			for _, s := range tc.not {
				if strings.Contains(string(data), s) {
					t.Errorf("json has %s: %s", s, data)
				}
			}
		})
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

func TestAskJoinsWordsAndPassesNew(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	calls := fakeAsk(t)
	if err := cmdAsk(t.TempDir(), []string{"where", "is", "#2?", "--new"}); err != nil {
		t.Fatal(err)
	}
	if want := `"where is #2?" fresh=true`; len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("ask calls = %q, want %q", *calls, want)
	}
}

func TestAskReadsTheQuestionFromStdin(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	calls := fakeAsk(t)
	r, w, _ := os.Pipe()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })
	w.WriteString("what needs me?")
	w.Close()
	if err := cmdAsk(t.TempDir(), []string{"-"}); err != nil {
		t.Fatal(err)
	}
	if want := `"what needs me?" fresh=false`; len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("ask calls = %q, want %q", *calls, want)
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
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := cmdAsk(t.TempDir(), []string{"move #2"})
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	if err != notSaved || !strings.Contains(string(out), "moved #2") {
		t.Errorf("err = %v, stdout = %q", err, out)
	}
}
