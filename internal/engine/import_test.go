package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/gh"
	"github.com/aapzu/drover/internal/linear"
	"github.com/aapzu/drover/internal/store"
)

// An agent imported while it runs in another terminal is mirrored, never
// duplicated, and is resumed on the board in its own directory once it exits.
func TestImportRunningAgentLifecycle(t *testing.T) {
	root, log := setupProject(t)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	os.MkdirAll(filepath.Join(claudeDir, "sessions"), 0o755)
	agent := exec.Command("sleep", "60")
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	defer agent.Process.Kill()
	register := func(pid int) string {
		path := filepath.Join(claudeDir, "sessions", fmt.Sprintf("%d.json", pid))
		entry, _ := json.Marshal(claude.Live{PID: pid, SessionID: "abcdef12-live", Cwd: root, Status: "busy"})
		os.WriteFile(path, entry, 0o644)
		return path
	}
	registry := register(agent.Process.Pid)

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	runEngine(t, e)

	found, _ := e.DiscoverSessions(40)
	if len(found) != 1 || !found[0].Running {
		t.Fatalf("discover = %+v", found)
	}
	e.ImportSessions(found)
	task := e.Snapshot().Tasks[0]
	if task.ColumnID != "exec" || task.WorktreePath != root || task.Sessions[store.MainSession].Status != store.StatusExternal {
		t.Fatalf("imported card wrong: column=%s worktree=%s", task.ColumnID, task.WorktreePath)
	}
	if again, _ := e.DiscoverSessions(40); len(again) != 0 {
		t.Errorf("imported session offered again: %+v", again)
	}
	if n := e.ImportSessions(found); n != 0 || len(e.Snapshot().Tasks) != 1 {
		t.Errorf("re-importing the same session added %d cards", n)
	}

	var ext *ExternalError
	if err := e.Move(task.ID, "done", MoveOptions{}); !errors.As(err, &ext) {
		t.Errorf("move to Done of a live external agent: err = %v", err)
	}
	if err := e.Start(task.ID); !errors.As(err, &ext) {
		t.Errorf("start of a live external agent: err = %v", err)
	}

	// A registry entry missing for a moment must not release a live agent.
	os.Remove(registry)
	time.Sleep(2 * time.Second)
	if s := e.Snapshot().Tasks[0].Sessions[store.MainSession]; s.Status != store.StatusExternal {
		t.Fatalf("live agent released when its registry entry vanished: %s", s.Status)
	}
	agent.Process.Kill()
	agent.Wait()
	waitFor(t, e, "external agent to be released", func(t *store.Task) bool {
		return t.Sessions[store.MainSession].Status == store.StatusSuspended
	})

	other := exec.Command("sleep", "60")
	other.Start()
	defer other.Process.Kill()
	exited := make(chan struct{})
	go func() { other.Wait(); close(exited) }()
	register(other.Process.Pid)
	if err := e.Start(task.ID); !errors.As(err, &ext) {
		t.Errorf("start while resumed elsewhere: err = %v", err)
	}
	if err := e.TakeOver(task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("take over left the other terminal's agent running")
	}
	waitFor(t, e, "agent resumed with the imported id", func(*store.Task) bool {
		return strings.Contains(readFile(log), "--resume\nabcdef12-live")
	})
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("project root went missing: %v", err)
	}
}

