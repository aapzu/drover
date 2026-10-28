package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
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
	if len(board.Columns) != 4 || board.FirstColumn().Name != "Planning" {
		t.Fatalf("unexpected columns: %+v", board.Columns)
	}
	if !slices.Equal(board.LegacyTodo, []string{"e6350e8a", "todo"}) {
		t.Errorf("LegacyTodo = %v, want the dropped To Do column", board.LegacyTodo)
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

func TestAutomationConditional(t *testing.T) {
	for _, c := range []struct {
		row  Automation
		want bool
	}{
		{Automation{}, false},
		{Automation{Condition: "done"}, true},
		{Automation{ConditionScript: "true"}, true},
		{Automation{Condition: "done", ConditionScript: "true"}, true},
	} {
		if got := c.row.Conditional(); got != c.want {
			t.Errorf("%+v: Conditional() = %v", c.row, got)
		}
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
		os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"first","name":"First"},
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
	board := `{"columns":[{"id":"first","name":"First"}]`
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
		os.WriteFile(filepath.Join(dir, FileName), []byte(`{"worktreesDir":`+strconv.Quote(bad)+`,"columns":[{"id":"first","name":"First"}]}`), 0o644)
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
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"first","name":"First"}],
	  "prFlow":{"opened":"In Review"}}`), 0o644)
	if _, _, err := Load(dir); err == nil {
		t.Fatal("unknown prFlow column accepted")
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"first","name":"First"},
	  {"id":"in-review","name":"In Review"}],"prFlow":{"opened":"in review"}}`), 0o644)
	board, _, err := Load(dir)
	if err != nil || board.PRFlow == nil || board.PRFlow.Opened != "in review" {
		t.Fatalf("prFlow not loaded: %+v %v", board.PRFlow, err)
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"first","name":"First"},
	  {"id":"in-review","name":"In Review"}],"prFlow":{"requested":"in-review"}}`), 0o644)
	if _, _, err := Load(dir); err == nil {
		t.Fatal("prFlow.requested without autoSpawn accepted")
	}
}

func TestChangesRequestedMode(t *testing.T) {
	cases := []struct {
		name    string
		flow    PRFlow
		propose bool
		message string
	}{
		{"unset proposes", PRFlow{}, true, proposeChangesMessage},
		{"propose", PRFlow{ChangesRequestedMode: "propose"}, true, proposeChangesMessage},
		{"fix", PRFlow{ChangesRequestedMode: "fix"}, false, fixChangesMessage},
		{"own message with unset mode keeps fixing", PRFlow{ChangesRequestedMessage: "mine"}, false, "mine"},
		{"own message with propose mode", PRFlow{ChangesRequestedMessage: "mine", ChangesRequestedMode: "propose"}, true, "mine"},
	}
	for _, c := range cases {
		if got := c.flow.Proposes(); got != c.propose {
			t.Errorf("%s: Proposes() = %v", c.name, got)
		}
		if got := c.flow.ChangesMessage(); got != c.message {
			t.Errorf("%s: ChangesMessage() = %q", c.name, got)
		}
	}
	if !Default().PRFlow.Proposes() {
		t.Error("default board does not propose")
	}
	var nilFlow *PRFlow
	if !nilFlow.Proposes() || nilFlow.ChangesMessage() != proposeChangesMessage {
		t.Error("a missing prFlow does not propose")
	}
	dir := t.TempDir()
	for mode, valid := range map[string]bool{"propose": true, "fix": true, "auto": false} {
		os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"first","name":"First"}],
		  "prFlow":{"changesRequestedMode":"`+mode+`"}}`), 0o644)
		board, _, err := Load(dir)
		if valid != (err == nil) {
			t.Fatalf("mode %q: err = %v", mode, err)
		}
		if valid && board.PRFlow.ChangesRequestedMode != mode {
			t.Errorf("mode %q loaded as %q", mode, board.PRFlow.ChangesRequestedMode)
		}
	}
}

