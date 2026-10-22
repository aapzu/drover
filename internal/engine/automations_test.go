package engine

import (
	"context"
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
	todo := config.Column{ID: "todo", Role: "todo", AutoSpawn: true}
	review := config.Column{ID: "review"}
	cases := []struct {
		name  string
		board *config.Board
		want  string
	}{
		{"default board", config.Default(), "executing"},
		{"skips plan columns", &config.Board{Columns: []config.Column{todo, plan, plan2, exec}}, "exec"},
		{"all plan", &config.Board{Columns: []config.Column{todo, plan, plan2}}, ""},
		{"board plan mode", &config.Board{DefaultPermissionMode: "plan", Columns: []config.Column{todo, exec}}, ""},
		{"no agent column", &config.Board{Columns: []config.Column{todo, review}}, ""},
	}
	for _, c := range cases {
		got, ok := workColumn(c.board)
		if got.ID != c.want || ok != (c.want != "") {
			t.Errorf("%s: workColumn = %q, %v, want %q", c.name, got.ID, ok, c.want)
		}
	}
}

func TestFirstAgentColumn(t *testing.T) {
	plan := config.Column{ID: "plan", AutoSpawn: true, PermissionMode: "plan"}
	todo := config.Column{ID: "todo", Role: "todo", AutoSpawn: true}
	review := config.Column{ID: "review"}
	cases := []struct {
		name  string
		board *config.Board
		want  string
	}{
		{"default board", config.Default(), "planning"},
		{"skips non-agent columns", &config.Board{Columns: []config.Column{todo, review, plan}}, "plan"},
		{"no agent column", &config.Board{Columns: []config.Column{todo, review}}, ""},
	}
	for _, c := range cases {
		got, ok := firstAgentColumn(c.board)
		if got.ID != c.want || ok != (c.want != "") {
			t.Errorf("%s: firstAgentColumn = %q, %v, want %q", c.name, got.ID, ok, c.want)
		}
	}
}

func TestRunScriptAutomation(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"todo","name":"To Do","role":"todo"},
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
	  {"id":"todo","name":"To Do","role":"todo"},
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
	  {"id":"todo","name":"To Do","role":"todo"},
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
	if !hasLog(moved, `Automation "Never" skipped: condition not met`) {
		t.Errorf("Never row not skipped: %+v", moved.Log)
	}
}

func TestConditionScriptGatesEnterRows(t *testing.T) {
	e := startWithBoard(t, `{"columns":[
	  {"id":"todo","name":"To Do","role":"todo"},
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
	  {"id":"todo","name":"To Do","role":"todo"},
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
	  {"id":"todo","name":"To Do","role":"todo"},
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
	  {"id":"todo","name":"To Do","role":"todo"},
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
	err := runShell(context.Background(), t.TempDir(), "sleep 30 & sleep 30", nil, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Errorf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("a child kept the script running for %s", elapsed)
	}
}

func TestRunShellSucceedsWithBackgroundChild(t *testing.T) {
	if err := runShell(context.Background(), t.TempDir(), "sleep 8 & exit 0", nil, time.Minute); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestRunShellErrorWithoutOutput(t *testing.T) {
	err := runShell(context.Background(), t.TempDir(), "exit 3", nil, time.Minute)
	if err == nil || err.Error() != "exit status 3" {
		t.Errorf("err = %v", err)
	}
}

func TestInitialPromptKeepsGatedMessageRow(t *testing.T) {
	e := &Engine{}
	for _, row := range []config.Automation{
		{Type: "send_message", Message: "/code-review", ConditionScript: "true"},
		{Type: "send_message", Message: "/code-review", Condition: "The work is done"},
	} {
		prompt, rest := e.initialPrompt(&store.Task{Title: "Do the work"}, []config.Automation{row}, groupContext{})
		if prompt != "Do the work" || len(rest) != 1 || rest[0] != row {
			t.Errorf("prompt = %q, rest = %v", prompt, rest)
		}
	}
}