func TestImportLinearIssue(t *testing.T) {
	root, _ := setupProject(t)
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	issue := linear.Issue{ID: "i1", Identifier: "DX-1", Title: "Speed up CI", Description: "CI takes 40 minutes.",
		URL: "https://linear.app/x/issue/DX-1", BranchName: "aapeli/dx-1-speed-up-ci"}
	e.ImportIssues([]linear.Issue{issue, issue})
	snap := e.Snapshot()
	if len(snap.Tasks) != 1 {
		t.Fatalf("want one card, got %d", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if task.Branch != "aapeli/dx-1-speed-up-ci" || task.Issue.Key != "DX-1" || !strings.Contains(task.Prompt, "CI takes 40 minutes.") {
		t.Errorf("card wrong: %+v", task)
	}
	got := e.render(task, "{{issueKey}} {{issueUrl}}", groupContext{to: config.Column{}}, false)
	if got != "DX-1 https://linear.app/x/issue/DX-1" {
		t.Errorf("issue placeholders = %q", got)
	}
}

func TestImportSplitsSessionThatOpenedSeveralPRs(t *testing.T) {
	root, _ := setupProject(t)
	os.WriteFile(filepath.Join(root, "drover.json"), []byte(prBoard), 0o644)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	created := prCreatedLine
	path := claude.TranscriptPath("sid", root)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(created(1)+created(2)+created(1)), 0o644)

	found := []claude.Found{
		{SessionID: "sid", Title: "Auth", Cwd: root, TranscriptPath: path},
		{SessionID: "plain", Title: "Chat", Cwd: root},
	}
	if n := e.ImportSessions(found); n != 3 {
		t.Fatalf("ImportSessions added %d cards, want 3", n)
	}
	tasks := e.Snapshot().Tasks
	if len(tasks) != 3 || tasks[0].PR == nil || tasks[0].PR.Number != 1 {
		t.Fatalf("session card did not take PR one: %+v", tasks)
	}
	if tasks[1].PR != nil || tasks[1].GroupID != "" {
		t.Errorf("session without PRs changed: %+v", tasks[1])
	}
	if split := tasks[2]; split.GroupID != tasks[0].ID || split.PR == nil || split.PR.Number != 2 {
		t.Errorf("PR two is not split off the session card: %+v", split)
	}
	if n := e.ImportSessions(found); n != 0 || len(e.Snapshot().Tasks) != 3 {
		t.Errorf("re-importing added %d cards", n)
	}
}

func TestDiscoverSessionsSkipsTheAssistantsConversation(t *testing.T) {
	root, _ := setupProject(t)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	os.MkdirAll(filepath.Join(claudeDir, "sessions"), 0o755)
	const assistantID = "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"
	register := func(id string) {
		agent := exec.Command("sleep", "60")
		if err := agent.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })
		path := filepath.Join(claudeDir, "sessions", fmt.Sprintf("%d.json", agent.Process.Pid))
		entry, _ := json.Marshal(claude.Live{PID: agent.Process.Pid, SessionID: id, Cwd: root, Status: "busy"})
		os.WriteFile(path, entry, 0o644)
	}
	register(assistantID)
	register("abcdef12-other")
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(root), "assistant-session"), []byte(assistantID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	found, _ := e.DiscoverSessions(40)
	if len(found) != 1 || found[0].SessionID != "abcdef12-other" {
		t.Fatalf("discover = %+v; the assistant's own conversation must not be offered for import", found)
	}
}

// linearServer answers the queries LinearIssues and syncIssueStarted send and
// records the issue updates.
func linearServer(t *testing.T) (*linear.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var updates []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch {
		case strings.Contains(req.Query, "assignedIssues"):
			w.Write([]byte(`{"data":{"viewer":{"assignedIssues":{"nodes":[
			  {"id":"i1","identifier":"DX-1","title":"On the board","state":{"name":"Todo","type":"unstarted"},"team":{"id":"t1","key":"DX"}},
			  {"id":"i2","identifier":"DX-2","title":"Not yet","state":{"name":"Todo","type":"unstarted"},"team":{"id":"t1","key":"DX"}}]}}}}`))
		case strings.Contains(req.Query, "issueUpdate"):
			mu.Lock()
			updates = append(updates, req.Variables["id"].(string)+"="+req.Variables["state"].(string))
			mu.Unlock()
			w.Write([]byte(`{"data":{"issueUpdate":{"success":true}}}`))
		default:
			w.Write([]byte(`{"data":{"issue":{"state":{"type":"unstarted"},"team":{"states":{"nodes":[{"id":"s-progress","name":"In Progress","type":"started","position":1}]}}}}}`))
		}
	}))
	t.Cleanup(srv.Close)
	client := &linear.Client{Key: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(updates)
	}
}

func TestLinearIssuesSkipsTheOnesOnTheBoard(t *testing.T) {
	e := bareEngine(t)
	if e.HasLinear() {
		t.Error("HasLinear without a client")
	}
	if _, err := e.LinearIssues(context.Background()); err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY") {
		t.Errorf("LinearIssues without a key = %v", err)
	}

	e.linear, _ = linearServer(t)
	e.state.Tasks = []*store.Task{{ID: "a", Issue: &store.Issue{Provider: "linear", ID: "i1"}}, {ID: "b"}}
	issues, err := e.LinearIssues(context.Background())
	if !e.HasLinear() || err != nil || len(issues) != 1 || issues[0].Identifier != "DX-2" {
		t.Errorf("HasLinear = %v, LinearIssues = %+v, %v, want only DX-2", e.HasLinear(), issues, err)
	}
}