func TestKeepAgent(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, column string
		ok           bool
	}{
		{"plain column", `{"id":"in-review","name":"In review","keepAgent":true}`, true},
		{"autoSpawn", `{"id":"in-review","name":"In review","autoSpawn":true,"keepAgent":true}`, false},
		{"done", `{"id":"done","name":"Done","role":"done","keepAgent":true}`, false},
	} {
		os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[{"id":"first","name":"First"},`+tc.column+`]}`), 0o644)
		board, _, err := Load(dir)
		if (err == nil) != tc.ok {
			t.Fatalf("%s: loaded = %v, want %v (%v)", tc.name, err == nil, tc.ok, err)
		}
		if tc.ok && !board.Columns[1].KeepAgent {
			t.Errorf("%s: keepAgent not loaded", tc.name)
		}
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

func TestLegacyTodoColumnDropped(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, FileName), []byte(`{"columns":[
	  {"id":"backlog","name":"Backlog","role":"todo"},
	  {"id":"planning","name":"Planning","autoSpawn":true}
	]}`), 0o644)
	board, _, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if board.FirstColumn().ID != "planning" || !slices.Equal(board.LegacyTodo, []string{"backlog", "todo"}) {
		t.Errorf("columns = %+v, legacy = %v; want Planning first and backlog dropped", board.Columns, board.LegacyTodo)
	}
	if err := board.Save(root); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(filepath.Join(root, FileName))
	if strings.Contains(string(saved), "todo") || strings.Contains(string(saved), "Backlog") {
		t.Errorf("saved board still holds the To Do column: %s", saved)
	}

	os.WriteFile(filepath.Join(root, FileName), []byte(`{"columns":[{"id":"backlog","name":"Backlog","role":"todo"}]}`), 0o644)
	if _, _, err := Load(root); err == nil {
		t.Error("a board with only a To Do column loaded")
	}
}

func TestLoadNoWorktreeColumn(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"columns":[
	  {"id":"plan","name":"Planning","autoSpawn":true,"noWorktree":true},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`), 0o644)
	board, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !board.Columns[0].NoWorktree || board.Columns[1].NoWorktree {
		t.Errorf("NoWorktree = %v, %v; want true, false", board.Columns[0].NoWorktree, board.Columns[1].NoWorktree)
	}
}

