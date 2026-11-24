package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

var realHasTranscript = hasTranscript

func init() {
	hasTranscript = func(string, string) (bool, error) { return true, nil }
	hasSession = func(string) bool { return false }
}

func stubTranscript(t *testing.T, has bool, err error) {
	t.Helper()
	hasTranscript = func(string, string) (bool, error) { return has, err }
	t.Cleanup(func() { hasTranscript = func(string, string) (bool, error) { return true, nil } })
}

type panel struct {
	live     bool
	calls    [][]string
	dirs     []string
	envs     []map[string]string
	stops    int
	mouseOff int
}

func fakePanel(t *testing.T) *panel {
	t.Helper()
	p := &panel{}
	origHas, origNew, origStop, origMouse := hasSession, newSession, stopSession, disableMouse
	t.Cleanup(func() { hasSession, newSession, stopSession, disableMouse = origHas, origNew, origStop, origMouse })
	hasSession = func(string) bool { return p.live }
	newSession = func(name, dir string, argv []string, env map[string]string) error {
		p.calls, p.dirs, p.envs, p.live = append(p.calls, argv), append(p.dirs, dir), append(p.envs, env), true
		return nil
	}
	stopSession = func(string) { p.live, p.stops = false, p.stops+1 }
	disableMouse = func(string) error { p.mouseOff++; return nil }
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

// newRoot is a project root with its own state directory.
func newRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return filepath.Join(t.TempDir(), "repo")
}

const savedID = "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"

// seedID saves a conversation id for root, as an earlier question or panel would have.
func seedID(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := save(root, savedID); err != nil {
		t.Fatal(err)
	}
	return savedID
}