func TestSyncIssueStartedMovesTheLinearIssue(t *testing.T) {
	linearCard := func() *store.Task {
		return &store.Task{ID: "a", Number: 1, Issue: &store.Issue{Provider: "linear", ID: "i1", Key: "DX-1", State: "Todo"}}
	}
	for name, tc := range map[string]struct {
		sync    string
		card    *store.Task
		noKey   bool
		updates []string
	}{
		"opted in":         {sync: "1", card: linearCard(), updates: []string{"i1=s-progress"}},
		"not opted in":     {card: linearCard()},
		"no API key":       {sync: "1", card: linearCard(), noKey: true},
		"no issue":         {sync: "1", card: &store.Task{ID: "a"}},
		"another provider": {sync: "1", card: &store.Task{ID: "a", Issue: &store.Issue{Provider: "jira", ID: "i1"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DROVER_LINEAR_SYNC", tc.sync)
			e := bareEngine(t)
			e.state.Tasks = []*store.Task{tc.card}
			var updates func() []string
			if !tc.noKey {
				e.linear, updates = linearServer(t)
			}
			e.syncIssueStarted(tc.card)
			if len(tc.updates) == 0 {
				if updates != nil {
					time.Sleep(50 * time.Millisecond)
					if len(updates()) != 0 {
						t.Errorf("updated Linear anyway: %v", updates())
					}
				}
				return
			}
			moved := func() bool {
				e.mu.Lock()
				defer e.mu.Unlock()
				return hasLog(tc.card, "Linear: moved DX-1 to In Progress")
			}
			for deadline := time.Now().Add(5 * time.Second); !moved() && time.Now().Before(deadline); {
				time.Sleep(20 * time.Millisecond)
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			if !hasLog(tc.card, "Linear: moved DX-1 to In Progress") || tc.card.Issue.State != "In Progress" || !slices.Equal(updates(), tc.updates) {
				t.Errorf("issue state %q, log %v, updates %v", tc.card.Issue.State, tc.card.Log, updates())
			}
		})
	}
}

func TestExecutableIsAResolvedPath(t *testing.T) {
	exe, err := Executable()
	if err != nil || !filepath.IsAbs(exe) {
		t.Fatalf("Executable = %q, %v", exe, err)
	}
	if resolved, _ := filepath.EvalSymlinks(exe); resolved != exe {
		t.Errorf("Executable = %q, want symlinks resolved to %q", exe, resolved)
	}
}

// registerLive lists a conversation held by this process in Claude Code's
// session registry, so it reads as running in another terminal.
func registerLive(t *testing.T, sessionID, status string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "sessions"), 0o755)
	entry, _ := json.Marshal(claude.Live{PID: os.Getpid(), SessionID: sessionID, Status: status})
	os.WriteFile(filepath.Join(dir, "sessions", "1.json"), entry, 0o644)
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestPollExternalMirrorsTheOtherTerminal(t *testing.T) {
	e := bareEngine(t)
	s := &store.Session{Status: store.StatusExternal, Activity: store.ActivityIdle, AgentSessionID: "sid"}
	card := &store.Task{ID: "a", Number: 1, Sessions: map[string]*store.Session{store.MainSession: s}}
	e.state.Tasks = []*store.Task{card, {ID: "b", Number: 2}}

	registerLive(t, "sid", "busy")
	if followUps := e.pollExternal(); len(followUps) != 0 || s.Activity != store.ActivityThinking || s.PID != os.Getpid() {
		t.Fatalf("a busy agent: activity %q, pid %d, %d follow-ups", s.Activity, s.PID, len(followUps))
	}

	registerLive(t, "sid", "idle")
	followUps := e.pollExternal()
	if s.Activity != store.ActivityIdle || len(followUps) != 1 {
		t.Fatalf("the agent's turn ended: activity %q, %d follow-ups, want one reading its reply", s.Activity, len(followUps))
	}
	followUps[0]()

	registerLive(t, "cleared", "idle")
	e.pollExternal()
	if s.AgentSessionID != "cleared" {
		t.Errorf("a /clear in the other terminal left the old id: %q", s.AgentSessionID)
	}
}

func TestPollExternalReleasesAnAgentWhoseProcessIsGone(t *testing.T) {
	for name, tc := range map[string]struct {
		pid      int
		updated  time.Time
		released bool
	}{
		"process gone":              {deadPID(t), time.Now().Add(-time.Hour), true},
		"process alive":             {os.Getpid(), time.Now().Add(-time.Hour), false},
		"just backgrounded, no pid": {0, time.Now(), false},
		"no pid for a while":        {0, time.Now().Add(-time.Hour), true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			e := bareEngine(t)
			s := &store.Session{Status: store.StatusExternal, Activity: store.ActivityThinking, AgentSessionID: "sid", PID: tc.pid, UpdatedAt: tc.updated}
			card := &store.Task{ID: "a", Number: 1, Sessions: map[string]*store.Session{store.MainSession: s}}
			e.state.Tasks = []*store.Task{card}

			followUps := e.pollExternal()
			if tc.released {
				if s.Status != store.StatusSuspended || s.Activity != store.ActivityIdle || len(followUps) != 1 || !hasLog(card, "Agent exited in its own terminal") || !strings.Contains(notice(e), "#1 exited in its terminal") {
					t.Errorf("not released: %+v, %d follow-ups, log %v", s, len(followUps), card.Log)
				}
				return
			}
			if s.Status != store.StatusExternal || len(followUps) != 0 {
				t.Errorf("released too early: %+v, %d follow-ups", s, len(followUps))
			}
		})
	}

	e := bareEngine(t)
	e.state.Tasks = []*store.Task{{ID: "a"}}
	if followUps := e.pollExternal(); followUps != nil {
		t.Errorf("a board without external agents polled %d follow-ups", len(followUps))
	}
}

