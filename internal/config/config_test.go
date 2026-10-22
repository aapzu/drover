package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const sample = `{
  "columns": [
    {"id": "e6350e8a", "name": "To Do", "role": "todo", "icon": "layers", "color": "#6b7280"},
    {"id": "4873bb89", "name": "Planning", "icon": "map", "autoSpawn": true, "permissionMode": "plan",
     "modelOverride": "opus", "effortOverride": "xhigh", "planExitTarget": "Executing"},
    {"id": "dc9f2b38", "name": "Executing", "autoSpawn": true, "permissionMode": "auto",
     "automations": {"onTurnEnd": [
       {"name": "On to Code Review", "type": "move_to_column", "condition": "The findings are applied", "targetColumn": "Code Review"},
       {"name": "Apply findings", "type": "send_message", "message": "Apply them."}
     ]}},
    {"id": "623a36ef", "name": "Code Review", "autoSpawn": true,
     "automations": {"onEnter": [{"name": "Message", "type": "send_message", "message": "/code-review {{baseBranch}}"}]}},
    {"id": "64f9ef9a", "name": "Done", "role": "done"}
  ],
  "_modifiedBy": "7ef4fd0cb484"
}`

func TestLoadSampleBoard(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	os.WriteFile(filepath.Join(dir, "drover.local.json"), []byte(`{"defaultBaseBranch":"develop"}`), 0o644)

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
	if !ok || len(review.Automations.OnEnter) != 1 {
		t.Errorf("Code Review column wrong: %+v", review)
	}
	exec, _ := board.Find("Executing")
	if rows := exec.Automations.OnTurnEnd; len(rows) != 2 || rows[0].Condition != "The findings are applied" {
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
		{&Board{}, Column{}, ""},
	} {
		if got := tc.board.PermissionMode(tc.col); got != tc.want {
			t.Errorf("PermissionMode(%+v) with default %q = %q, want %q", tc.col, tc.board.DefaultPermissionMode, got, tc.want)
		}
	}
}

func TestReviewFlow(t *testing.T) {
	load := func(flow string) (*Board, error) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"todo","name":"To Do","role":"todo"},
		  {"id":"waiting","name":"Waiting"}],"reviewFlow":`+flow+`}`), 0o644)
		board, _, err := Load(dir)
		return board, err
	}
	for _, field := range []string{"started", "reviewed", "updated", "done"} {
		if _, err := load(`{"` + field + `":"nowhere"}`); err == nil {
			t.Errorf("unknown reviewFlow.%s column accepted", field)
		}
	}
	board, err := load(`{"started":"Waiting","reviewed":"waiting"}`)
	if err != nil || board.ReviewFlow == nil || board.ReviewFlow.Started != "Waiting" || board.ReviewFlow.Reviewed != "waiting" {
		t.Fatalf("reviewFlow not loaded: %+v %v", board.ReviewFlow, err)
	}
	if _, err := load(`{}`); err != nil {
		t.Errorf("empty reviewFlow rejected: %v", err)
	}
}

func TestWorktreesPath(t *testing.T) {
	dir := t.TempDir()
	board := `{"columns":[{"id":"todo","name":"To Do","role":"todo"}]`
	for _, tc := range []struct {
		name, main, local, want string
	}{
		{"default", ``, ``, filepath.Join(dir, ".drover", "worktrees")},
		{"relative", `,"worktreesDir":".claude/worktrees"`, ``, filepath.Join(dir, ".claude", "worktrees")},
		{"absolute", `,"worktreesDir":"/var/wt/"`, ``, "/var/wt"},
		{"local wins", `,"worktreesDir":"a"`, `{"worktreesDir":"b"}`, filepath.Join(dir, "b")},
		{"empty local keeps main", `,"worktreesDir":"a"`, `{"worktreesDir":""}`, filepath.Join(dir, "a")},
	} {
		os.WriteFile(filepath.Join(dir, FileName), []byte(board+tc.main+"}"), 0o644)
		os.Remove(filepath.Join(dir, "drover.local.json"))
		if tc.local != "" {
			os.WriteFile(filepath.Join(dir, "drover.local.json"), []byte(tc.local), 0o644)
		}
		b, _, err := Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := b.WorktreesPath(dir); got != tc.want {
			t.Errorf("%s: WorktreesPath = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWorktreesDirMustNotContainRoot(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{".", "..", "./", dir, filepath.Dir(dir), "/"} {
		os.WriteFile(filepath.Join(dir, FileName), []byte(`{"worktreesDir":`+strconv.Quote(bad)+`,"columns":[{"id":"todo","name":"To Do","role":"todo"}]}`), 0o644)
		if _, _, err := Load(dir); err == nil {
			t.Errorf("worktreesDir %q accepted", bad)
		}
	}
}

func TestPRFlow(t *testing.T) {
	if err := Default().validate(); err != nil {
		t.Fatalf("default board: %v", err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"todo","name":"To Do","role":"todo"}],
	  "prFlow":{"opened":"In Review"}}`), 0o644)
	if _, _, err := Load(dir); err == nil {
		t.Fatal("unknown prFlow column accepted")
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"todo","name":"To Do","role":"todo"},
	  {"id":"in-review","name":"In Review"}],"prFlow":{"opened":"in review"}}`), 0o644)
	board, _, err := Load(dir)
	if err != nil || board.PRFlow == nil || board.PRFlow.Opened != "in review" {
		t.Fatalf("prFlow not loaded: %+v %v", board.PRFlow, err)
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"todo","name":"To Do","role":"todo"},
	  {"id":"in-review","name":"In Review"}],"prFlow":{"requested":"in-review"}}`), 0o644)
	if _, _, err := Load(dir); err == nil {
		t.Fatal("prFlow.requested without autoSpawn accepted")
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"7d":    7 * 24 * time.Hour,
		"36h":   36 * time.Hour,
		"1h30m": 90 * time.Minute,
	} {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0d", "-1d", "abc", "d", "7days", "0.5d", "1e9d", "1s"} {
		if _, err := parseAge(in); err == nil {
			t.Errorf("parseAge(%q) succeeded", in)
		}
	}
}

