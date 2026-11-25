package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

func TestWorkColumn(t *testing.T) {
	plan := config.Column{ID: "plan", AutoSpawn: true, PermissionMode: "plan"}
	plan2 := config.Column{ID: "plan2", AutoSpawn: true, PermissionMode: "plan"}
	exec := config.Column{ID: "exec", AutoSpawn: true}
	inbox := config.Column{ID: "inbox"}
	review := config.Column{ID: "review"}
	cases := []struct {
		name  string
		board *config.Board
		want  string
	}{
		{"default board", config.Default(), "executing"},
		{"skips plan columns", &config.Board{Columns: []config.Column{inbox, plan, plan2, exec}}, "exec"},
		{"all plan", &config.Board{Columns: []config.Column{inbox, plan, plan2}}, ""},
		{"board plan mode", &config.Board{DefaultPermissionMode: "plan", Columns: []config.Column{inbox, exec}}, ""},
		{"no agent column", &config.Board{Columns: []config.Column{inbox, review}}, ""},
	}
	for _, c := range cases {
		got, ok := workColumn(c.board)
		if got.ID != c.want || ok != (c.want != "") {
			t.Errorf("%s: workColumn = %q, %v, want %q", c.name, got.ID, ok, c.want)
		}
	}
}

func TestRunScriptAutomation(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"automations":{"onEnter":[
	    {"name":"Mark","type":"run_script","script":"printf '%s|%s' {{title}} \"$DROVER_TASKNUMBER\" > marker"},
	    {"name":"Break","type":"run_script","script":"echo boom; exit 3"}]}},
	  {"id":"done","name":"Done","role":"done"}]}`)

	task := e.CreateTask("Fix it's $HOME", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, e, "both scripts to run", func(t *store.Task) bool {
		return hasLog(t, `Automation "Break" failed`)
	})
	if got := readFile(filepath.Join(moved.WorktreePath, "marker")); got != "Fix it's $HOME|1" {
		t.Errorf("marker = %q", got)
	}
	if !hasLog(moved, `Automation "Mark" ran`) || !hasLog(moved, "boom") {
		t.Errorf("log missing script results: %+v", moved.Log)
	}
}

func TestNotifyRunsInColumnWithoutAgent(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"review","name":"In review","automations":{"onEnter":[
	    {"name":"Ping","type":"notify","title":"{{title}}","body":"now in {{toColumn}}"},
	    {"name":"Talk","type":"send_message","message":"hello"}]}},
	  {"id":"done","name":"Done","role":"done"}]}`)

	task := e.CreateTask("Ship it now", "")
	if err := e.Move(task.ID, "review", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := e.Snapshot().Tasks[0]
	if got := desktopNotified("Ship it now:"); len(got) != 1 || got[0] != "Ship it now: now in In review" {
		t.Errorf("notifications = %q", got)
	}
	if !hasLog(moved, `Automation "Talk" skipped: no agent runs in this column`) {
		t.Errorf("send_message not skipped: %+v", moved.Log)
	}
}