func TestExternalCheckRefusesAConversationRunningElsewhere(t *testing.T) {
	registerLive(t, "sid", "idle")
	e := bareEngine(t)
	imported := func(status store.SessionStatus, id string) *store.Task {
		return &store.Task{ID: "a", Number: 3, Sessions: map[string]*store.Session{store.MainSession: {Imported: true, Status: status, AgentSessionID: id}}}
	}

	card := imported(store.StatusSuspended, "sid")
	var ext *ExternalError
	if err := e.externalCheck(card); !errors.As(err, &ext) || ext.Number != 3 || ext.PID != os.Getpid() || card.Sessions[store.MainSession].Status != store.StatusExternal || !hasLog(card, "running in another terminal") {
		t.Errorf("externalCheck = %v, session %+v", err, card.Sessions[store.MainSession])
	}
	if err := e.externalCheck(card); !errors.As(err, &ext) {
		t.Errorf("a second check = %v", err)
	}
	if n := strings.Count(logText(card), "running in another terminal"); n != 1 {
		t.Errorf("the discovery was logged %d times", n)
	}

	for name, c := range map[string]*store.Task{
		"not in the registry":        imported(store.StatusSuspended, "other"),
		"running on the board":       imported(store.StatusRunning, "sid"),
		"never imported":             {ID: "b", Sessions: map[string]*store.Session{store.MainSession: {Status: store.StatusSuspended, AgentSessionID: "sid"}}},
		"external whose pid is gone": {ID: "c", Sessions: map[string]*store.Session{store.MainSession: {Imported: true, Status: store.StatusExternal, AgentSessionID: "other", PID: deadPID(t)}}},
	} {
		if err := e.externalCheck(c); err != nil {
			t.Errorf("%s: externalCheck = %v", name, err)
		}
	}
}

// reviewSearchGh fakes gh for ReviewPRs: you are "me" in repo o/r, and the
// searches answer with requested.json or reviewed.json from dir. Every search
// is recorded in dir/args.
func reviewSearchGh(t *testing.T) (dir string) {
	t.Helper()
	dir, bin := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(bin, "gh"), []byte(`#!/bin/sh
case "$1 $2" in
  "api user") echo me;;
  "repo view") echo o/r;;
  "search prs")
    echo "$@" >> "$FAKE_DIR/args"
    case "$*" in
      *review-requested*) cat "$FAKE_DIR/requested.json";;
      *) cat "$FAKE_DIR/reviewed.json";;
    esac;;
esac
`), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	return dir
}

func prJSON(n int, author string) string {
	return fmt.Sprintf(`{"number":%d,"title":"Change %d","url":"https://github.com/o/r/pull/%d","author":{"login":%q},"isDraft":false}`, n, n, n, author)
}

