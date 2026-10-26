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