func TestConditionGatesTurnEndRows(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"automations":{"onTurnEnd":[
	    {"name":"Never","type":"move_to_column","targetColumn":"Done","condition":"The agent gave up"},
	    {"name":"Always","type":"move_to_column","targetColumn":"Review","condition":"ALWAYS true"}]}},
	  {"id":"review","name":"Review"},
	  {"id":"done","name":"Done","role":"done"}]}`)

	task := e.CreateTask("Do the work", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, e, "the ALWAYS row to move the card", func(t *store.Task) bool { return t.ColumnID == "review" })
	if !hasLog(moved, `Automation "Never" skipped: condition not met (answered "NO")`) {
		t.Errorf("Never row not skipped: %+v", moved.Log)
	}
}

func TestTurnEndRowSkipsTheTurnItStarted(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"automations":{"onTurnEnd":[
	    {"name":"Again","type":"send_message","message":"again please"},
	    {"name":"Count","type":"run_script","script":"echo x >> count"}]}},
	  {"id":"done","name":"Done","role":"done"}]}`)

	task := e.CreateTask("Do the work", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, e, "the row to skip its own turn", func(t *store.Task) bool {
		return hasLog(t, `Automation "Again" skipped: this turn answered its own message`)
	})
	time.Sleep(time.Second)
	events := readFile(e.sessionDir(moved, store.MainSession) + "/events.jsonl")
	if got := strings.Count(events, "Stop"); got != 2 {
		t.Errorf("turns ended = %d, want 2 (the prompt and the row's message)", got)
	}
	if typed := readFile(os.Getenv("FAKE_LOG")); strings.Count(typed, "again please") != 1 {
		t.Errorf("row's message sent more than once:\n%s", typed)
	}
	if got := readFile(filepath.Join(moved.WorktreePath, "count")); got != "x\nx\n" {
		t.Errorf("run_script row ran %q on the two turn ends, want it on both", got)
	}

	if err := e.Send(task.ID, "follow-up"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, "the row to fire again after a turn it didn't start", func(*store.Task) bool {
		return strings.Count(readFile(os.Getenv("FAKE_LOG")), "again please") == 2
	})
}

func TestConditionScriptGatesEnterRows(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"automations":{"onEnter":[
	    {"name":"Titled","type":"run_script","script":"echo yes > titled","conditionScript":"test \"$DROVER_TITLE\" = {{title}}"},
	    {"name":"Blocked","type":"run_script","script":"echo yes > blocked","conditionScript":"exit 1"},
	    {"name":"Early","type":"move_to_column","conditionScript":"echo yes > early"}]}},
	  {"id":"done","name":"Done","role":"done"}]}`)

	task := e.CreateTask("Fix it's $HOME", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, e, "the Early row to be skipped", func(t *store.Task) bool {
		return hasLog(t, `Automation "Early" skipped: move_to_column only runs on turn end`)
	})
	if got := readFile(filepath.Join(moved.WorktreePath, "titled")); got != "yes\n" {
		t.Errorf("titled = %q", got)
	}
	if !hasLog(moved, `Automation "Blocked" skipped: condition script not met (exit 1)`) {
		t.Errorf("Blocked row not skipped: %+v", moved.Log)
	}
	for _, name := range []string{"blocked", "early"} {
		if got := readFile(filepath.Join(moved.WorktreePath, name)); got != "" {
			t.Errorf("%s ran: %q", name, got)
		}
	}
}

func TestConditionScriptGatesTurnEndRows(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"exec","name":"Executing","autoSpawn":true,"automations":{
	    "onEnter":[{"name":"Mark","type":"run_script","script":"touch ready"}],
	    "onTurnEnd":[
	      {"name":"Marked","type":"run_script","script":"echo yes > marked","conditionScript":"test -f ready"},
	      {"name":"Unmarked","type":"run_script","script":"echo yes > unmarked","conditionScript":"exit 1"},
	      {"name":"Never","type":"move_to_column","targetColumn":"Done","conditionScript":"exit 1"},
	      {"name":"Short","type":"move_to_column","targetColumn":"Done","conditionScript":"exit 1","condition":"The agent gave up"},
	      {"name":"Both","type":"move_to_column","targetColumn":"Done","conditionScript":"test -f ready","condition":"The agent gave up"},
	      {"name":"Ready","type":"move_to_column","targetColumn":"Review","conditionScript":"test -f ready && test {{toColumn}} = Executing"}]}},
	  {"id":"review","name":"Review"},
	  {"id":"done","name":"Done","role":"done"}]}`)

	task := e.CreateTask("Do the work", "")
	if err := e.Move(task.ID, "exec", MoveOptions{}); err != nil {
		t.Fatal(err)
	}
	moved := waitFor(t, e, "the Ready row to move the card", func(t *store.Task) bool { return t.ColumnID == "review" })
	for _, name := range []string{"Never", "Short"} {
		if !hasLog(moved, `Automation "`+name+`" skipped: condition script not met (exit 1)`) {
			t.Errorf("%s row not skipped by its script: %+v", name, moved.Log)
		}
	}
	if hasLog(moved, `Automation "Short" skipped: condition not met`) {
		t.Errorf("Short row's condition ran after its script failed: %+v", moved.Log)
	}
	if !hasLog(moved, `Automation "Both" skipped: condition not met`) {
		t.Errorf("Both row not stopped by its condition: %+v", moved.Log)
	}
	for name, want := range map[string]string{"marked": "yes\n", "unmarked": ""} {
		if got := readFile(filepath.Join(moved.WorktreePath, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestErroredConditionScriptNotifiesForMove(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"done","name":"Done","role":"done"}]}`)
	task := e.CreateTask("Do the work", "")
	for len(e.Notices()) > 0 {
		<-e.Notices()
	}
	for _, c := range []struct {
		script, rowType string
		notice          bool
	}{
		{"kill -9 $$", "move_to_column", true},
		{"nosuchcmd_xyz", "move_to_column", true},
		{"exit 126", "move_to_column", true},
		{"kill -9 $$", "run_script", false},
		{"echo boom; exit 2", "move_to_column", false},
		{"echo boom; exit 1", "move_to_column", false},
	} {
		row := config.Automation{Name: "Gate", Type: c.rowType, ConditionScript: c.script}
		if e.shouldRun(task, row, groupContext{}) {
			t.Errorf("%q let its %s row run", c.script, c.rowType)
		}
		select {
		case notice := <-e.Notices():
			if !c.notice || !strings.Contains(notice, "the condition script of Gate errored") {
				t.Errorf("%q on %s: notice = %q", c.script, c.rowType, notice)
			}
		default:
			if c.notice {
				t.Errorf("%q on %s: no notice", c.script, c.rowType)
			}
		}
	}
}

func TestConditionScriptSeesAgentReply(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"backlog","name":"Backlog"},
	  {"id":"done","name":"Done","role":"done"}]}`)
	task := e.CreateTask("Do the work", "")
	row := config.Automation{Type: "notify", ConditionScript: `printf %s {{agentReply}} | grep -q DONE && test "$DROVER_AGENTREPLY" = "all DONE"`}
	if !e.shouldRun(task, row, groupContext{reply: "all DONE"}) {
		t.Error("row skipped for a reply the script accepts")
	}
	if e.shouldRun(task, row, groupContext{reply: "not yet"}) {
		t.Error("row ran for a reply the script rejects")
	}
}

