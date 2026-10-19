package config

import (
	"os"
	"path/filepath"
	"testing"
)

// upstream is the kangentic.json from the Kangentic repository itself.
const upstream = `{
  "version": 1,
  "columns": [
    {"id": "e6350e8a", "name": "To Do", "role": "todo", "icon": "layers", "color": "#6b7280"},
    {"id": "4873bb89", "name": "Planning", "icon": "map", "autoSpawn": true, "permissionMode": "plan",
     "modelOverride": "opus", "effortOverride": "xhigh", "planExitTarget": "Executing"},
    {"id": "dc9f2b38", "name": "Executing", "autoSpawn": true, "permissionMode": "auto",
     "automations": {"onTurnEnd": [
       {"name": "On to Code Review", "type": "move_to_column", "finalLine": "Findings: applied", "maxRuns": 4, "targetColumn": "Code Review"},
       {"name": "Apply findings", "type": "send_message", "message": "Apply them."}
     ]}},
    {"id": "623a36ef", "name": "Code Review", "autoSpawn": true, "sessionTarget": "isolated",
     "sessionSpawnStrategy": "always_spawn_new",
     "automations": {"onEnter": [{"name": "Message", "type": "send_message", "message": "/code-review {{baseBranch}}", "mode": "immediate"}]}},
    {"id": "64f9ef9a", "name": "Done", "role": "done"}
  ],
  "_modifiedBy": "7ef4fd0cb484"
}`

func TestLoadUpstreamBoard(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(upstream), 0o644)
	os.WriteFile(filepath.Join(dir, "kangentic.local.json"), []byte(`{"defaultBaseBranch":"develop"}`), 0o644)

	board, found, err := Load(dir)
	if err != nil || !found {
		t.Fatalf("Load: found=%v err=%v", found, err)
	}
	if len(board.Columns) != 5 || board.TodoColumn().Name != "To Do" {
		t.Fatalf("unexpected columns: %+v", board.Columns)
	}
	if board.DefaultBaseBranch != "develop" {
		t.Errorf("local override not applied: %q", board.DefaultBaseBranch)
	}
	review, ok := board.Find("code review")
	if !ok || !review.Isolated() || !review.AlwaysSpawnNew() {
		t.Errorf("Code Review column wrong: %+v", review)
	}
	exec, _ := board.Find("Executing")
	if rows := exec.Automations.OnTurnEnd; len(rows) != 2 || rows[0].MaxRuns != 4 || rows[0].FinalLine != "Findings: applied" {
		t.Errorf("turn-end automations wrong: %+v", rows)
	}
}

func TestLoadMissingUsesDefault(t *testing.T) {
	board, found, err := Load(t.TempDir())
	if err != nil || found || len(board.Columns) == 0 {
		t.Fatalf("got found=%v err=%v cols=%d", found, err, len(board.Columns))
	}
}

func TestPermissionModeFallsBackToBoardDefault(t *testing.T) {
	plan := Column{PermissionMode: "plan"}
	for _, tc := range []struct {
		board *Board
		col   Column
		want  string
	}{
		{&Board{DefaultPermissionMode: "acceptEdits"}, plan, "plan"},
		{&Board{DefaultPermissionMode: "acceptEdits"}, Column{}, "acceptEdits"},
		{&Board{}, Column{}, "auto"},
	} {
		if got := tc.board.PermissionMode(tc.col); got != tc.want {
			t.Errorf("PermissionMode(%+v) with default %q = %q, want %q", tc.col, tc.board.DefaultPermissionMode, got, tc.want)
		}
	}
}

func TestStartColumn(t *testing.T) {
	if got := Default().StartColumn().ID; got != "planning" {
		t.Errorf("default start column = %s, want planning", got)
	}
	manual := &Board{Columns: []Column{{ID: "todo", Role: "todo"}, {ID: "review"}, {ID: "done", Role: "done"}}}
	if got := manual.StartColumn().ID; got != "todo" {
		t.Errorf("start column without agents = %s, want todo", got)
	}
}

func TestPRFlow(t *testing.T) {
	if err := Default().validate(); err != nil {
		t.Fatalf("default board: %v", err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"version":1,"columns":[{"id":"todo","name":"To Do","role":"todo"}],
	  "prFlow":{"opened":"In Review"}}`), 0o644)
	if _, _, err := Load(dir); err == nil {
		t.Fatal("unknown prFlow column accepted")
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"version":1,"columns":[{"id":"todo","name":"To Do","role":"todo"},
	  {"id":"in-review","name":"In Review"}],"prFlow":{"opened":"in review"}}`), 0o644)
	board, _, err := Load(dir)
	if err != nil || board.PRFlow == nil || board.PRFlow.Opened != "in review" {
		t.Fatalf("prFlow not loaded: %+v %v", board.PRFlow, err)
	}
}
