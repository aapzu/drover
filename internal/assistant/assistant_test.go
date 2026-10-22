package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

var realHasTranscript = hasTranscript

func init() {
	hasTranscript = func(string, string) (bool, error) { return true, nil }
	hasSession = func(string) bool { return false }
}

type panel struct {
	live  bool
	calls [][]string
	dirs  []string
	stops int
}

func fakePanel(t *testing.T) *panel {
	t.Helper()
	p := &panel{}
	origHas, origNew, origStop := hasSession, newSession, stopSession
	t.Cleanup(func() { hasSession, newSession, stopSession = origHas, origNew, origStop })
	hasSession = func(string) bool { return p.live }
	newSession = func(name, dir string, argv []string, env map[string]string) error {
		p.calls, p.dirs, p.live = append(p.calls, argv), append(p.dirs, dir), true
		return nil
	}
	stopSession = func(string) { p.live, p.stops = false, p.stops+1 }
	return p
}

type call struct {
	argv  []string
	stdin string
	dir   string
}

func fakeClaude(t *testing.T, out string, err error) *[]call {
	t.Helper()
	var calls []call
	orig := runClaude
	t.Cleanup(func() { runClaude = orig })
	runClaude = func(_ context.Context, dir string, argv []string, stdin string) (string, error) {
		calls = append(calls, call{argv, stdin, dir})
		return out, err
	}
	return &calls
}

func flagValue(argv []string, flag string) string {
	if i := slices.Index(argv, flag); i >= 0 && i+1 < len(argv) {
		return argv[i+1]
	}
	return ""
}

func TestAskStartsThenResumesOneConversation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	calls := fakeClaude(t, " #2 is in Planning\n", nil)

	answer, err := Ask(context.Background(), root, "/bin/drover", "where is #2?", false)
	if err != nil || answer != "#2 is in Planning" {
		t.Fatalf("Ask = %q, %v", answer, err)
	}
	if _, err := Ask(context.Background(), root, "/bin/drover", "and #3?", false); err != nil {
		t.Fatal(err)
	}

	first, second := (*calls)[0], (*calls)[1]
	id := flagValue(first.argv, "--session-id")
	if id == "" || slices.Contains(first.argv, "--resume") {
		t.Fatalf("first call should start a session: %q", first.argv)
	}
	if flagValue(second.argv, "--resume") != id || slices.Contains(second.argv, "--session-id") {
		t.Errorf("second call should resume %s: %q", id, second.argv)
	}
	if first.stdin != "where is #2?" || first.dir != root {
		t.Errorf("question and directory = %q in %q", first.stdin, first.dir)
	}
}

func TestAskForgetsAFailedFirstQuestion(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	calls := fakeClaude(t, "", errors.New("boom"))

	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err == nil {
		t.Fatal("expected the failure to surface")
	}
	runClaude = func(_ context.Context, dir string, argv []string, stdin string) (string, error) {
		*calls = append(*calls, call{argv, stdin, dir})
		return "ok", nil
	}
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[1]; slices.Contains(got.argv, "--resume") || flagValue(got.argv, "--session-id") == "" {
		t.Errorf("a first question that failed must not be resumed: %q", got.argv)
	}
	if flagValue((*calls)[0].argv, "--session-id") == flagValue((*calls)[1].argv, "--session-id") {
		t.Error("the retry reused the session id of the failed attempt")
	}
}

func TestResetStartsANewConversation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	calls := fakeClaude(t, "ok", nil)

	for range 2 {
		if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
			t.Fatal(err)
		}
		if err := Reset(root); err != nil {
			t.Fatal(err)
		}
	}
	if err := Reset(root); err != nil {
		t.Errorf("resetting without a conversation: %v", err)
	}
	if len(*calls) != 2 || slices.Contains((*calls)[1].argv, "--resume") {
		t.Errorf("the question after a reset should start a session: %q", *calls)
	}
}