func TestRunShellTimeout(t *testing.T) {
	start := time.Now()
	err := runShell(context.Background(), t.TempDir(), "sleep 30 & sleep 30", nil, 200*time.Millisecond, shellWaitDelay)
	if err == nil || !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Errorf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("a child kept the script running for %s", elapsed)
	}
}

func TestRunShellSucceedsWithBackgroundChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Cleanup(func() {
		if pid, err := os.ReadFile(pidFile); err == nil {
			exec.Command("kill", strings.TrimSpace(string(pid))).Run()
		}
	})
	if err := runShell(context.Background(), t.TempDir(), "sleep 8 & echo $! > "+pidFile+"; exit 0", nil, time.Minute, 100*time.Millisecond); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestRunShellFailureCarriesTheExitCodeAndTheScriptsOutput(t *testing.T) {
	err := runShell(context.Background(), t.TempDir(), "exit 3", nil, time.Minute, shellWaitDelay)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Errorf("err = %v, want an exit error with code 3", err)
	}

	err = runShell(context.Background(), t.TempDir(), "echo boom >&2; exit 1", nil, time.Minute, shellWaitDelay)
	if !errors.As(err, &exit) || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the script's output in it", err)
	}
}

func TestShellTimeout(t *testing.T) {
	for minutes, want := range map[int]time.Duration{0: 10 * time.Minute, 5: 5 * time.Minute, 500: 2 * time.Hour} {
		if got := shellTimeout(minutes); got != want {
			t.Errorf("shellTimeout(%d) = %s, want %s", minutes, got, want)
		}
	}
}