func TestDefaultModelFillsColumns(t *testing.T) {
	for _, tc := range []struct {
		name, board, want string
	}{
		{"unset", ``, DefaultAgentModel},
		{"board default", `,"defaultModel":"sonnet"`, "sonnet"},
	} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, FileName), []byte(`{"columns":[
		  {"id":"first","name":"First"},
		  {"id":"planning","name":"Planning","autoSpawn":true},
		  {"id":"executing","name":"Executing","autoSpawn":true,"modelOverride":"opus"}
		]`+tc.board+`}`), 0o644)
		board, _, err := Load(root)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := board.Columns[1].ModelOverride; got != tc.want {
			t.Errorf("%s: planning model = %q, want %q", tc.name, got, tc.want)
		}
		if got := board.Columns[2].ModelOverride; got != "opus" {
			t.Errorf("%s: executing model = %q, want its own override opus", tc.name, got)
		}
		if got := board.FirstColumn().ModelOverride; got != tc.want {
			t.Errorf("%s: first column model = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// loadJSON loads a board from drover.json and drover.local.json holding shared
// and local. An empty one leaves its file out; with no drover.json the default
// board loads.
func loadJSON(t *testing.T, shared, local string) (*Board, error) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{FileName: shared, LocalFileName: local} {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	board, _, err := Load(dir)
	return board, err
}

func TestDefaultBoard(t *testing.T) {
	board, found, err := Load(t.TempDir())
	if err != nil || found || len(board.Columns) == 0 {
		t.Fatalf("got found=%v err=%v cols=%d", found, err, len(board.Columns))
	}
	if first := board.FirstColumn(); first.ID != "planning" || !first.IsAgent() {
		t.Errorf("first column = %+v, want the Planning agent column", first)
	}
	if !slices.Equal(board.LegacyTodo, []string{"todo"}) {
		t.Errorf("default board's legacy ids = %v, want just the old default id", board.LegacyTodo)
	}
	for _, c := range board.Columns {
		if want := c.ID == "planning"; c.NoWorktree != want {
			t.Errorf("column %q NoWorktree = %v, want %v", c.ID, c.NoWorktree, want)
		}
	}
	if err := Default().validate(); err != nil {
		t.Errorf("default board: %v", err)
	}

	col, _, ok := Default().Column("executing")
	if !ok || len(col.Automations.OnTurnEnd) != 1 {
		t.Fatalf("default executing column has no turn-end rule: %+v", col)
	}
	row := col.Automations.OnTurnEnd[0]
	if row.Type != "move_to_column" || row.TargetColumn != "waiting-for-me" || row.Condition == "" {
		t.Errorf("default turn-end rule: %+v", row)
	}
}

func TestExecutingWaitsForYou(t *testing.T) {
	const executing = `{"id":"executing","name":"Executing","autoSpawn":true`
	const waitingForMe = `{"id":"waiting-for-me","name":"Waiting for me"}`
	cases := []struct {
		name, columns, local, wantName string
		wantRows                       int
		matchesDefault                 bool
	}{
		{"opted out", executing + `,"automations":{"onTurnEnd":[]}}, ` + waitingForMe, "", "", 0, false},
		{"custom rows", executing + `,"automations":{"onTurnEnd":[{"name":"Apply","type":"send_message","message":"Apply them."}]}}, ` + waitingForMe, "", "Apply", 1, false},
		{"null", executing + `,"automations":{"onTurnEnd":null}}, ` + waitingForMe, "", "Waiting for me", 1, false},
		{"no waiting-for-me", executing + `}`, "", "", 0, false},
		{"unset gets the default row", executing + `}, ` + waitingForMe, "", "Waiting for me", 1, true},
		{"a local overlay keeps it", executing + `}, ` + waitingForMe, `{"maxConcurrent":2}`, "Waiting for me", 1, true},
	}
	for _, tc := range cases {
		board, err := loadJSON(t, `{"columns":[`+tc.columns+`]}`, tc.local)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		col, _, _ := board.Column("executing")
		rows := col.Automations.OnTurnEnd
		if len(rows) != tc.wantRows {
			t.Errorf("%s: turn-end rows = %d, want %d", tc.name, len(rows), tc.wantRows)
		}
		if tc.wantRows > 0 && rows[0].Name != tc.wantName {
			t.Errorf("%s: first turn-end row = %q, want %q", tc.name, rows[0].Name, tc.wantName)
		}
		if want, _, _ := Default().Column("executing"); tc.matchesDefault && !reflect.DeepEqual(rows, want.Automations.OnTurnEnd) {
			t.Errorf("%s: turn-end rows = %+v, want the default %+v", tc.name, rows, want.Automations.OnTurnEnd)
		}
	}
}

func TestLocalOverlay(t *testing.T) {
	const done = `{"columns":[{"id":"done","name":"Done","role":"done"}]}`
	cases := []struct {
		name, shared, local string
		check               func(t *testing.T, b *Board)
	}{
		{"shared settings without a local file",
			`{"maxConcurrent":4,"notifications":{"turnDone":false},"columns":[{"id":"first","name":"First"}]}`, "",
			func(t *testing.T, b *Board) {
				want := DefaultNotifications()
				want.TurnDone = false
				if b.Notifications != want || b.Limit() != 4 || !b.AutoName {
					t.Errorf("notifications=%+v limit=%d", b.Notifications, b.Limit())
				}
			}},
		{"personal settings override the shared file",
			`{"maxConcurrent":4,"notifications":{"turnDone":false},"columns":[{"id":"first","name":"First"}]}`,
			`{"maxConcurrent":2,"idleTimeoutMinutes":30,"notifications":{"exited":false,"cooldownSeconds":60}}`,
			func(t *testing.T, b *Board) {
				want := DefaultNotifications()
				want.TurnDone, want.Exited, want.CooldownSeconds = false, false, 60
				if b.Notifications != want || b.Limit() != 2 || b.IdleTimeoutMinutes != 30 {
					t.Errorf("notifications=%+v limit=%d idle=%d", b.Notifications, b.Limit(), b.IdleTimeoutMinutes)
				}
			}},
		{"overrides apply to the default board", "", `{"notifications":{"needsYou":false},"autoName":false}`,
			func(t *testing.T, b *Board) {
				if b.Notifications.NeedsYou || !b.Notifications.TurnDone || b.Limit() != DefaultMaxConcurrent || b.AutoName {
					t.Errorf("notifications=%+v limit=%d", b.Notifications, b.Limit())
				}
			}},
		{"splitByPR is on for the default board", "", "",
			func(t *testing.T, b *Board) {
				if !b.SplitByPR {
					t.Error("default board does not split by PR")
				}
			}},
		{"splitByPR is on when drover.json omits it", done, "",
			func(t *testing.T, b *Board) {
				if !b.SplitByPR {
					t.Error("drover.json without splitByPR turns splitting off")
				}
			}},
		{"shared splitByPR false is kept", `{"splitByPR":false,"columns":[{"id":"done","name":"Done","role":"done"}]}`, "",
			func(t *testing.T, b *Board) {
				if b.SplitByPR {
					t.Error("shared splitByPR:false is ignored")
				}
			}},
		{"local splitByPR true overrides the shared file", `{"splitByPR":false,"columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"splitByPR":true}`,
			func(t *testing.T, b *Board) {
				if !b.SplitByPR {
					t.Error("local splitByPR:true does not override the shared file")
				}
			}},
		{"a broken local file is an error", "", `{"maxConcurrent":`, nil},
		{"a bad local notifications value is an error", "", `{"notifications":5}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			board, err := loadJSON(t, tc.shared, tc.local)
			if tc.check == nil {
				if err == nil {
					t.Error("want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, board)
		})
	}
}

func TestStamp(t *testing.T) {
	dir := t.TempDir()
	empty := Stamp(dir)
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	shared := Stamp(dir)
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(`{"maxConcurrent":2}`), 0o644)
	both := Stamp(dir)
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(`{"maxConcurrent":20}`), 0o644)
	resized := Stamp(dir)
	if empty == shared || shared == both || both == resized {
		t.Errorf("stamps should all differ: %q %q %q %q", empty, shared, both, resized)
	}
	if Stamp(dir) != resized {
		t.Error("stamp changed without the files changing")
	}
}

func TestIsDone(t *testing.T) {
	for role, want := range map[string]bool{"done": true, "": false, "todo": false, "Done": false} {
		if got := (Column{Role: role}).IsDone(); got != want {
			t.Errorf("Column{Role: %q}.IsDone() = %v, want %v", role, got, want)
		}
	}
}

func TestFind(t *testing.T) {
	board := &Board{Columns: []Column{
		{ID: "a", Name: "Backlog"},
		{ID: "in-review", Name: "In Review"},
		{ID: "x", Name: "a"},
	}}
	for _, tc := range []struct {
		ref, wantID string
	}{
		{"in-review", "in-review"},
		{"In Review", "in-review"},
		{"in review", "in-review"},
		{"BACKLOG", "a"},
		{"a", "a"},
		{"missing", ""},
		{"", ""},
	} {
		col, ok := board.Find(tc.ref)
		if col.ID != tc.wantID || ok != (tc.wantID != "") {
			t.Errorf("Find(%q) = %q, %v, want %q", tc.ref, col.ID, ok, tc.wantID)
		}
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	if err := Default().Save(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil || !strings.HasSuffix(string(data), "}\n") {
		t.Fatalf("saved file = %q, err %v", data, err)
	}
	board, found, err := Load(dir)
	if err != nil || !found {
		t.Fatalf("saved board does not load: found=%v err=%v", found, err)
	}
	var ids []string
	for _, c := range board.Columns {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []string{"planning", "executing", "waiting-for-me", "in-review", "done"}) || board.PRFlow == nil || board.ArchiveDoneAfter != "7d" {
		t.Errorf("round trip lost data: ids=%v flow=%+v archive=%q", ids, board.PRFlow, board.ArchiveDoneAfter)
	}
	if err := Default().Save(filepath.Join(dir, "missing")); err == nil {
		t.Error("saving into a missing directory succeeded")
	}
}

func TestLoadRejectsAnInvalidBoard(t *testing.T) {
	cases := []struct {
		name, shared, wantErr string
	}{
		{"not json", `{"columns":`, "parse drover.json"},
		{"no columns", `{"columns":[]}`, "has no columns"},
		{"missing columns key", `{}`, "has no columns"},
		{"column without an id", `{"columns":[{"name":"First"}]}`, "needs an id and a name"},
		{"column without a name", `{"columns":[{"id":"first"}]}`, "needs an id and a name"},
		{"duplicate column ids", `{"columns":[{"id":"a","name":"A"},{"id":"a","name":"B"}]}`, "duplicate column id a"},
		{"bad archiveDoneAfter", `{"archiveDoneAfter":"0d","columns":[{"id":"a","name":"A"}]}`, "archiveDoneAfter"},
		{"unknown prFlow column", `{"columns":[{"id":"a","name":"A"}],"prFlow":{"merged":"nowhere"}}`, `unknown column "nowhere"`},
	}
	for _, tc := range cases {
		if _, err := loadJSON(t, tc.shared, ""); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.wantErr)
		}
	}

	if board, err := loadJSON(t, `{"columns":[{"id":"a","name":"A"}]}`, ""); err != nil || len(board.Columns) != 1 {
		t.Errorf("a board without a done column: %+v, %v", board, err)
	}

	for _, name := range []string{FileName, LocalFileName} {
		dir := t.TempDir()
		os.Mkdir(filepath.Join(dir, name), 0o755)
		if _, _, err := Load(dir); err == nil {
			t.Errorf("an unreadable %s loaded", name)
		}
	}
}