func TestReviewPRsFiltersTheSearches(t *testing.T) {
	dir := reviewSearchGh(t)
	os.WriteFile(filepath.Join(dir, "requested.json"), []byte("["+prJSON(1, "them")+","+prJSON(2, "Me")+","+prJSON(3, "them")+","+prJSON(4, "them")+"]"), 0o644)
	os.WriteFile(filepath.Join(dir, "reviewed.json"), []byte("["+prJSON(3, "them")+","+prJSON(5, "me")+","+prJSON(6, "them")+"]"), 0o644)
	e := bareEngine(t)
	e.board.ReviewRepos = []string{"o/foundation", "O/R"}
	e.state.Tasks = []*store.Task{{ID: "a", PR: &store.PR{URL: "https://github.com/o/r/pull/4", Review: true}}}

	got, err := e.ReviewPRs()
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, p := range got {
		summary = append(summary, fmt.Sprintf("%d:%v", p.Number, p.Reviewed))
	}
	// 2 and 5 are yours, 4 is on the board, and 3 awaits you again.
	if want := []string{"1:false", "3:false", "6:true"}; !slices.Equal(summary, want) {
		t.Errorf("ReviewPRs = %v, want %v", summary, want)
	}
	args := readFile(filepath.Join(dir, "args"))
	for _, want := range []string{"user-review-requested:@me", "--reviewed-by=@me", "--repo o/r --repo o/foundation --state open"} {
		if !strings.Contains(args, want) {
			t.Errorf("searches %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "--review-requested=@me") || strings.Contains(args, "O/R") {
		t.Errorf("searches %q include team requests or a repo twice", args)
	}

	os.Remove(filepath.Join(dir, "args"))
	e.board.TeamReviewRequests = true
	if _, err := e.ReviewPRs(); err != nil {
		t.Fatal(err)
	}
	if args := readFile(filepath.Join(dir, "args")); !strings.Contains(args, "--review-requested=@me") || strings.Contains(args, "user-review-requested") {
		t.Errorf("teamReviewRequests: searches %q", args)
	}
}

func TestReviewPRByURL(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "gh"), []byte(`#!/bin/sh
case "$1 $3" in
  "api "*) echo me;;
  *"/pull/1") echo '{"title":"Theirs","state":"OPEN","author":{"login":"them"}}';;
  *"/pull/2") echo '{"title":"Reviewed","state":"OPEN","author":{"login":"them"},"latestReviews":[{"author":{"login":"me"},"state":"APPROVED","submittedAt":"2026-10-19T10:00:00Z"}]}';;
  *"/pull/3") echo '{"title":"Mine","state":"OPEN","author":{"login":"me"}}';;
  *"/pull/4") echo '{"title":"Gone","state":"MERGED","author":{"login":"them"}}';;
esac
`), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e := bareEngine(t)

	p, err := e.ReviewPRByURL(" https://github.com/o/r/pull/1 ")
	if err != nil || p.Number != 1 || p.Title != "Theirs" || p.Author.Login != "them" || p.URL != "https://github.com/o/r/pull/1" || p.Reviewed {
		t.Errorf("an open PR: %+v, %v", p, err)
	}
	if p, err := e.ReviewPRByURL("https://github.com/o/r/pull/2"); err != nil || !p.Reviewed {
		t.Errorf("a PR you reviewed: %+v, %v", p, err)
	}
	for ref, want := range map[string]string{
		"https://github.com/o/r/pull/3": "PR #3 is yours",
		"https://github.com/o/r/pull/4": "PR #4 is merged",
		"o/r#5":                         "not a pull request URL",
	} {
		if _, err := e.ReviewPRByURL(ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", ref, err, want)
		}
	}
}

func TestImportReviewsLandsWithoutAnAgent(t *testing.T) {
	pr := func(n int, reviewed bool) ReviewPR {
		return ReviewPR{Found: gh.Found{Number: n, Title: fmt.Sprintf("Change %d", n), URL: fmt.Sprintf("https://github.com/o/r/pull/%d", n), Author: gh.Author{Login: "them"}}, Reviewed: reviewed}
	}
	for _, tc := range []struct {
		name                string
		board               *config.Board
		requested, reviewed string
	}{
		{"default board", config.Default(), "review-requested", "waiting-on-author"},
		{"custom board without the targets", &config.Board{Columns: []config.Column{
			{ID: "todo", Name: "Todo", AutoSpawn: true},
			{ID: "reviewing", Name: "Reviewing", Lane: config.LaneReview, AutoSpawn: true},
			{ID: "parked", Name: "Parked", Lane: config.LaneReview},
		}}, "parked", "parked"},
		{"custom board without a lane", &config.Board{Columns: []config.Column{
			{ID: "todo", Name: "Todo"}, {ID: "done", Name: "Done", Role: "done"},
		}}, "todo", "todo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := bareEngine(t)
			e.board = tc.board
			added := e.ImportReviews([]ReviewPR{pr(1, false), pr(2, true), pr(1, false)})
			if len(added) != 2 || len(e.state.Tasks) != 2 {
				t.Fatalf("added %d cards, board has %d", len(added), len(e.state.Tasks))
			}
			asked, done := e.state.Tasks[0], e.state.Tasks[1]
			if asked.ColumnID != tc.requested || done.ColumnID != tc.reviewed {
				t.Errorf("columns: requested %q, reviewed %q", asked.ColumnID, done.ColumnID)
			}
			if asked.Title != "Review: Change 1" || asked.Prompt != "/code-review https://github.com/o/r/pull/1" || !hasLog(asked, "Imported PR #1 to review") {
				t.Errorf("card: %q %q %v", asked.Title, asked.Prompt, asked.Log)
			}
			for _, c := range added {
				if !c.PR.IsReview() || c.PR.State != "" || c.PR.Author != "them" || c.PRMovePending || len(c.Sessions) != 0 {
					t.Errorf("#%d: PR %+v, move pending %v, %d sessions", c.Number, c.PR, c.PRMovePending, len(c.Sessions))
				}
			}
			if again := e.ImportReviews([]ReviewPR{pr(2, true)}); len(again) != 0 {
				t.Error("a PR on the board was imported again")
			}
		})
	}
}