func TestTruncatePrompt(t *testing.T) {
	long := strings.Repeat("é", 4001)
	for name, tc := range map[string]struct{ in, want string }{
		"short":      {"fix it", "fix it"},
		"at the cap": {strings.Repeat("é", 4000), strings.Repeat("é", 4000)},
		"over":       {long, strings.Repeat("é", 4000)},
	} {
		if got := truncatePrompt(tc.in); got != tc.want {
			t.Errorf("%s: truncatePrompt cut to %d runes, want %d", name, len([]rune(got)), len([]rune(tc.want)))
		}
	}
}

func TestMoveTarget(t *testing.T) {
	board := &config.Board{Columns: []config.Column{{ID: "a", Name: "Alpha"}, {ID: "b", Name: "Beta"}, {ID: "c", Name: "Gamma"}}}
	for name, tc := range map[string]struct {
		ref     string
		current int
		want    string
	}{
		"next column":      {"", 0, "b"},
		"last has no next": {"", 2, ""},
		"by id":            {"c", 0, "c"},
		"by name":          {"beta", 2, "b"},
		"unknown":          {"nowhere", 0, ""},
	} {
		col, ok := moveTarget(board, tc.ref, tc.current)
		if col.ID != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: moveTarget = %q, %v, want %q", name, col.ID, ok, tc.want)
		}
	}
}

func TestRenderFillsPlaceholders(t *testing.T) {
	owner := &store.Task{ID: "o1", Number: 4, WorktreePath: "/wt/4", Branch: "feat/x", BaseBranch: "develop"}
	card := &store.Task{ID: "c1", Number: 5, Title: "Fix it", Prompt: "it's broken", GroupID: "o1",
		Issue: &store.Issue{Key: "DX-1", URL: "https://linear/DX-1"},
		PR:    &store.PR{URL: "https://gh/pr/9", Number: 9, Title: "PR title", Branch: "pr-branch"}}
	e := &Engine{Root: "/repo", ProjectName: "repo", state: &store.State{Tasks: []*store.Task{owner, card}}, board: &config.Board{DefaultBaseBranch: "trunk"}}
	gc := groupContext{from: config.Column{Name: "Plan"}, to: config.Column{Name: "Exec"}, trigger: "enter", reply: "done; rm -rf /"}

	for name, tc := range map[string]struct {
		tpl   string
		shell bool
		want  string
	}{
		"plain":                {"#{{taskNumber}} {{ title }} -> {{toColumn}} from {{fromColumn}}", false, "#5 Fix it -> Exec from Plan"},
		"owner's worktree":     {"{{worktreePath}} {{projectPath}} {{projectName}}", false, "/wt/4 /repo repo"},
		"PR branch wins":       {"{{branchName}} {{prBranch}} {{prNumber}} {{prUrl}}", false, "pr-branch pr-branch 9 https://gh/pr/9"},
		"issue":                {"{{issueKey}} {{issueUrl}}", false, "DX-1 https://linear/DX-1"},
		"owner's base":         {"{{baseBranch}}", false, "develop"},
		"agent reply":          {"{{agentReply}} ({{trigger}})", false, "done; rm -rf / (enter)"},
		"unknown":              {"{{reply}} {{nope}}", false, "{{reply}} {{nope}}"},
		"shell quoting":        {"echo {{description}} {{agentReply}}", true, `echo 'it'\''s broken' 'done; rm -rf /'`},
		"shell leaves unknown": {"echo {{nope}}", true, "echo {{nope}}"},
	} {
		if got := e.render(card, tc.tpl, gc, tc.shell); got != tc.want {
			t.Errorf("%s: render = %q, want %q", name, got, tc.want)
		}
	}
}