func TestArgvOnlyAllowsTheBoardCLI(t *testing.T) {
	exe := "/Users/me/.local/bin/drover"
	args := argv(exe, "abc", false, systemPrompt(exe, "/work/repo", &config.Board{}))

	if flagValue(args, "--tools") != "Bash" {
		t.Errorf("tools = %q", flagValue(args, "--tools"))
	}
	allowed := args[slices.Index(args, "--allowedTools")+1 : slices.Index(args, "--disallowedTools")]
	for _, want := range []string{
		"Bash('" + exe + "' move *)",
		"Bash(" + exe + " list)",
		"Bash(" + exe + " rm *)",
	} {
		if !slices.Contains(allowed, want) {
			t.Errorf("allowed tools lack %q: %q", want, allowed)
		}
	}
	for _, rule := range allowed {
		if !strings.Contains(rule, exe) {
			t.Errorf("rule %q is not about the drover binary", rule)
		}
	}
	denied := args[slices.Index(args, "--disallowedTools")+1 : slices.Index(args, "--session-id")]
	for _, want := range []string{"Bash(" + exe + " rm *-force*)", "Bash('" + exe + "' rm *-force*)"} {
		if !slices.Contains(denied, want) {
			t.Errorf("forced removal isn't denied by %q: %q", want, denied)
		}
	}
	for _, rule := range allowed {
		if strings.Contains(rule, " rm") && !strings.HasSuffix(rule, " rm)") && !strings.HasSuffix(rule, " rm *)") {
			t.Errorf("rm rule %q allows more than rm", rule)
		}
	}
	prompt := flagValue(args, "--append-system-prompt")
	for _, want := range []string{"'" + exe + "' list --json", "never pass --force", "wait for the user to agree"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
}

func TestCorruptSessionFileStartsANewConversation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	fakeClaude(t, "ok", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	if id, ok := load(root); !ok {
		t.Fatalf("load = %q, %v", id, ok)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(root), sessionFile), []byte("not-a-uuid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := load(root); ok {
		t.Error("a corrupt session file should start a new conversation")
	}
}

func TestPromptNamesTheRepoAndItsColumns(t *testing.T) {
	board := &config.Board{
		DefaultBaseBranch: "develop",
		Columns: []config.Column{
			{ID: "todo", Name: "Sessions", Role: "todo"},
			{ID: "exec", Name: "Executing", AutoSpawn: true, PermissionMode: "auto"},
			{ID: "wait", Name: "Waiting"},
			{ID: "done", Name: "Done", Role: "done"},
		},
	}
	got := systemPrompt("/bin/drover", "/work/my-app", board)
	for _, want := range []string{
		"my-app at /work/my-app",
		"branch from develop",
		"- Sessions (id todo): no agent; a card moved here loses its worktree",
		"- Executing (id exec): an agent works on the card in auto mode",
		"- Waiting (id wait): no agent runs here",
		"- Done (id done): finished cards",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Sessions (id") > strings.Index(got, "Done (id") {
		t.Error("columns are not in board order")
	}
}

func TestAskGivesClaudeTheBoardAndAModel(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(`{"columns":[{"id":"todo","name":"Backlog","role":"todo"},{"id":"qa","name":"QA lane","autoSpawn":true}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	orig := runClaude
	t.Cleanup(func() { runClaude = orig })
	var got []string
	runClaude = func(ctx context.Context, dir string, argv []string, stdin string) (string, error) {
		got = argv
		deadline, _ = ctx.Deadline()
		return "ok", nil
	}
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	if flagValue(got, "--model") != model || !strings.Contains(flagValue(got, "--append-system-prompt"), "QA lane (id qa)") {
		t.Errorf("argv = %q", got)
	}
	if left := time.Until(deadline); left <= 0 || left > askTimeout {
		t.Errorf("deadline in %s, want within %s", left, askTimeout)
	}
}

func TestFreshForgetsTheConversationOnlyOnceTheQuestionCanRun(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	calls := fakeClaude(t, "ok", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", true); err != nil {
		t.Fatal(err)
	}
	if slices.Contains((*calls)[1].argv, "--resume") || flagValue((*calls)[0].argv, "--session-id") == flagValue((*calls)[1].argv, "--session-id") {
		t.Errorf("a fresh question must start a new session: %q", (*calls)[1].argv)
	}

	os.WriteFile(filepath.Join(root, config.FileName), []byte("{"), 0o644)
	before, _ := load(root)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", true); err == nil {
		t.Fatal("expected the broken board to fail the question")
	}
	if after, ok := load(root); !ok || after != before {
		t.Error("a question that couldn't run still forgot the conversation")
	}
}

func TestAskReportsWhatWentWrong(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()

	fakeClaude(t, "  \n", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Errorf("blank output: %v", err)
	}
	if _, ok := load(root); ok {
		t.Error("a blank answer started a conversation")
	}

	fakeClaude(t, "ok", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	fakeClaude(t, "", errors.New("exit status 1"))
	_, err := Ask(context.Background(), root, "/bin/drover", "q", false)
	if err == nil || !strings.Contains(err.Error(), "drover ask --new") {
		t.Errorf("a failed resume should say how to start over: %v", err)
	}
}

func TestAnswerSurvivesAFailedSave(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store.Dir(root), sessionFile+".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeClaude(t, "moved it", nil)
	answer, err := Ask(context.Background(), root, "/bin/drover", "q", false)
	if answer != "moved it" || err == nil || !strings.Contains(err.Error(), "next question starts a new one") {
		t.Errorf("Ask = %q, %v; want the answer with a warning", answer, err)
	}
}

func TestQuestionsTakeTurns(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	orig := runClaude
	t.Cleanup(func() { runClaude = orig })
	entered, release := make(chan []string, 2), make(chan struct{})
	runClaude = func(_ context.Context, _ string, argv []string, _ string) (string, error) {
		entered <- argv
		<-release
		return "ok", nil
	}
	done := make(chan error, 3)
	go func() { _, err := Ask(context.Background(), root, "/bin/drover", "one", false); done <- err }()
	first := <-entered
	go func() { _, err := Ask(context.Background(), root, "/bin/drover", "two", false); done <- err }()
	go func() { done <- Reset(root) }()

	select {
	case <-entered:
		t.Fatal("a second question started while the first was running")
	case <-time.After(200 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var second []string
	select {
	case second = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the second question never started")
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if slices.Contains(first, "--resume") {
		t.Fatalf("the first question resumed something: %q", first)
	}
	if id := flagValue(first, "--session-id"); id == "" || (slices.Contains(second, "--resume") && flagValue(second, "--resume") != id) {
		t.Errorf("the second question didn't continue the first: %q then %q", first, second)
	}
}

func TestRunClaudeExplainsFailures(t *testing.T) {
	run := runClaude
	for name, tc := range map[string]struct {
		argv []string
		want string
	}{
		"stderr":    {[]string{"sh", "-c", "echo bad token >&2; exit 3"}, "bad token"},
		"stdout":    {[]string{"sh", "-c", "echo Invalid API key; exit 1"}, "Invalid API key"},
		"not found": {[]string{"drover-no-such-binary"}, "on PATH"},
	} {
		_, err := run(context.Background(), t.TempDir(), tc.argv, "")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", name, err, tc.want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := run(ctx, t.TempDir(), []string{"sleep", "30"}, ""); err == nil || !strings.Contains(err.Error(), "no answer after") {
		t.Errorf("timeout: error = %v", err)
	}
}

func TestPanelRunsTheInteractiveTUIUnderTheSameRules(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	hasTranscript = func(string, string) (bool, error) { return false, nil }
	t.Cleanup(func() { hasTranscript = func(string, string) (bool, error) { return true, nil } })
	p := fakePanel(t)
	exe := "/bin/drover"

	if err := Start(root, exe, &config.Board{}); err != nil {
		t.Fatal(err)
	}
	if err := Start(root, exe, &config.Board{}); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || p.dirs[0] != root {
		t.Fatalf("Start twice should open one session in the repo: %q in %q", p.calls, p.dirs)
	}
	args := p.calls[0]
	if args[0] != "claude" || slices.Contains(args, "-p") {
		t.Errorf("the panel should run the interactive claude: %q", args)
	}
	if flagValue(args, "--permission-mode") != "dontAsk" || flagValue(args, "--tools") != "Bash" {
		t.Errorf("permission mode %q, tools %q", flagValue(args, "--permission-mode"), flagValue(args, "--tools"))
	}
	if !slices.Contains(args, "Bash("+exe+" list)") || !slices.Contains(args, "Bash("+exe+" rm *-force*)") {
		t.Errorf("the panel lacks the drover-only rules: %q", args)
	}
	id := flagValue(args, "--session-id")
	if saved, ok := load(root); !ok || saved != id {
		t.Errorf("saved id = %q, %v; want %q", saved, ok, id)
	}
}

func TestPanelResumesOnceTheConversationHasATranscript(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	p := fakePanel(t)
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"
	if err := save(root, id); err != nil {
		t.Fatal(err)
	}
	if err := Start(root, "/bin/drover", &config.Board{}); err != nil {
		t.Fatal(err)
	}
	if flagValue(p.calls[0], "--resume") != id || slices.Contains(p.calls[0], "--session-id") {
		t.Errorf("should resume %s: %q", id, p.calls[0])
	}
}

func TestAskDoesNotResumeAConversationThatNeverGotAMessage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"
	if err := save(root, id); err != nil {
		t.Fatal(err)
	}
	hasTranscript = func(string, string) (bool, error) { return false, nil }
	t.Cleanup(func() { hasTranscript = func(string, string) (bool, error) { return true, nil } })
	calls := fakeClaude(t, "ok", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	if got := (*calls)[0].argv; slices.Contains(got, "--resume") {
		t.Errorf("there is nothing to resume yet: %q", got)
	}
}

func TestAskAndResetRefuseWhileThePanelRuns(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	p := fakePanel(t)
	calls := fakeClaude(t, "ok", nil)
	p.live = true

	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); !errors.Is(err, errPanelOpen) {
		t.Errorf("Ask = %v", err)
	}
	if err := Reset(root); !errors.Is(err, errPanelOpen) {
		t.Errorf("Reset = %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("claude ran %d times beside the panel", len(*calls))
	}
	Stop(root)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Errorf("Ask after the panel closed: %v", err)
	}
}

func TestRestartStartsAFreshConversation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	p := fakePanel(t)
	if err := Start(root, "/bin/drover", &config.Board{}); err != nil {
		t.Fatal(err)
	}
	if err := Restart(root, "/bin/drover", &config.Board{}); err != nil {
		t.Fatal(err)
	}
	if p.stops != 1 || len(p.calls) != 2 {
		t.Fatalf("stops = %d, sessions = %d", p.stops, len(p.calls))
	}
	if flagValue(p.calls[0], "--session-id") == flagValue(p.calls[1], "--session-id") {
		t.Error("the restarted panel continued the old conversation")
	}
}

func TestSessionNameIsSafeAndPerProject(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, b := SessionName("/work/a/my repo.x"), SessionName("/work/b/my repo.x")
	if a == b {
		t.Errorf("two repos named alike share %q", a)
	}
	if strings.ContainsAny(a, " .:") || !strings.HasSuffix(a, "-assistant") {
		t.Errorf("session name %q", a)
	}
}

func TestRealTranscriptLookupFollowsClaudesProjectDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	hasTranscript = realHasTranscript
	t.Cleanup(func() { hasTranscript = func(string, string) (bool, error) { return true, nil } })
	root := filepath.Join(t.TempDir(), "repo")
	id := "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := save(root, id); err != nil {
		t.Fatal(err)
	}

	if _, resume, _ := conversation(root); resume {
		t.Error("a conversation without a transcript was resumed")
	}
	path := claude.TranscriptPath(id, root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, resume, _ := conversation(root); !resume || got != id {
		t.Errorf("conversation = %q, %v; want %s resumed", got, resume, id)
	}
}

func TestPanelKeepsTheSavedIdUntilItHasATranscript(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	p := fakePanel(t)
	hasTranscript = func(string, string) (bool, error) { return false, nil }
	t.Cleanup(func() { hasTranscript = func(string, string) (bool, error) { return true, nil } })
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"
	if err := save(root, id); err != nil {
		t.Fatal(err)
	}
	if err := Start(root, "/bin/drover", &config.Board{}); err != nil {
		t.Fatal(err)
	}
	if flagValue(p.calls[0], "--session-id") != id || slices.Contains(p.calls[0], "--resume") {
		t.Errorf("should start under the saved id %s: %q", id, p.calls[0])
	}
	if saved, _ := load(root); saved != id {
		t.Errorf("saved id changed to %q", saved)
	}
}

func TestStopWaitsForAStartInProgress(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	p := fakePanel(t)
	unlock, err := lock(root)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { Stop(root); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop ran while a Start held the conversation")
	case <-time.After(100 * time.Millisecond):
	}
	p.live = true
	unlock()
	<-stopped
	if p.live || p.stops != 1 {
		t.Errorf("live %v, stops %d; Stop should end the session the Start made", p.live, p.stops)
	}
}

func TestSessionIDIsTheSavedConversation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	if id := SessionID(root); id != "" {
		t.Errorf("SessionID before any conversation = %q", id)
	}
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"
	if err := save(root, id); err != nil {
		t.Fatal(err)
	}
	if got := SessionID(root); got != id {
		t.Errorf("SessionID = %q, want %q", got, id)
	}
}

func TestStartAndRestartDoNotWaitForAQuestion(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	p := fakePanel(t)
	unlock, err := lock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := Start(root, "/bin/drover", &config.Board{}); !errors.Is(err, errBusy) {
		t.Errorf("Start = %v", err)
	}
	if err := Restart(root, "/bin/drover", &config.Board{}); !errors.Is(err, errBusy) {
		t.Errorf("Restart = %v", err)
	}
	if len(p.calls) != 0 || p.stops != 0 {
		t.Errorf("a busy conversation was touched: %d sessions, %d stops", len(p.calls), p.stops)
	}
}

func TestStopDoesNotHangBehindALongQuestion(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	p := fakePanel(t)
	p.live = true
	unlock, err := lock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	if err := Stop(root); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > stopWait+time.Second || p.live {
		t.Errorf("Stop took %v, live %v", took, p.live)
	}
}

func TestStopReportsASessionThatWontDie(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	p := fakePanel(t)
	p.live = true
	stopSession = func(string) {}
	if err := Stop(root); err == nil || !strings.Contains(err.Error(), "kill-session") {
		t.Errorf("Stop = %v; want the command that ends it", err)
	}
}

func TestUnreadableConversationStateIsAnErrorNotANewConversation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(store.Dir(root), sessionFile), 0o755); err != nil {
		t.Fatal(err)
	}
	p := fakePanel(t)
	calls := fakeClaude(t, "ok", nil)
	if err := Start(root, "/bin/drover", &config.Board{}); err == nil || len(p.calls) != 0 {
		t.Errorf("Start = %v with %d sessions", err, len(p.calls))
	}
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err == nil || len(*calls) != 0 {
		t.Errorf("Ask = %v with %d runs", err, len(*calls))
	}

	hasTranscript = func(string, string) (bool, error) { return false, errors.New("permission denied") }
	t.Cleanup(func() { hasTranscript = func(string, string) (bool, error) { return true, nil } })
	root = filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := save(root, "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"); err != nil {
		t.Fatal(err)
	}
	if err := Start(root, "/bin/drover", &config.Board{}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Start = %v; a transcript that can't be checked shouldn't pass for a missing one", err)
	}
}

func TestSaveLeavesNoPartialFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"
	if err := save(root, id); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(store.Dir(root))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left %s behind", e.Name())
		}
	}
	if got, _ := load(root); got != id {
		t.Errorf("load = %q", got)
	}
}