func TestImportedReviewFirstRefreshOnlyRecords(t *testing.T) {
	e := bareEngine(t)
	at, _ := time.Parse(time.RFC3339, "2026-10-19T10:00:00Z")
	info := gh.PR{State: "OPEN", HeadSHA: "bbb", Author: gh.Author{Login: "them"},
		LatestReviews: []gh.Review{{Author: gh.Author{Login: "me"}, State: "APPROVED", SubmittedAt: at, Commit: struct {
			OID string `json:"oid"`
		}{"aaa"}}}}
	e.ImportReviews([]ReviewPR{{Found: gh.Found{Number: 7, URL: "https://github.com/o/r/pull/7"}, Reviewed: true}})
	card := e.state.Tasks[0]

	if step := e.reviewStep(card, card, card.PR.State, card.PR.HeadSHA, info, "me"); step.to.ID != "" || card.PR.ReviewedSHA != "aaa" || card.PR.MyReview != "APPROVED" {
		t.Fatalf("first refresh: move to %q, PR %+v", step.to.ID, card.PR)
	}
	card.PR.State, card.PR.HeadSHA = info.State, info.HeadSHA
	if step := e.reviewStep(card, card, "OPEN", "bbb", info, "me"); step.to.ID != "" {
		t.Errorf("an unchanged PR moves the card to %q", step.to.ID)
	}
	info.HeadSHA = "ccc"
	if step := e.reviewStep(card, card, "OPEN", "bbb", info, "me"); step.to.ID != "reviewing" {
		t.Errorf("new commits since your review move the card to %q, want reviewing", step.to.ID)
	}
	if v := e.vars(card, groupContext{}); v["prReviewedSha"] != "aaa" {
		t.Errorf("prReviewedSha = %q", v["prReviewedSha"])
	}
}

func TestReReviewWaitsForAReviewOfYours(t *testing.T) {
	e := bareEngine(t)
	e.stopping = t.Context()
	col, _, _ := e.board.Column("reviewing")
	row := col.Automations.OnEnter[0]
	card := &store.Task{ID: "a", Number: 1, PR: &store.PR{URL: "https://github.com/o/r/pull/7", Number: 7, Review: true}}
	e.state.Tasks = []*store.Task{card}
	if e.shouldRun(card, row, groupContext{to: col}) {
		t.Error("an imported PR you haven't reviewed is asked for a re-review")
	}
	card.PR.ReviewedSHA = "aaa"
	if !e.shouldRun(card, row, groupContext{to: col}) {
		t.Error("a PR you reviewed isn't asked for a re-review")
	}
}