func TestVarsFallBackWithoutAWorktreePRorBase(t *testing.T) {
	card := &store.Task{ID: "c1", Number: 2, Branch: "own-branch"}
	for name, tc := range map[string]struct {
		board *config.Board
		base  string
	}{
		"board default": {&config.Board{DefaultBaseBranch: "trunk"}, "trunk"},
		"main":          {&config.Board{}, "main"},
	} {
		e := &Engine{Root: "/repo", state: &store.State{Tasks: []*store.Task{card}}, board: tc.board}
		v := e.vars(card, groupContext{})
		if v["worktreePath"] != "/repo" || v["branchName"] != "own-branch" || v["baseBranch"] != tc.base || v["prUrl"] != "" || v["prNumber"] != "" || v["issueKey"] != "" {
			t.Errorf("%s: vars = %v", name, v)
		}
	}
}

const turnEndBoard = `{"columns":[
  {"id":"plan","name":"Planning"},
  {"id":"exec","name":"Executing","autoSpawn":true,"automations":{"onTurnEnd":[%s]}},
  {"id":"review","name":"Review"},
  {"id":"done","name":"Done","role":"done"}]}`

// turnEndEngine is an engine with one card in Executing whose idle agent
// has a message queued, so what a turn end did to the queue is visible.
func turnEndEngine(t *testing.T, rows string) (*Engine, *store.Task) {
	t.Helper()
	e := boardEngine(t, fmt.Sprintf(turnEndBoard, rows))
	card := withQueued(agentCard("a", 1, "exec", 0))
	card.Visit = 1
	e.state.Tasks = []*store.Task{card}
	return e, card
}

func TestRunTurnEnd(t *testing.T) {
	move := func(target string) string {
		return fmt.Sprintf(`{"name":"Go","type":"move_to_column","targetColumn":%q}`, target)
	}
	for name, tc := range map[string]struct {
		rows       string
		fixing     bool
		visit      int
		wantColumn string
		wantLog    string
		wantQueued int
	}{
		"moves to the named column":             {rows: move("Review"), visit: 1, wantColumn: "review", wantLog: `Automation "Go": moving to Review`},
		"an empty target is the next column":    {rows: move(""), visit: 1, wantColumn: "review", wantLog: `Automation "Go": moving to Review`},
		"an unknown column leaves the card":     {rows: move("Nowhere"), visit: 1, wantColumn: "exec", wantLog: `Automation "Go": no column "Nowhere"`},
		"no move while fixing review comments":  {rows: move("Review"), fixing: true, visit: 1, wantColumn: "exec", wantLog: `Automation "Go" skipped: fixing review comments`},
		"no rows just delivers the queue":       {rows: "", visit: 1, wantColumn: "exec"},
		"a turn of an earlier visit is ignored": {rows: move("Review"), visit: 0, wantColumn: "exec", wantQueued: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			e, card := turnEndEngine(t, tc.rows)
			if tc.fixing {
				card.PR = &store.PR{FixFrom: "reviewer"}
			}
			e.runTurnEnd(card.ID, store.MainSession, tc.visit, "done")

			if card.ColumnID != tc.wantColumn {
				t.Errorf("column = %q, want %q", card.ColumnID, tc.wantColumn)
			}
			if tc.wantLog != "" && !hasLog(card, tc.wantLog) {
				t.Errorf("log = %v, want %q", card.Log, tc.wantLog)
			}
			if pending := len(card.Sessions[store.MainSession].Pending); pending != tc.wantQueued {
				t.Errorf("%d messages still queued, want %d", pending, tc.wantQueued)
			}
		})
	}
}

