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

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

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
	if err := os.MkdirAll(filepath.Join(store.Dir(root), sessionFile), 0o755); err != nil {
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