const importReviewBoard = `{
  "columns": [
    {"id":"planning","name":"Planning","autoSpawn":true},
    {"id": "requested", "name": "Review requested", "lane": "review"},
    {"id": "reviewing", "name": "Reviewing", "lane": "review", "autoSpawn": true},
    {"id": "triage", "name": "Triage", "lane": "review", "autoSpawn": true},
    {"id": "waiting", "name": "Waiting on author", "lane": "review"},
    {"id": "done", "name": "Done", "role": "done"}
  ],
  "reviewFlow": {"requested": "requested", "started": "triage", "reviewed": "waiting", "updated": "reviewing", "done": "done"}
}`

func TestImportedReviewStartsOnlyWhenYouStartIt(t *testing.T) {
	r := newReviewRig(t, "them", importReviewBoard)
	e := r.e
	e.ImportReviews([]ReviewPR{{Found: gh.Found{Number: 7, Title: "Their change", URL: "https://github.com/o/r/pull/7", Author: gh.Author{Login: "them"}}}})
	waitForAll(t, e, "the first refresh", func(tasks []*store.Task) bool { return reviewCard(tasks).PR.State == "OPEN" })
	time.Sleep(600 * time.Millisecond)
	c := reviewCard(e.Snapshot().Tasks)
	if c.ColumnID != "requested" || len(c.Sessions) != 0 || readFile(r.log) != "" {
		t.Fatalf("importing started something: column %s, %d sessions, claude log %q", c.ColumnID, len(c.Sessions), readFile(r.log))
	}

	if err := e.Start(c.ID); err != nil {
		t.Fatal(err)
	}
	waitForAll(t, e, "the first review turn to move the card to Triage", func(tasks []*store.Task) bool {
		return reviewCard(tasks).ColumnID == "triage"
	})
	if log := readFile(r.log); !strings.Contains(log, "--\n/code-review https://github.com/o/r/pull/7\n") {
		t.Errorf("the agent didn't open with the review command:\n%s", log)
	}
}

func TestImportedReviewedPRReturnsOnNewCommits(t *testing.T) {
	r := newReviewRig(t, "them", importReviewBoard)
	e := r.e
	mine := `{"author":{"login":"me"},"state":"COMMENTED","submittedAt":"2026-10-19T10:00:00Z","commit":{"oid":"aaa"}}`
	r.gh("OPEN", "aaa", mine)
	e.ImportReviews([]ReviewPR{{Found: gh.Found{Number: 7, URL: "https://github.com/o/r/pull/7"}, Reviewed: true}})
	waitForAll(t, e, "the first refresh to record your review", func(tasks []*store.Task) bool {
		return reviewCard(tasks).PR.ReviewedSHA == "aaa"
	})
	if c := reviewCard(e.Snapshot().Tasks); c.ColumnID != "waiting" || hasLog(c, "You reviewed") || len(c.Sessions) != 0 {
		t.Fatalf("the review already there moved or started the card: column %s, log %v", c.ColumnID, c.Log)
	}
	r.gh("OPEN", "bbb", mine)
	waitForAll(t, e, "new commits to move the card to Reviewing", func(tasks []*store.Task) bool {
		return reviewCard(tasks).ColumnID == "reviewing"
	})
}

func TestSetReviewLevel(t *testing.T) {
	e := bareEngine(t)
	e.ImportReviews([]ReviewPR{{Found: gh.Found{Number: 7, URL: "https://github.com/o/r/pull/7"}}})
	card := e.state.Tasks[0]
	if !UnleveledReview(card) {
		t.Fatalf("an imported review has no level: %q", card.Prompt)
	}
	if err := e.SetReviewLevel(card.ID, "huge"); err == nil {
		t.Error("an unknown level was accepted")
	}
	if err := e.SetReviewLevel(card.ID, "max"); err != nil || card.Prompt != "/code-review max https://github.com/o/r/pull/7" || !hasLog(card, "Review level: max") {
		t.Fatalf("SetReviewLevel: %v, prompt %q", err, card.Prompt)
	}
	if UnleveledReview(card) || e.SetReviewLevel(card.ID, "low") == nil {
		t.Error("a review with a level asks for one again")
	}

	for name, c := range map[string]*store.Task{
		"started":    {PR: &store.PR{Review: true}, Prompt: "/code-review https://github.com/o/r/pull/1", Sessions: map[string]*store.Session{store.MainSession: {}}},
		"not review": {PR: &store.PR{}, Prompt: "/code-review https://github.com/o/r/pull/1"},
		"own prompt": {PR: &store.PR{Review: true}, Prompt: "look at https://github.com/o/r/pull/1"},
	} {
		if UnleveledReview(c) {
			t.Errorf("%s: asks for a level", name)
		}
	}
}