func TestFailedTurnEndMoveStillDeliversTheQueue(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	e, card := turnEndEngine(t, `{"name":"Finish","type":"move_to_column","targetColumn":"Done"}`)
	card.Sessions["bg"] = &store.Session{Key: "bg", Status: store.StatusExternal, Imported: true, PID: os.Getpid()}

	e.runTurnEnd(card.ID, store.MainSession, 1, "done")

	if card.ColumnID != "exec" || !hasLog(card, `Automation "Finish": move to Done failed`) {
		t.Errorf("column %q, log %v: the move should have been refused", card.ColumnID, card.Log)
	}
	if n := len(card.Sessions[store.MainSession].Pending); n != 0 {
		t.Errorf("%d messages still queued after the failed move", n)
	}
}

func TestExecuteLogsWhatTheRowDid(t *testing.T) {
	e := boardEngine(t, `{"columns":[{"id":"plan","name":"Planning"},{"id":"done","name":"Done","role":"done"}]}`)
	card := &store.Task{ID: "a", Number: 7, Title: "Ship the tracker", ColumnID: "plan"}
	e.state.Tasks = []*store.Task{card}
	gc := groupContext{from: e.board.Columns[0], to: e.board.Columns[1]}

	e.execute(card, config.Automation{Name: "Odd", Type: "explode"}, gc)
	if !hasLog(card, `Automation "Odd" failed: unsupported automation type "explode"`) {
		t.Errorf("log = %v", card.Log)
	}

	e.execute(card, config.Automation{Name: "Ping", Type: "notify"}, gc)
	if got := desktopNotified("Ship the tracker:"); len(got) != 1 || got[0] != "Ship the tracker: Done" {
		t.Errorf("a notify row with no title or body sent %q, want the card title and the column", got)
	}
	if !hasLog(card, `Automation "Ping" ran`) {
		t.Errorf("log = %v", card.Log)
	}

	e.execute(card, config.Automation{Name: "Fail", Type: "run_script", Script: "exit 4"}, gc)
	if !hasLog(card, `Automation "Fail" failed`) {
		t.Errorf("a failing script was not logged as failed: %v", card.Log)
	}
}

func TestInitialPrompt(t *testing.T) {
	review := config.Automation{Name: "Review", Type: "send_message", Message: "/code-review {{baseBranch}}"}
	script := config.Automation{Name: "Mark", Type: "run_script", Script: "true"}
	gatedByScript := config.Automation{Type: "send_message", Message: "/code-review", ConditionScript: "true"}
	gatedByAgent := config.Automation{Type: "send_message", Message: "/code-review", Condition: "The work is done"}
	for name, tc := range map[string]struct {
		task       store.Task
		enter      []config.Automation
		wantPrompt string
		wantRest   int
	}{
		"no prompt is the title":                    {store.Task{Title: "Fix login"}, nil, "Fix login", 0},
		"a prompt continuing the title stands":      {store.Task{Title: "Fix login", Prompt: "Fix login on Safari"}, nil, "Fix login on Safari", 0},
		"a separate prompt follows the title":       {store.Task{Title: "Fix login", Prompt: "It fails on Safari"}, nil, "Fix login\n\nIt fails on Safari", 0},
		"a leading message row replaces the prompt": {store.Task{Title: "Fix login", Prompt: "ignored"}, []config.Automation{review, script}, "/code-review main", 1},
		"a later message row is left to run":        {store.Task{Title: "Fix login"}, []config.Automation{script, review}, "Fix login", 2},
		"a message row gated by a script is kept":   {store.Task{Title: "Fix login"}, []config.Automation{gatedByScript}, "Fix login", 1},
		"a message row gated by the agent is kept":  {store.Task{Title: "Fix login"}, []config.Automation{gatedByAgent}, "Fix login", 1},
	} {
		e := bareEngine(t)
		task := tc.task
		e.state.Tasks = []*store.Task{&task}
		prompt, rest := e.initialPrompt(&task, tc.enter, groupContext{})
		if prompt != tc.wantPrompt || len(rest) != tc.wantRest {
			t.Errorf("%s: prompt %q with %d rows left, want %q with %d", name, prompt, len(rest), tc.wantPrompt, tc.wantRest)
		}
	}
}