// askArgv answers one question and returns the claude command line it ran.
func askArgv(t *testing.T, root string) []string {
	t.Helper()
	calls := fakeClaude(t, "ok", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	return (*calls)[0].argv
}

// panelArgv starts the panel and returns the claude command line it launched.
func panelArgv(t *testing.T, root string) []string {
	t.Helper()
	p := fakePanel(t)
	if _, err := Start(root, "/bin/drover", &config.Board{}); err != nil {
		t.Fatal(err)
	}
	return p.calls[0]
}

func TestAskStartsThenResumesOneConversation(t *testing.T) {
	root := newRoot(t)
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
	root := newRoot(t)
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

func TestCorruptSessionFileStartsANewConversation(t *testing.T) {
	root := newRoot(t)
	fakeClaude(t, "ok", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	if id := SessionID(root); id == "" {
		t.Fatal("no saved id")
	}
	if err := os.WriteFile(filepath.Join(store.Dir(root), sessionFile), []byte("not-a-uuid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if SessionID(root) != "" {
		t.Error("a corrupt session file should start a new conversation")
	}
}

func withDocs(t *testing.T, docs string) {
	t.Helper()
	old := Docs
	Docs = docs
	t.Cleanup(func() { Docs = old })
}

func TestPromptPointsAtTheDocs(t *testing.T) {
	root := "/work/my-app"
	withDocs(t, "")
	if got := systemPrompt("/bin/drover", root, &config.Board{}); strings.Contains(got, docsFile) {
		t.Errorf("prompt names the docs without any:\n%s", got)
	}
	withDocs(t, "# drover")
	got := systemPrompt("/bin/drover", root, &config.Board{})
	for _, want := range []string{docsPath(root), "Read that file first"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestSessionsReadTheDocsFromTheStateDir(t *testing.T) {
	const docs = "# drover\n\nPress `b` to hide a card.\n"
	cases := []struct {
		name string
		argv func(t *testing.T, root string) []string
	}{{"ask", askArgv}, {"panel", panelArgv}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDocs(t, docs)
			root := newRoot(t)
			args := tc.argv(t, root)

			if got, err := os.ReadFile(docsPath(root)); err != nil || string(got) != docs {
				t.Fatalf("docs file = %q, %v", got, err)
			}
			allowed := args[slices.Index(args, "--allowedTools")+1 : slices.Index(args, "--disallowedTools")]
			if want := "Read(/" + docsPath(root) + ")"; !slices.Contains(allowed, want) {
				t.Errorf("allowed tools lack %q: %q", want, allowed)
			}
			if !strings.Contains(flagValue(args, "--append-system-prompt"), docsPath(root)) {
				t.Error("the prompt doesn't name the docs file")
			}
		})
	}
}

func TestDocsAreRewrittenWhenTheyChange(t *testing.T) {
	root := newRoot(t)
	withDocs(t, "old")
	if err := writeDocs(root); err != nil {
		t.Fatal(err)
	}
	withDocs(t, "new")
	if err := writeDocs(root); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(docsPath(root)); string(got) != "new" {
		t.Errorf("docs file = %q, want new", got)
	}
	if _, err := os.Stat(docsPath(root) + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary file left behind: %v", err)
	}
}

func TestNoDocsMeansNoDocsRuleOrFile(t *testing.T) {
	withDocs(t, "")
	root := newRoot(t)
	args := askArgv(t, root)
	for _, arg := range args {
		if strings.HasPrefix(arg, "Read(") {
			t.Errorf("rule %q allows reading docs that don't exist", arg)
		}
	}
	if _, err := os.Stat(docsPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a docs file exists without docs: %v", err)
	}
}

func TestDocsWriteFailureChangesNothing(t *testing.T) {
	withDocs(t, "# drover")
	root := newRoot(t)
	id := seedID(t, root)
	if err := os.Mkdir(docsPath(root), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := fakeClaude(t, "ok", nil)
	p := fakePanel(t)

	if _, err := Ask(context.Background(), root, "/bin/drover", "q", true); err == nil || !strings.Contains(err.Error(), "docs") {
		t.Errorf("Ask --new = %v, want a docs error", err)
	}
	if _, err := Start(root, "/bin/drover", &config.Board{}); err == nil {
		t.Error("Start succeeded without writable docs")
	}
	p.live = true
	if err := Restart(root, "/bin/drover", &config.Board{}); err == nil {
		t.Error("Restart succeeded without writable docs")
	}
	if len(*calls) != 0 || len(p.calls) != 0 || p.stops != 0 || !p.live {
		t.Errorf("a session ran or stopped: %d asks, %d panels, %d stops", len(*calls), len(p.calls), p.stops)
	}
	if got := SessionID(root); got != id {
		t.Errorf("the conversation became %q, want %q kept", got, id)
	}
}

func TestAskReadsTheBoardFromTheRepoAndGivesClaudeADeadline(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(`{"columns":[{"id":"backlog","name":"Backlog"},{"id":"qa","name":"QA lane","autoSpawn":true}]}`), 0o644); err != nil {
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
	if !strings.Contains(flagValue(got, "--append-system-prompt"), "QA lane (id qa)") {
		t.Errorf("the assistant was not told about the repo's own columns: %q", got)
	}
	if left := time.Until(deadline); left <= 0 || left > askTimeout {
		t.Errorf("deadline in %s, want within %s", left, askTimeout)
	}
}

func TestAskReportsWhatWentWrong(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()

	fakeClaude(t, "  \n", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Errorf("blank output: %v", err)
	}
	if SessionID(root) != "" {
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

func TestStartOpensOneInteractiveSessionAndSavesItsConversation(t *testing.T) {
	root := newRoot(t)
	stubTranscript(t, false, nil)
	p := fakePanel(t)

	if created, err := Start(root, "/bin/drover", &config.Board{}); err != nil || !created {
		t.Fatalf("first Start = %v, %v", created, err)
	}
	if created, err := Start(root, "/bin/drover", &config.Board{}); err != nil || created {
		t.Fatalf("Start beside a running session = %v, %v; it should leave it alone", created, err)
	}
	if len(p.calls) != 1 || p.dirs[0] != root {
		t.Fatalf("Start twice should open one session in the repo: %q in %q", p.calls, p.dirs)
	}
	args := p.calls[0]
	if args[0] != "claude" || slices.Contains(args, "-p") {
		t.Errorf("the panel should run the interactive claude: %q", args)
	}
	id := flagValue(args, "--session-id")
	if saved := SessionID(root); saved != id {
		t.Errorf("saved id = %q; want %q", saved, id)
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
	stubTranscript(t, true, nil)
	hasTranscript = realHasTranscript
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

func TestStopWaitsForAStartInProgress(t *testing.T) {
	root := newRoot(t)
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

func TestStartAndRestartDoNotWaitForAQuestion(t *testing.T) {
	root := newRoot(t)
	p := fakePanel(t)
	unlock, err := lock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Start(root, "/bin/drover", &config.Board{}); !errors.Is(err, errBusy) {
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
	root := newRoot(t)
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
	root := newRoot(t)
	p := fakePanel(t)
	p.live = true
	stopSession = func(string) {}
	if err := Stop(root); err == nil || !strings.Contains(err.Error(), "kill-session") {
		t.Errorf("Stop = %v; want the command that ends it", err)
	}
}

func TestUnreadableConversationStateIsAnErrorNotANewConversation(t *testing.T) {
	root := newRoot(t)
	if err := os.MkdirAll(filepath.Join(store.Dir(root), sessionFile), 0o755); err != nil {
		t.Fatal(err)
	}
	p := fakePanel(t)
	calls := fakeClaude(t, "ok", nil)
	if _, err := Start(root, "/bin/drover", &config.Board{}); err == nil || len(p.calls) != 0 {
		t.Errorf("Start = %v with %d sessions", err, len(p.calls))
	}
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err == nil || len(*calls) != 0 {
		t.Errorf("Ask = %v with %d runs", err, len(*calls))
	}

	stubTranscript(t, false, errors.New("permission denied"))
	root = filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := save(root, "6f1c1f0e-8c35-4a0e-9d3c-0d6a4a2f7a11"); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(root, "/bin/drover", &config.Board{}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Start = %v; a transcript that can't be checked shouldn't pass for a missing one", err)
	}
}

func TestRestartKeepsTheConversationWhenTheOldSessionWontDie(t *testing.T) {
	root := newRoot(t)
	p := fakePanel(t)
	if _, err := Start(root, "/bin/drover", &config.Board{}); err != nil {
		t.Fatal(err)
	}
	saved := SessionID(root)
	stopSession = func(string) {}
	if err := Restart(root, "/bin/drover", &config.Board{}); err == nil {
		t.Fatal("Restart succeeded beside a session that is still running")
	}
	if got := SessionID(root); got != saved || len(p.calls) != 1 {
		t.Errorf("saved id %q (was %q), %d sessions; the conversation was forgotten for a restart that didn't happen", got, saved, len(p.calls))
	}
}

func TestStartSurfacesASessionErrorAndKeepsTheSavedId(t *testing.T) {
	root := newRoot(t)
	fakePanel(t)
	newSession = func(string, string, []string, map[string]string) error { return errors.New("tmux broke") }
	created, err := Start(root, "/bin/drover", &config.Board{})
	if err == nil || created || !strings.Contains(err.Error(), "tmux broke") {
		t.Fatalf("Start = %v, %v", created, err)
	}
	if SessionID(root) == "" {
		t.Error("the conversation id was lost with the failed start")
	}
}

func TestStartFailsWithoutLaunchingWhenTheConversationCannotBeSaved(t *testing.T) {
	root := newRoot(t)
	p := fakePanel(t)
	stubTranscript(t, false, nil)
	tmp := filepath.Join(store.Dir(root), sessionFile+".tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := Start(root, "/bin/drover", &config.Board{})
	if err == nil || created || !strings.Contains(err.Error(), "could not save the conversation") {
		t.Fatalf("Start = %v, %v", created, err)
	}
	if len(p.calls) != 0 {
		t.Errorf("launched %d sessions with an unsaved conversation id", len(p.calls))
	}
}

func TestStartAndRestartRefuseANilBoardWithoutStoppingAnything(t *testing.T) {
	root := newRoot(t)
	p := fakePanel(t)
	if _, err := Start(root, "/bin/drover", nil); !errors.Is(err, errNoBoard) {
		t.Errorf("Start with a nil board = %v", err)
	}
	p.live = true
	if _, err := Start(root, "/bin/drover", nil); !errors.Is(err, errNoBoard) {
		t.Errorf("Start with a nil board beside a live session = %v", err)
	}
	if err := Restart(root, "/bin/drover", nil); !errors.Is(err, errNoBoard) {
		t.Errorf("Restart with a nil board = %v", err)
	}
	p.live = false
	if len(p.calls) != 0 {
		t.Errorf("launched %d sessions without a board", len(p.calls))
	}
}

func TestSessionIsLockedDown(t *testing.T) {
	const exe = "/bin/drover"
	cases := []struct {
		name  string
		argv  func(t *testing.T, root string) []string
		check func(t *testing.T, args []string)
	}{
		{"ask", askArgv, func(t *testing.T, args []string) {
			if args[1] != "-p" {
				t.Errorf("ask should run headless: %q", args)
			}
		}},
		{"panel", panelArgv, func(t *testing.T, args []string) {
			if slices.Contains(args, "--permission-mode") {
				t.Errorf("the panel should ask about commands the rules don't cover: %q", args)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			args := tc.argv(t, root)
			tc.check(t, args)

			if args[0] != "claude" || flagValue(args, "--model") != model || flagValue(args, "--tools") != "Bash,Read,Grep,Glob" {
				t.Errorf("claude, model or tools wrong: %q", args[:4])
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
				switch {
				case strings.Contains(rule, exe):
				case rule == "Bash(gh)" || rule == "Bash(gh *)":
				case strings.HasPrefix(rule, "Bash(git "):
					sub := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(rule, "Bash("), ")"))[1]
					if !slices.Contains(gitReadOnly, sub) {
						t.Errorf("rule %q allows git %s", rule, sub)
					}
				default:
					t.Errorf("rule %q is neither the drover binary, gh nor read-only git", rule)
				}
			}
			for _, want := range []string{"Bash(gh)", "Bash(gh *)"} {
				if !slices.Contains(allowed, want) {
					t.Errorf("allowed tools lack %q", want)
				}
			}
			wantGit := []string{"status", "log", "show", "diff", "blame", "ls-files", "ls-tree", "shortlog", "rev-parse", "describe", "cat-file"}
			if !slices.Equal(gitReadOnly, wantGit) {
				t.Errorf("gitReadOnly = %q, want exactly %q", gitReadOnly, wantGit)
			}
			for _, sub := range wantGit {
				for _, want := range []string{"Bash(git " + sub + ")", "Bash(git " + sub + " *)"} {
					if !slices.Contains(allowed, want) {
						t.Errorf("allowed tools lack %q", want)
					}
				}
			}
			for _, rule := range []string{"Read", "Grep", "Glob", "Bash(git *)", "Bash(git:*)"} {
				if slices.Contains(allowed, rule) {
					t.Errorf("%q is allowed; the file tools stay in the working directory without a rule", rule)
				}
			}
			denied := args[slices.Index(args, "--disallowedTools")+1 : slices.Index(args, "--session-id")]
			for _, want := range []string{
				"Bash(" + exe + " rm *-force*)", "Bash('" + exe + "' rm *-force*)",
				"Edit", "Write", "NotebookEdit",
			} {
				if !slices.Contains(denied, want) {
					t.Errorf("%q isn't denied: %q", want, denied)
				}
			}
			for _, pattern := range append(slices.Clone(gitDenied), ghDenied...) {
				if want := "Bash(" + pattern + ")"; !slices.Contains(denied, want) {
					t.Errorf("%q isn't denied: %q", want, denied)
				}
			}
			for _, rule := range allowed {
				if strings.Contains(rule, " rm") && !strings.HasSuffix(rule, " rm)") && !strings.HasSuffix(rule, " rm *)") {
					t.Errorf("rm rule %q allows more than rm", rule)
				}
			}
			prompt := flagValue(args, "--append-system-prompt")
			for _, want := range []string{
				"'" + exe + "' list --json", "never pass --force", "wait for the user to agree",
				"read-only to you", "offer a card", "Run `add` only after the user agrees",
				"A card's `pr` has", "Never answer whether a pull request is merged or closed from git history",
				"run `gh` in the repository", "do that only when the user asks", "A card without `pr`",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("the prompt lacks %q", want)
				}
			}

			if i := slices.Index(args, "--setting-sources"); i < 0 || args[i+1] != "" {
				t.Errorf("settings files may add allow rules, but argv = %q", args)
			}
			var got struct {
				Env     map[string]string
				Sandbox struct {
					Filesystem struct{ DenyWrite, AllowWrite []string }
					Network    struct{ AllowedDomains []string }
				}
			}
			if err := json.Unmarshal([]byte(flagValue(args, "--settings")), &got); err != nil {
				t.Fatal(err)
			}
			fs := got.Sandbox.Filesystem
			if !slices.Equal(fs.DenyWrite, []string{root}) || !slices.Equal(fs.AllowWrite, []string{store.Dir(root)}) {
				t.Errorf("sandbox filesystem = %+v", fs)
			}
			if !slices.Equal(got.Sandbox.Network.AllowedDomains, ghHosts) {
				t.Errorf("sandbox network = %+v, gh needs %q", got.Sandbox.Network, ghHosts)
			}
			if got.Env["GIT_OPTIONAL_LOCKS"] != "0" {
				t.Errorf("env = %v", got.Env)
			}
		})
	}
}

func TestConversationResumesOnlyOnceItHasATranscript(t *testing.T) {
	cases := []struct {
		name       string
		argv       func(t *testing.T, root string) []string
		transcript bool
		resumes    bool
		keepsID    bool
	}{
		{"panel resumes once it has a transcript", panelArgv, true, true, true},
		{"panel keeps the saved id until it has a transcript", panelArgv, false, false, true},
		{"ask resumes once it has a transcript", askArgv, true, true, true},
		{"ask does not resume a conversation that never got a message", askArgv, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			id := seedID(t, root)
			stubTranscript(t, tc.transcript, nil)
			args := tc.argv(t, root)
			switch {
			case tc.resumes:
				if flagValue(args, "--resume") != id || slices.Contains(args, "--session-id") {
					t.Errorf("should resume %s: %q", id, args)
				}
			case slices.Contains(args, "--resume") || flagValue(args, "--session-id") == "":
				t.Errorf("there is nothing to resume yet: %q", args)
			case tc.keepsID && (flagValue(args, "--session-id") != id || SessionID(root) != id):
				t.Errorf("should start under the saved id %s: %q, saved %q", id, args, SessionID(root))
			}
		})
	}
}

func TestForgettingStartsANewConversation(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, root string) (first, second []string)
	}{
		{"reset", func(t *testing.T, root string) ([]string, []string) {
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
			if len(*calls) != 2 {
				t.Fatalf("claude ran %d times, want 2", len(*calls))
			}
			return (*calls)[0].argv, (*calls)[1].argv
		}},
		{"fresh question", func(t *testing.T, root string) ([]string, []string) {
			calls := fakeClaude(t, "ok", nil)
			for _, fresh := range []bool{false, true} {
				if _, err := Ask(context.Background(), root, "/bin/drover", "q", fresh); err != nil {
					t.Fatal(err)
				}
			}
			return (*calls)[0].argv, (*calls)[1].argv
		}},
		{"restart", func(t *testing.T, root string) ([]string, []string) {
			p := fakePanel(t)
			for _, restart := range []bool{false, true} {
				var err error
				if restart {
					err = Restart(root, "/bin/drover", &config.Board{})
				} else {
					_, err = Start(root, "/bin/drover", &config.Board{})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if p.stops != 1 || len(p.calls) != 2 {
				t.Fatalf("stops = %d, sessions = %d", p.stops, len(p.calls))
			}
			return p.calls[0], p.calls[1]
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, second := tc.run(t, newRoot(t))
			if slices.Contains(second, "--resume") || flagValue(second, "--session-id") == "" {
				t.Errorf("the next conversation should start a session: %q", second)
			}
			if flagValue(first, "--session-id") == flagValue(second, "--session-id") {
				t.Errorf("the conversation continued the old session id: %q", second)
			}
		})
	}
}

func TestFreshKeepsTheConversationWhenTheQuestionCannotRun(t *testing.T) {
	root := newRoot(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeClaude(t, "ok", nil)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, config.FileName), []byte("{"), 0o644)
	before := SessionID(root)
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", true); err == nil {
		t.Fatal("expected the broken board to fail the question")
	}
	if after := SessionID(root); after == "" || after != before {
		t.Error("a question that couldn't run still forgot the conversation")
	}
}

func TestPanelRefusesWhileItRuns(t *testing.T) {
	cases := []struct {
		name string
		run  func(root string) error
		hint string
	}{
		{"ask", func(root string) error {
			_, err := Ask(context.Background(), root, "/bin/drover", "q", false)
			return err
		}, ""},
		{"ask --new", func(root string) error {
			_, err := Ask(context.Background(), root, "/bin/drover", "q", true)
			return err
		}, ""},
		{"reset", Reset, "press N"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			p := fakePanel(t)
			calls := fakeClaude(t, "ok", nil)
			id := seedID(t, root)
			p.live = true

			err := tc.run(root)
			if !errors.Is(err, errPanelOpen) || !strings.Contains(err.Error(), "kill-session -t "+SessionName(root)) || !strings.Contains(err.Error(), tc.hint) {
				t.Errorf("err = %v", err)
			}
			if SessionID(root) != id {
				t.Errorf("the conversation of a running panel was forgotten")
			}
			if len(*calls) != 0 {
				t.Errorf("claude ran %d times beside the panel", len(*calls))
			}
			Stop(root)
			if _, err := Ask(context.Background(), root, "/bin/drover", "q", false); err != nil {
				t.Errorf("Ask after the panel closed: %v", err)
			}
		})
	}
}

func TestMoveOffer(t *testing.T) {
	const opener = "When a card in "
	wait, verify, shipped, later := config.Column{ID: "wait", Name: "Waiting"}, config.Column{ID: "verify", Name: "Verify"}, config.Column{ID: "fin", Name: "Shipped", Role: "done"}, config.Column{ID: "later", Name: "Later", Role: "done"}
	cases := []struct {
		name          string
		cols          []config.Column
		flow          *config.PRFlow
		named, absent []string
	}{
		{"merged and closed go to different columns", []config.Column{wait, verify, shipped}, &config.PRFlow{Opened: "wait", Merged: "verify"}, []string{"Waiting", "Verify", "Shipped"}, nil},
		{"no merged column", []config.Column{wait, verify, shipped}, &config.PRFlow{Opened: "wait"}, []string{"Waiting", "Shipped"}, []string{"Verify"}},
		{"merged is the done column", []config.Column{wait, verify, shipped}, &config.PRFlow{Opened: "wait", Merged: "fin"}, []string{"Waiting", "Shipped"}, []string{"Verify"}},
		{"merged names no column", []config.Column{wait, verify, shipped}, &config.PRFlow{Opened: "wait", Merged: "nowhere"}, []string{"Waiting", "Shipped"}, []string{"Verify", "nowhere"}},
		{"columns are found by name", []config.Column{wait, verify, shipped}, &config.PRFlow{Opened: "WAITING", Merged: "verify"}, []string{"Waiting", "Verify", "Shipped"}, nil},
		{"the first done column wins", []config.Column{wait, shipped, later}, &config.PRFlow{Opened: "wait"}, []string{"Waiting", "Shipped"}, []string{"Later"}},
		{"no done column", []config.Column{wait}, &config.PRFlow{Opened: "wait"}, nil, nil},
		{"no PR flow", []config.Column{shipped}, nil, nil, nil},
		{"no opened column", []config.Column{wait, shipped}, &config.PRFlow{Merged: "fin"}, nil, nil},
		{"opened names no column", []config.Column{wait, shipped}, &config.PRFlow{Opened: "nowhere"}, nil, nil},
	}
	for _, tc := range cases {
		board := &config.Board{Columns: tc.cols, PRFlow: tc.flow}
		got := moveOffer(board)
		prompt := systemPrompt("/bin/drover", "/work/my-app", board)
		if tc.named == nil {
			if got != "" || strings.Contains(prompt, opener) {
				t.Errorf("%s: offer = %q; with no column to offer there should be none", tc.name, got)
			}
			continue
		}
		if !strings.Contains(prompt, got) || !strings.Contains(got, "only after the user agrees") {
			t.Errorf("%s: the offer %q is missing from the prompt or doesn't wait for the user", tc.name, got)
		}
		at := 0
		for _, name := range tc.named {
			i := strings.Index(got[at:], name)
			if i < 0 {
				t.Errorf("%s: the offer doesn't name %s, in order %q: %q", tc.name, name, tc.named, got)
				break
			}
			at += i + len(name)
		}
		for _, name := range tc.absent {
			if strings.Contains(got, name) {
				t.Errorf("%s: the offer names %s: %q", tc.name, name, got)
			}
		}
	}
}

func TestDescribeColumns(t *testing.T) {
	cases := []struct {
		name string
		cols []config.Column
		want string
	}{
		{"no columns", nil, ""},
		{"plain", []config.Column{{ID: "a", Name: "A"}}, "- A (id a): no agent runs here"},
		{"agent without a mode", []config.Column{{ID: "a", Name: "A", AutoSpawn: true}}, "- A (id a): an agent works on the card"},
		{"agent with a mode", []config.Column{{ID: "a", Name: "A", AutoSpawn: true, PermissionMode: "plan"}}, "- A (id a): an agent works on the card in plan mode"},
		{"done", []config.Column{{ID: "d", Name: "D", Role: "done"}}, "- D (id d): finished cards; the agent stops and the worktree is kept until the card is archived"},
		{"done is not an agent column", []config.Column{{ID: "d", Name: "D", Role: "done", AutoSpawn: true, PermissionMode: "plan"}}, "- D (id d): finished cards; the agent stops and the worktree is kept until the card is archived"},
		{"keep agent", []config.Column{{ID: "k", Name: "K", KeepAgent: true}}, "- K (id k): a running agent is kept instead of suspended, and none starts here"},
		{"in board order", []config.Column{{ID: "b", Name: "B"}, {ID: "a", Name: "A"}}, "- B (id b): no agent runs here\n- A (id a): no agent runs here"},
	}
	for _, tc := range cases {
		if got := describeColumns(&config.Board{Columns: tc.cols}); got != tc.want {
			t.Errorf("%s: describeColumns = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBaseBranchInThePrompt(t *testing.T) {
	if got := baseBranch(&config.Board{DefaultBaseBranch: "develop"}); got != ", and worktrees branch from develop by default" {
		t.Errorf("baseBranch = %q", got)
	}
	if got := baseBranch(&config.Board{}); got != "" {
		t.Errorf("baseBranch without a base branch = %q", got)
	}
	if prompt := systemPrompt("/bin/drover", "/work/my-app", &config.Board{}); !strings.Contains(prompt, "my-app at /work/my-app.\n") || strings.Contains(prompt, "branch from") {
		t.Errorf("a board without a base branch should not name one:\n%s", prompt)
	}
}

func TestPanelEnvFollowsDisableMouse(t *testing.T) {
	for _, on := range []bool{true, false} {
		p := fakePanel(t)
		if _, err := Start(newRoot(t), "/bin/drover", &config.Board{DisableMouse: on}); err != nil {
			t.Fatal(err)
		}
		if has := p.envs[0]["CLAUDE_CODE_DISABLE_MOUSE"] == "1"; has != on {
			t.Errorf("disableMouse=%t: env = %v", on, p.envs[0])
		}
		if (p.mouseOff == 1) != on {
			t.Errorf("disableMouse=%t: tmux mouse turned off %d times", on, p.mouseOff)
		}
	}
}

func allowedRules(args []string) []string {
	return args[slices.Index(args, "--allowedTools")+1 : slices.Index(args, "--disallowedTools")]
}

func deniedRules(args []string) []string {
	return args[slices.Index(args, "--disallowedTools")+1 : slices.Index(args, "--session-id")]
}

func TestAssistantMCPSettingsComeFromTheBoardAndOnlyAddAllowRules(t *testing.T) {
	const plugin, tool = "slack@claude-plugins-official", "mcp__plugin_slack_slack__slack_read_*"
	plain := panelArgv(t, newRoot(t))
	if strings.Contains(flagValue(plain, "--settings"), "enabledPlugins") || strings.Contains(flagValue(plain, "--append-system-prompt"), tool) {
		t.Errorf("a board without assistant MCP settings must not enable plugins or name MCP tools: %q", plain)
	}

	board := &config.Board{AssistantPlugins: []string{plugin}, AssistantTools: []string{tool}}
	panel := func(t *testing.T) []string {
		p := fakePanel(t)
		if _, err := Start(newRoot(t), "/bin/drover", board); err != nil {
			t.Fatal(err)
		}
		return p.calls[0]
	}
	ask := func(t *testing.T) []string {
		fakePanel(t)
		root := newRoot(t)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		data := `{"assistantPlugins":["` + plugin + `"],"assistantTools":["` + tool + `"],"columns":[{"id":"a","name":"A"}]}`
		if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return askArgv(t, root)
	}
	for name, run := range map[string]func(*testing.T) []string{"panel": panel, "ask": ask} {
		t.Run(name, func(t *testing.T) {
			args := run(t)
			var settings struct {
				EnabledPlugins map[string]bool `json:"enabledPlugins"`
			}
			if err := json.Unmarshal([]byte(flagValue(args, "--settings")), &settings); err != nil || !settings.EnabledPlugins[plugin] {
				t.Errorf("--settings does not enable %s: %v %s", plugin, err, flagValue(args, "--settings"))
			}
			if !slices.Contains(allowedRules(args), tool) {
				t.Errorf("allowed tools lack %q: %q", tool, allowedRules(args))
			}
			if !strings.Contains(flagValue(args, "--append-system-prompt"), tool) {
				t.Error("the prompt does not name the MCP tool")
			}
			if !slices.Contains(args, "--setting-sources") || flagValue(args, "--setting-sources") != "" {
				t.Errorf("settings sources must stay empty: %q", args)
			}
			if flagValue(args, "--tools") != flagValue(plain, "--tools") {
				t.Errorf("--tools changed: %q", flagValue(args, "--tools"))
			}
			if !slices.Equal(deniedRules(args), deniedRules(plain)) {
				t.Errorf("deny rules changed: %q", deniedRules(args))
			}
			for _, rule := range allowedRules(args) {
				if rule == "Edit" || rule == "Write" || rule == "Bash(git *)" {
					t.Errorf("rule %q must not be allowed", rule)
				}
			}
		})
	}
}

func TestAskExplainsAFailureWithoutBlowingUpTheMessage(t *testing.T) {
	root := newRoot(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := runClaude
	t.Cleanup(func() { runClaude = orig })
	runClaude = func(ctx context.Context, dir string, argv []string, stdin string) (string, error) {
		argv = []string{"sh", "-c", "printf a >&2; i=0; while [ $i -lt 400 ]; do printf 'é' >&2; i=$((i+1)); done; exit 1"}
		return orig(ctx, dir, argv, stdin)
	}
	_, err := Ask(context.Background(), root, "/bin/drover", "q", false)
	if err == nil {
		t.Fatal("expected the failure to surface")
	}
	msg := err.Error()
	if !utf8.ValidString(msg) || !strings.HasSuffix(msg, "é…") {
		t.Errorf("a long stderr should be cut on a character boundary with an ellipsis: %q", msg[max(0, len(msg)-20):])
	}
	if len(msg) > 300+maxErrorLen {
		t.Errorf("the error is %d bytes long; stderr should be cut to about %d", len(msg), maxErrorLen)
	}
}

func TestAskFailsWhenTheConversationCannotBeForgotten(t *testing.T) {
	root := newRoot(t)
	calls := fakeClaude(t, "ok", nil)
	stuck := filepath.Join(store.Dir(root), sessionFile)
	if err := os.MkdirAll(stuck, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Ask(context.Background(), root, "/bin/drover", "q", true); err == nil || len(*calls) != 0 {
		t.Errorf("ask --new = %v with %d runs; it must not answer from the conversation it failed to forget", err, len(*calls))
	}
	if err := Reset(root); err == nil {
		t.Error("Reset reported success though the conversation is still there")
	}
}

func TestStopStillEndsTheSessionWhenTheLockFails(t *testing.T) {
	root := newRoot(t)
	p := fakePanel(t)
	p.live = true
	if err := os.MkdirAll(filepath.Join(store.Dir(root), lockFile), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Stop(root)
	if err == nil || errors.Is(err, errBusy) || !strings.Contains(err.Error(), lockFile) {
		t.Errorf("Stop = %v; want the lock failure reported", err)
	}
	if p.live || p.stops != 1 {
		t.Errorf("live %v, stops %d; a broken lock must not keep the session running", p.live, p.stops)
	}
}

func TestAskGivesUpOnACancelledContext(t *testing.T) {
	root := newRoot(t)
	orig := runClaude
	t.Cleanup(func() { runClaude = orig })
	runClaude = func(ctx context.Context, _ string, _ []string, _ string) (string, error) { return "", ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Ask(ctx, root, "/bin/drover", "q", false); !errors.Is(err, context.Canceled) {
		t.Errorf("Ask = %v, want the cancellation", err)
	}
	if SessionID(root) != "" {
		t.Error("a question that never ran started a conversation")
	}
}