func TestApprovedReviewInDoneComesBack(t *testing.T) {
	e := bareEngine(t)
	at, _ := time.Parse(time.RFC3339, "2026-10-19T10:00:00Z")
	approved := gh.PR{State: "OPEN", HeadSHA: "aaa", LatestReviews: []gh.Review{{Author: gh.Author{Login: "me"}, State: "APPROVED", SubmittedAt: at}}}
	e.ImportReviews([]ReviewPR{{Found: gh.Found{Number: 7, URL: "https://github.com/o/r/pull/7"}}})
	card := e.state.Tasks[0]
	step := func(info gh.PR) string {
		prev, prevHead := card.PR.State, card.PR.HeadSHA
		card.PR.State, card.PR.HeadSHA = info.State, info.HeadSHA
		to := e.reviewStep(card, card, prev, prevHead, info, "me").to.ID
		if to != "" {
			card.ColumnID = to
		}
		return to
	}

	asked := gh.PR{State: "OPEN", HeadSHA: "aaa", ReviewRequests: []gh.Author{{Login: "me"}}}
	if to := step(asked); to != "" || !card.PR.ReviewRequested {
		t.Fatalf("first check: moved to %q, requested %v", to, card.PR.ReviewRequested)
	}
	if to := step(approved); to != "review-done" || !reviewedOpen(card) {
		t.Fatalf("approving moved the card to %q, tracked %v", to, reviewedOpen(card))
	}

	reasked := approved
	reasked.ReviewRequests = asked.ReviewRequests
	if to := step(reasked); to != "review-requested" || !hasLog(card, "Your review was requested again on PR #7") {
		t.Errorf("a new review request moved the card to %q", to)
	}
	if to := step(reasked); to != "" {
		t.Errorf("the same request moved the card again, to %q", to)
	}

	card.ColumnID = "review-done"
	pushed := approved
	pushed.HeadSHA = "bbb"
	if to := step(pushed); to != "reviewing" {
		t.Errorf("new commits moved the approved card to %q, want reviewing", to)
	}

	for name, c := range map[string]*store.Task{
		"never reviewed": {PR: &store.PR{Review: true, State: "OPEN"}},
		"archived":       {PR: &store.PR{Review: true, State: "OPEN", ReviewedSHA: "aaa"}, Archived: true},
		"merged":         {PR: &store.PR{Review: true, State: "MERGED", ReviewedSHA: "aaa"}},
		"your own PR":    {PR: &store.PR{State: "OPEN", ReviewedSHA: "aaa"}},
	} {
		if reviewedOpen(c) {
			t.Errorf("%s: tracked in Done", name)
		}
	}
}

func TestApprovedReviewIsPolledFromDone(t *testing.T) {
	r := newReviewRig(t, "them", strings.Replace(importReviewBoard, `"reviewed": "waiting"`, `"reviewed": "waiting", "approved": "done"`, 1))
	e := r.e
	e.ImportReviews([]ReviewPR{{Found: gh.Found{Number: 7, URL: "https://github.com/o/r/pull/7"}}})
	waitForAll(t, e, "the first refresh", func(tasks []*store.Task) bool { return reviewCard(tasks).PR.State == "OPEN" })

	approval := `{"author":{"login":"me"},"state":"APPROVED","submittedAt":"2026-10-19T10:00:00Z","commit":{"oid":"aaa"}}`
	r.gh("OPEN", "aaa", approval)
	waitForAll(t, e, "your approval to move the card to Done", func(tasks []*store.Task) bool { return reviewCard(tasks).ColumnID == "done" })
	r.gh("OPEN", "bbb", approval)
	waitForAll(t, e, "new commits to bring the card back to Reviewing", func(tasks []*store.Task) bool { return reviewCard(tasks).ColumnID == "reviewing" })
}
