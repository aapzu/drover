package engine

import (
	"path/filepath"
	"testing"

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