func TestArchiveDoneAfter(t *testing.T) {
	root := t.TempDir()
	write := func(after string) {
		os.WriteFile(filepath.Join(root, FileName), []byte(`{"version":1,`+after+`"columns":[{"id":"done","name":"Done","role":"done"}]}`), 0o644)
	}
	write(`"archiveDoneAfter":"3d",`)
	board, _, err := Load(root)
	if err != nil || board.ArchiveAfter() != 72*time.Hour {
		t.Fatalf("got %v, %v", board.ArchiveAfter(), err)
	}
	write("")
	if board, _, err = Load(root); err != nil || board.ArchiveAfter() != 0 {
		t.Fatalf("unset: got %v, %v", board.ArchiveAfter(), err)
	}
	write(`"archiveDoneAfter":"soon",`)
	if _, _, err = Load(root); err == nil {
		t.Error("a bad archiveDoneAfter loaded")
	}
	if Default().ArchiveAfter() == 0 {
		t.Error("the default board never archives")
	}
}

func TestTodoColumnModel(t *testing.T) {
	for _, tc := range []struct {
		name, todo, want string
	}{
		{"unset", ``, DefaultTodoModel},
		{"overridden", `,"modelOverride":"sonnet"`, "sonnet"},
	} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, FileName), []byte(`{"columns":[{"id":"todo","name":"To Do","role":"todo"`+tc.todo+`}]}`), 0o644)
		board, _, err := Load(root)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := board.TodoColumn().ModelOverride; got != tc.want {
			t.Errorf("%s: todo model = %q, want %q", tc.name, got, tc.want)
		}
	}
	board, _, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := board.TodoColumn().ModelOverride; got != DefaultTodoModel {
		t.Errorf("default board: todo model = %q, want %q", got, DefaultTodoModel)
	}
}

func TestLocalOverridesPersonalSettings(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"maxConcurrent":4,"notifications":{"turnDone":false},
	  "columns":[{"id":"todo","name":"To Do","role":"todo"}]}`), 0o644)
	board, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultNotifications()
	want.TurnDone = false
	if board.Notifications != want || board.Limit() != 4 || !board.AutoName {
		t.Errorf("shared board: notifications=%+v limit=%d", board.Notifications, board.Limit())
	}

	before := Stamp(dir)
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(`{"maxConcurrent":2,"idleTimeoutMinutes":30,
	  "notifications":{"exited":false,"cooldownSeconds":60}}`), 0o644)
	if Stamp(dir) == before {
		t.Error("stamp ignores drover.local.json")
	}
	board, _, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want.Exited, want.CooldownSeconds = false, 60
	if board.Notifications != want || board.Limit() != 2 || board.IdleTimeoutMinutes != 30 {
		t.Errorf("local overrides: notifications=%+v limit=%d idle=%d", board.Notifications, board.Limit(), board.IdleTimeoutMinutes)
	}
}

func TestLocalOverridesApplyToDefaultBoard(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(`{"notifications":{"needsYou":false},"autoName":false}`), 0o644)
	board, found, err := Load(dir)
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if board.Notifications.NeedsYou || !board.Notifications.TurnDone || board.Limit() != DefaultMaxConcurrent || board.AutoName {
		t.Errorf("default board: notifications=%+v limit=%d", board.Notifications, board.Limit())
	}
}

func TestBrokenLocalFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(`{"maxConcurrent":`), 0o644)
	if _, _, err := Load(dir); err == nil {
		t.Error("want a parse error for drover.local.json")
	}
}
