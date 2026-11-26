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
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	writeLayer(t, dir, Project, `{"defaultBaseBranch":"develop"}`)

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
		t.Errorf("project override not applied: %q", board.DefaultBaseBranch)
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
		dir := testRoot(t)
		writeLayer(t, dir, Global, `{"columns":[{"id":"first","name":"First"},
		  {"id":"waiting","name":"Waiting"}],"reviewFlow":`+flow+`}`)
		board, _, err := Load(dir)
		return board, err
	}
	for _, field := range []string{"requested", "started", "reviewed", "approved", "updated", "done"} {
		if _, err := load(`{"` + field + `":"nowhere"}`); err == nil {
			t.Errorf("unknown reviewFlow.%s column accepted", field)
		}
	}
	board, err := load(`{"started":"Waiting","reviewed":"waiting","approved":"first"}`)
	if err != nil || board.ReviewFlow == nil || board.ReviewFlow.Started != "Waiting" || board.ReviewFlow.Reviewed != "waiting" || board.ReviewFlow.Approved != "first" {
		t.Fatalf("reviewFlow not loaded: %+v %v", board.ReviewFlow, err)
	}
	if _, err := load(`{}`); err != nil {
		t.Errorf("empty reviewFlow rejected: %v", err)
	}
}

func TestWorktreesPath(t *testing.T) {
	dir := testRoot(t)
	board := `{"columns":[{"id":"first","name":"First"}]`
	for _, tc := range []struct {
		name, main, project, want string
	}{
		{"default", ``, ``, filepath.Join(dir, ".drover", "worktrees")},
		{"relative", `,"worktreesDir":".claude/worktrees"`, ``, filepath.Join(dir, ".claude", "worktrees")},
		{"absolute", `,"worktreesDir":"/var/wt/"`, ``, "/var/wt"},
		{"project wins", `,"worktreesDir":"a"`, `{"worktreesDir":"b"}`, filepath.Join(dir, "b")},
		{"absent project key keeps global", `,"worktreesDir":"a"`, `{"maxConcurrent":2}`, filepath.Join(dir, "a")},
	} {
		writeLayer(t, dir, Global, board+tc.main+"}")
		os.Remove(Project.Path(dir))
		if tc.project != "" {
			writeLayer(t, dir, Project, tc.project)
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
	dir := testRoot(t)
	for _, bad := range []string{".", "..", "./", dir, filepath.Dir(dir), "/"} {
		writeLayer(t, dir, Global, `{"worktreesDir":`+strconv.Quote(bad)+`,"columns":[{"id":"first","name":"First"}]}`)
		if _, _, err := Load(dir); err == nil {
			t.Errorf("worktreesDir %q accepted", bad)
		}
	}
}

func TestPRFlow(t *testing.T) {
	if err := Default().validate(); err != nil {
		t.Fatalf("default board: %v", err)
	}
	dir := testRoot(t)
	writeLayer(t, dir, Global, `{"columns":[{"id":"first","name":"First"}],
	  "prFlow":{"opened":"In Review"}}`)
	if _, _, err := Load(dir); err == nil {
		t.Fatal("unknown prFlow column accepted")
	}
	writeLayer(t, dir, Global, `{"columns":[{"id":"first","name":"First"},
	  {"id":"in-review","name":"In Review"}],"prFlow":{"opened":"in review"}}`)
	board, _, err := Load(dir)
	if err != nil || board.PRFlow == nil || board.PRFlow.Opened != "in review" {
		t.Fatalf("prFlow not loaded: %+v %v", board.PRFlow, err)
	}
	writeLayer(t, dir, Global, `{"columns":[{"id":"first","name":"First"},
	  {"id":"in-review","name":"In Review"}],"prFlow":{"requested":"in-review"}}`)
	if _, _, err := Load(dir); err == nil {
		t.Fatal("prFlow.requested without autoSpawn accepted")
	}
}

func TestChangesRequestedMode(t *testing.T) {
	const custom = "my own instructions"
	proposal := (&PRFlow{}).ChangesMessage()
	fix := (&PRFlow{ChangesRequestedMode: "fix"}).ChangesMessage()
	if proposal == fix {
		t.Fatal("proposing and fixing send the agent the same message")
	}
	if !strings.Contains(proposal, "Don't change anything") {
		t.Errorf("the proposal message doesn't hold the agent back: %q", proposal)
	}
	cases := []struct {
		name    string
		flow    PRFlow
		propose bool
		want    string
	}{
		{"unset proposes", PRFlow{}, true, proposal},
		{"propose", PRFlow{ChangesRequestedMode: "propose"}, true, proposal},
		{"fix", PRFlow{ChangesRequestedMode: "fix"}, false, fix},
		{"off", PRFlow{ChangesRequestedMode: "off"}, false, fix},
		{"own message with off mode", PRFlow{ChangesRequestedMessage: custom, ChangesRequestedMode: "off"}, false, custom},
		{"own message with unset mode keeps fixing", PRFlow{ChangesRequestedMessage: custom}, false, custom},
		{"own message with propose mode", PRFlow{ChangesRequestedMessage: custom, ChangesRequestedMode: "propose"}, true, custom},
	}
	for _, c := range cases {
		if got := c.flow.Proposes(); got != c.propose {
			t.Errorf("%s: Proposes() = %v", c.name, got)
		}
		if got := c.flow.ChangesMessage(); got != c.want {
			t.Errorf("%s: ChangesMessage() = %q, want %q", c.name, got, c.want)
		}
	}
	if !Default().PRFlow.Proposes() {
		t.Error("default board does not propose")
	}
	var nilFlow *PRFlow
	if nilFlow.Off() || (&PRFlow{ChangesRequestedMode: "fix"}).Off() || (&PRFlow{}).Off() {
		t.Error("Off() is true outside off mode")
	}
	if !(&PRFlow{ChangesRequestedMode: "off"}).Off() {
		t.Error("Off() is false in off mode")
	}
	if !nilFlow.Proposes() || nilFlow.ChangesMessage() != proposal {
		t.Error("a missing prFlow does not propose")
	}
	dir := testRoot(t)
	for mode, valid := range map[string]bool{"propose": true, "fix": true, "off": true, "auto": false} {
		writeLayer(t, dir, Global, `{"columns":[{"id":"first","name":"First"}],
		  "prFlow":{"changesRequestedMode":"`+mode+`"}}`)
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
	dir := testRoot(t)
	for _, tc := range []struct {
		name, column string
		ok           bool
	}{
		{"plain column", `{"id":"in-review","name":"In review","keepAgent":true}`, true},
		{"autoSpawn", `{"id":"in-review","name":"In review","autoSpawn":true,"keepAgent":true}`, false},
		{"done", `{"id":"done","name":"Done","role":"done","keepAgent":true}`, false},
	} {
		writeLayer(t, dir, Global, `{"columns":[{"id":"first","name":"First"},`+tc.column+`]}`)
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
	root := testRoot(t)
	write := func(after string) {
		writeLayer(t, root, Global, `{"version":1,`+after+`"columns":[{"id":"done","name":"Done","role":"done"}]}`)
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
	root := testRoot(t)
	writeLayer(t, root, Global, `{"columns":[
	  {"id":"backlog","name":"Backlog","role":"todo"},
	  {"id":"planning","name":"Planning","autoSpawn":true}
	]}`)
	board, _, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if board.FirstColumn().ID != "planning" || !slices.Equal(board.LegacyTodo, []string{"backlog", "todo"}) {
		t.Errorf("columns = %+v, legacy = %v; want Planning first and backlog dropped", board.Columns, board.LegacyTodo)
	}

	writeLayer(t, root, Global, `{"columns":[{"id":"backlog","name":"Backlog","role":"todo"}]}`)
	if _, _, err := Load(root); err == nil {
		t.Error("a board with only a To Do column loaded")
	}
}

func TestLoadNoWorktreeColumn(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, `{"columns":[
	  {"id":"plan","name":"Planning","autoSpawn":true,"noWorktree":true},
	  {"id":"exec","name":"Executing","autoSpawn":true},
	  {"id":"done","name":"Done","role":"done"}]}`)
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
		root := testRoot(t)
		writeLayer(t, root, Global, `{"columns":[
		  {"id":"first","name":"First"},
		  {"id":"planning","name":"Planning","autoSpawn":true},
		  {"id":"executing","name":"Executing","autoSpawn":true,"modelOverride":"opus"}
		]`+tc.board+`}`)
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

// loadJSON loads a board from the global and project config files holding
// global and project. An empty one leaves its file out; with neither the
// default board loads.
func loadJSON(t *testing.T, global, project string) (*Board, error) {
	t.Helper()
	dir := testRoot(t)
	for layer, content := range map[Layer]string{Global: global, Project: project} {
		if content != "" {
			writeLayer(t, dir, layer, content)
		}
	}
	board, _, err := Load(dir)
	return board, err
}

func TestDefaultBoard(t *testing.T) {
	board, found, err := Load(testRoot(t))
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
	col, _, ok := Default().Column("executing")
	if !ok || len(col.Automations.OnTurnEnd) != 1 {
		t.Fatalf("default executing column has no turn-end rule: %+v", col)
	}
	row := col.Automations.OnTurnEnd[0]
	if row.Type != "move_to_column" || row.TargetColumn != "waiting-for-me" || row.Condition == "" {
		t.Errorf("default turn-end rule: %+v", row)
	}
}

func TestDefaultBoardHasReviewLane(t *testing.T) {
	board := Default()
	var lane []string
	for _, c := range board.Columns {
		if c.InReviewLane() {
			lane = append(lane, c.ID)
		}
	}
	if want := []string{"review-requested", "reviewing", "triage", "waiting-on-author", "review-done"}; !slices.Equal(lane, want) {
		t.Errorf("review lane = %v, want %v", lane, want)
	}
	f := board.ReviewFlow
	if f == nil {
		t.Fatal("default board has no reviewFlow")
	}
	for name, ref := range map[string]string{"requested": f.Requested, "started": f.Started, "reviewed": f.Reviewed, "approved": f.Approved, "updated": f.Updated, "done": f.Done} {
		if _, ok := board.Find(ref); !ok {
			t.Errorf("reviewFlow.%s = %q names no column", name, ref)
		}
	}
	if c, _ := board.Find(f.Approved); !c.IsDone() {
		t.Errorf("reviewFlow.approved = %q is not a Done column", f.Approved)
	}
	if c, _ := board.Find(f.Done); !c.IsDone() {
		t.Errorf("reviewFlow.done = %q is not a Done column", f.Done)
	}
	c, _ := board.Find(f.Updated)
	if len(c.Automations.OnEnter) != 1 || !strings.Contains(c.Automations.OnEnter[0].Message, "{{prUrl}}") {
		t.Errorf("reviewFlow.updated column doesn't ask for a re-review on entry: %+v", c.Automations)
	}
	if len(c.Automations.OnTurnEnd) != 1 || c.Automations.OnTurnEnd[0].Type != "move_to_column" || c.Automations.OnTurnEnd[0].TargetColumn != "triage" {
		t.Errorf("reviewFlow.updated column doesn't move on to triage when the re-review ends: %+v", c.Automations)
	}
	if c, _ := board.Find(f.Requested); c.IsAgent() || !c.InReviewLane() {
		t.Errorf("reviewFlow.requested = %q should be a plain column in the lane: %+v", f.Requested, c)
	}
}

func TestExecutingWaitsForYou(t *testing.T) {
	const executing = `{"id":"executing","name":"Executing","autoSpawn":true`
	const waitingForMe = `{"id":"waiting-for-me","name":"Waiting for me"}`
	cases := []struct {
		name, columns, local, wantName string
		wantRows                       int
		matchesDefault                 bool
		wantTarget                     string
	}{
		{"opted out", executing + `,"automations":{"onTurnEnd":[]}}, ` + waitingForMe, "", "", 0, false, ""},
		{"custom rows", executing + `,"automations":{"onTurnEnd":[{"name":"Apply","type":"send_message","message":"Apply them."}]}}, ` + waitingForMe, "", "Apply", 1, false, ""},
		{"null", executing + `,"automations":{"onTurnEnd":null}}, ` + waitingForMe, "", "Waiting for me", 1, false, ""},
		{"no waiting-for-me", executing + `}`, "", "", 0, false, ""},
		{"a plain waiting column", executing + `}, {"id":"waiting","name":"Waiting"}`, "", "Waiting for me", 1, false, "waiting"},
		{"a Waiting for me name under another id", executing + `}, {"id":"parked","name":"Waiting for me"}`, "", "Waiting for me", 1, false, "parked"},
		{"waiting-for-me wins over waiting", executing + `}, {"id":"waiting","name":"Waiting"}, ` + waitingForMe, "", "Waiting for me", 1, false, "waiting-for-me"},
		{"a Waiting for me name wins over waiting", executing + `}, {"id":"waiting","name":"Waiting"}, {"id":"parked","name":"Waiting for me"}`, "", "Waiting for me", 1, false, "parked"},
		{"unset gets the default row", executing + `}, ` + waitingForMe, "", "Waiting for me", 1, true, ""},
		{"a local overlay keeps it", executing + `}, ` + waitingForMe, `{"maxConcurrent":2}`, "Waiting for me", 1, true, ""},
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
		if tc.wantTarget != "" && rows[0].TargetColumn != tc.wantTarget {
			t.Errorf("%s: turn-end target = %q, want %q", tc.name, rows[0].TargetColumn, tc.wantTarget)
		}
		if want, _, _ := Default().Column("executing"); tc.matchesDefault && !reflect.DeepEqual(rows, want.Automations.OnTurnEnd) {
			t.Errorf("%s: turn-end rows = %+v, want the default %+v", tc.name, rows, want.Automations.OnTurnEnd)
		}
	}
}

func TestReviewingTriages(t *testing.T) {
	const columns = `{"id":"first","name":"First"},
	  {"id":"triage","name":"Triage findings","lane":"review","autoSpawn":true},`
	const reviewing = `{"id":"reviewing","name":"Reviewing","lane":"review","autoSpawn":true`
	cases := []struct {
		name, board, wantRow, wantTarget string
	}{
		{"unset gets the row", `{"columns":[` + columns + reviewing + `}],"reviewFlow":{"started":"triage","updated":"reviewing"}}`, "Triage", "triage"},
		{"names resolve to ids", `{"columns":[` + columns + reviewing + `}],"reviewFlow":{"started":"Triage findings","updated":"Reviewing"}}`, "Triage", "triage"},
		{"opted out", `{"columns":[` + columns + reviewing + `,"automations":{"onTurnEnd":[]}}],"reviewFlow":{"started":"triage","updated":"reviewing"}}`, "", ""},
		{"custom rows", `{"columns":[` + columns + reviewing + `,"automations":{"onTurnEnd":[{"name":"Mine","type":"send_message","message":"x"}]}}],"reviewFlow":{"started":"triage","updated":"reviewing"}}`, "Mine", ""},
		{"updated is started", `{"columns":[` + columns + reviewing + `}],"reviewFlow":{"started":"reviewing","updated":"reviewing"}}`, "", ""},
		{"no started", `{"columns":[` + columns + reviewing + `}],"reviewFlow":{"updated":"reviewing"}}`, "", ""},
		{"no reviewFlow", `{"columns":[` + columns + reviewing + `}]}`, "", ""},
		{"plain updated column", `{"columns":[` + columns + `{"id":"reviewing","name":"Reviewing","lane":"review"}],"reviewFlow":{"started":"triage","updated":"reviewing"}}`, "", ""},
	}
	for _, tc := range cases {
		board, err := loadJSON(t, tc.board, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		col, _, _ := board.Column("reviewing")
		rows := col.Automations.OnTurnEnd
		if tc.wantRow == "" {
			if len(rows) != 0 {
				t.Errorf("%s: turn-end rows = %+v, want none", tc.name, rows)
			}
			continue
		}
		if len(rows) != 1 || rows[0].Name != tc.wantRow {
			t.Fatalf("%s: turn-end rows = %+v, want one %q", tc.name, rows, tc.wantRow)
		}
		if tc.wantTarget != "" && (rows[0].TargetColumn != tc.wantTarget || rows[0].ConditionScript != afterReview) {
			t.Errorf("%s: row = %+v, want a move to %q after a review", tc.name, rows[0], tc.wantTarget)
		}
	}
}

func TestLayers(t *testing.T) {
	const done = `{"columns":[{"id":"done","name":"Done","role":"done"}]}`
	cases := []struct {
		name, global, project string
		check                 func(t *testing.T, b *Board)
	}{
		{"global settings without a project file",
			`{"maxConcurrent":4,"notifications":{"turnDone":false},"columns":[{"id":"first","name":"First"}]}`, "",
			func(t *testing.T, b *Board) {
				want := DefaultNotifications()
				want.TurnDone = false
				if b.Notifications != want || b.Limit() != 4 || !b.AutoName {
					t.Errorf("notifications=%+v limit=%d", b.Notifications, b.Limit())
				}
			}},
		{"project settings override the global file",
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
		{"splitByPR is on when the config omits it", done, "",
			func(t *testing.T, b *Board) {
				if !b.SplitByPR {
					t.Error("a config without splitByPR turns splitting off")
				}
			}},
		{"global splitByPR false is kept", `{"splitByPR":false,"columns":[{"id":"done","name":"Done","role":"done"}]}`, "",
			func(t *testing.T, b *Board) {
				if b.SplitByPR {
					t.Error("global splitByPR:false is ignored")
				}
			}},
		{"project splitByPR true overrides the global file", `{"splitByPR":false,"columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"splitByPR":true}`,
			func(t *testing.T, b *Board) {
				if !b.SplitByPR {
					t.Error("project splitByPR:true does not override the global file")
				}
			}},
		{"disableMouse is off by default", "", "",
			func(t *testing.T, b *Board) {
				if b.DisableMouse {
					t.Error("default board disables the mouse")
				}
			}},
		{"project disableUpdateCheck true overrides the global file", `{"disableUpdateCheck":false,"columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"disableUpdateCheck":true}`,
			func(t *testing.T, b *Board) {
				if !b.DisableUpdateCheck {
					t.Error("project disableUpdateCheck:true does not override the global file")
				}
			}},
		{"issue tracker is Linear by default", "", "",
			func(t *testing.T, b *Board) {
				if b.Tracker() != TrackerLinear {
					t.Errorf("default tracker = %q", b.Tracker())
				}
			}},
		{"global issueTracker github", `{"issueTracker":"github","columns":[{"id":"done","name":"Done","role":"done"}]}`, "",
			func(t *testing.T, b *Board) {
				if b.Tracker() != TrackerGitHub {
					t.Errorf("tracker = %q", b.Tracker())
				}
			}},
		{"project issueTracker overrides the global file", `{"issueTracker":"github","columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"issueTracker":"linear"}`,
			func(t *testing.T, b *Board) {
				if b.Tracker() != TrackerLinear {
					t.Errorf("tracker = %q", b.Tracker())
				}
			}},
		{"project disableMouse true overrides the global file", `{"disableMouse":false,"columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"disableMouse":true}`,
			func(t *testing.T, b *Board) {
				if !b.DisableMouse {
					t.Error("project disableMouse:true does not override the global file")
				}
			}},
		{"team review requests are off by default", "", "",
			func(t *testing.T, b *Board) {
				if b.TeamReviewRequests || b.ReviewRepos != nil {
					t.Errorf("teamReviewRequests=%v reviewRepos=%q", b.TeamReviewRequests, b.ReviewRepos)
				}
			}},
		{"project review settings override the global file", `{"reviewRepos":["o/a"],"columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"teamReviewRequests":true,"reviewRepos":["o/b","o/c"]}`,
			func(t *testing.T, b *Board) {
				if !b.TeamReviewRequests || !slices.Equal(b.ReviewRepos, []string{"o/b", "o/c"}) {
					t.Errorf("teamReviewRequests=%v reviewRepos=%q", b.TeamReviewRequests, b.ReviewRepos)
				}
			}},
		{"global assistant MCP settings survive a project file without them", `{"assistantPlugins":["a"],"assistantTools":["mcp__a__*"],"columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"autoName":false}`,
			func(t *testing.T, b *Board) {
				if !slices.Equal(b.AssistantPlugins, []string{"a"}) || !slices.Equal(b.AssistantTools, []string{"mcp__a__*"}) {
					t.Errorf("plugins=%q tools=%q", b.AssistantPlugins, b.AssistantTools)
				}
			}},
		{"project empty assistant lists clear the global ones", `{"assistantPlugins":["a"],"assistantTools":["mcp__a__*"],"columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"assistantPlugins":[],"assistantTools":[]}`,
			func(t *testing.T, b *Board) {
				if len(b.AssistantPlugins) != 0 || len(b.AssistantTools) != 0 {
					t.Errorf("plugins=%q tools=%q", b.AssistantPlugins, b.AssistantTools)
				}
			}},
		{"project assistant MCP settings replace the global ones", `{"assistantPlugins":["a"],"assistantTools":["mcp__a__*"],"columns":[{"id":"done","name":"Done","role":"done"}]}`, `{"assistantPlugins":["b"],"assistantTools":["mcp__b__*"]}`,
			func(t *testing.T, b *Board) {
				if !slices.Equal(b.AssistantPlugins, []string{"b"}) || !slices.Equal(b.AssistantTools, []string{"mcp__b__*"}) {
					t.Errorf("plugins=%q tools=%q", b.AssistantPlugins, b.AssistantTools)
				}
			}},
		{"project columns replace the global ones",
			`{"columns":[{"id":"a","name":"A"},{"id":"b","name":"B"}],"prFlow":{"opened":"b"}}`,
			`{"columns":[{"id":"b","name":"B2"}]}`,
			func(t *testing.T, b *Board) {
				if len(b.Columns) != 1 || b.Columns[0].Name != "B2" {
					t.Errorf("columns = %+v", b.Columns)
				}
				if b.PRFlow == nil || b.PRFlow.Opened != "b" {
					t.Errorf("flow was not kept from the global config: %+v", b.PRFlow)
				}
			}},
		{"a project column does not inherit fields of the global column with its id",
			`{"columns":[{"id":"a","name":"A","color":"#111","autoSpawn":true}]}`,
			`{"columns":[{"id":"a","name":"A2"}]}`,
			func(t *testing.T, b *Board) {
				if a := b.Columns[0]; a.Name != "A2" || a.Color != "" || a.AutoSpawn {
					t.Errorf("a = %+v", a)
				}
			}},
		{"project flows replace the global ones",
			`{"columns":[{"id":"a","name":"A"},{"id":"b","name":"B"}],"prFlow":{"opened":"a","merged":"b"}}`,
			`{"prFlow":{"opened":"b"}}`,
			func(t *testing.T, b *Board) {
				if b.PRFlow.Opened != "b" || b.PRFlow.Merged != "" {
					t.Errorf("prFlow = %+v", b.PRFlow)
				}
			}},
		{"project scalars override the global ones and absent keys keep them",
			`{"maxConcurrent":4,"defaultModel":"opus","columns":[{"id":"a","name":"A"}]}`,
			`{"defaultModel":"haiku","initScript":"make"}`,
			func(t *testing.T, b *Board) {
				if b.Limit() != 4 || b.DefaultModel != "haiku" || b.InitScript != "make" {
					t.Errorf("limit=%d model=%q init=%q", b.Limit(), b.DefaultModel, b.InitScript)
				}
			}},
		{"settings without columns keep the default board", `{"maxConcurrent":3}`, "",
			func(t *testing.T, b *Board) {
				if b.Limit() != 3 || len(b.Columns) != len(Default().Columns) || b.PRFlow == nil || b.ArchiveDoneAfter != "7d" {
					t.Errorf("limit=%d columns=%d prFlow=%v archive=%q", b.Limit(), len(b.Columns), b.PRFlow, b.ArchiveDoneAfter)
				}
			}},
		{"a custom board without archiveDoneAfter never archives", done, "",
			func(t *testing.T, b *Board) {
				if b.ArchiveAfter() != 0 {
					t.Errorf("archive = %v", b.ArchiveAfter())
				}
			}},
		{"project columns that aren't a list are an error", "", `{"columns":{}}`, nil},
		{"a project column setting of the wrong type is an error", "", `{"columns":[{"id":"a","color":5}]}`, nil},
		{"a broken project file is an error", "", `{"maxConcurrent":`, nil},
		{"a bad project notifications value is an error", "", `{"notifications":5}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			board, err := loadJSON(t, tc.global, tc.project)
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
	dir := testRoot(t)
	empty := Stamp(dir)
	writeLayer(t, dir, Global, sample)
	shared := Stamp(dir)
	writeLayer(t, dir, Project, `{"maxConcurrent":2}`)
	both := Stamp(dir)
	writeLayer(t, dir, Project, `{"maxConcurrent":20}`)
	resized := Stamp(dir)
	if empty == shared || shared == both || both == resized {
		t.Errorf("stamps should all differ: %q %q %q %q", empty, shared, both, resized)
	}
	if Stamp(dir) != resized {
		t.Error("stamp changed without the files changing")
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

func TestClaudeEnv(t *testing.T) {
	if got := (&Board{DisableMouse: true}).ClaudeEnv(); !reflect.DeepEqual(got, map[string]string{"CLAUDE_CODE_DISABLE_MOUSE": "1"}) {
		t.Errorf("disableMouse env = %v", got)
	}
	var none *Board
	for name, b := range map[string]*Board{"off": {}, "no board": none} {
		if got := b.ClaudeEnv(); len(got) != 0 {
			t.Errorf("%s: env = %v, want none", name, got)
		}
	}
}

func TestLoadRejectsAnInvalidBoard(t *testing.T) {
	cases := []struct {
		name, shared, wantErr string
	}{
		{"not json", `{"columns":`, "config.json"},
		{"no columns", `{"columns":[]}`, "has no columns"},
		{"column without an id", `{"columns":[{"name":"First"}]}`, "needs an id and a name"},
		{"column without a name", `{"columns":[{"id":"first"}]}`, "needs an id and a name"},
		{"duplicate column ids", `{"columns":[{"id":"a","name":"A"},{"id":"a","name":"B"}]}`, "duplicate column id a"},
		{"bad archiveDoneAfter", `{"archiveDoneAfter":"0d","columns":[{"id":"a","name":"A"}]}`, "archiveDoneAfter"},
		{"unknown quickWorkdir", `{"quickWorkdir":"tmp","columns":[{"id":"a","name":"A"}]}`, `quickWorkdir must be "worktree" or "root"`},
		{"unknown issueTracker", `{"issueTracker":"jira","columns":[{"id":"a","name":"A"}]}`, `issueTracker must be "linear" or "github"`},
		{"assistantTools outside mcp__", `{"assistantTools":["Edit"],"columns":[{"id":"a","name":"A"}]}`, `assistantTools entry "Edit"`},
		{"reviewRepos without an owner", `{"reviewRepos":["widgets"],"columns":[{"id":"a","name":"A"}]}`, `reviewRepos entry "widgets"`},
		{"reviewRepos as a URL", `{"reviewRepos":["https://github.com/o/r"],"columns":[{"id":"a","name":"A"}]}`, `reviewRepos entry "https://github.com/o/r"`},
		{"bare mcp__ assistant tool", `{"assistantTools":["mcp__"],"columns":[{"id":"a","name":"A"}]}`, `assistantTools entry "mcp__"`},
		{"empty assistant plugin id", `{"assistantPlugins":[" "],"columns":[{"id":"a","name":"A"}]}`, "assistantPlugins"},
		{"unknown lane", `{"columns":[{"id":"a","name":"A"},{"id":"b","name":"B","lane":"side"}]}`, `unknown lane "side"`},
		{"first column in the review lane", `{"columns":[{"id":"a","name":"A","lane":"review"}]}`, "first column must not be in the review lane"},
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

	for _, layer := range []Layer{Global, Project} {
		dir := testRoot(t)
		os.MkdirAll(layer.Path(dir), 0o755)
		if _, _, err := Load(dir); err == nil {
			t.Errorf("an unreadable %s config loaded", layer)
		}
	}
}

func TestDoneColumn(t *testing.T) {
	b := &Board{Columns: []Column{{ID: "a", Name: "A"}, {ID: "d1", Name: "D1", Role: "done"}, {ID: "d2", Name: "D2", Role: "done"}}}
	if got := b.DoneColumn().ID; got != "d1" {
		t.Errorf("DoneColumn = %q, want the first done column d1", got)
	}
	if got := (&Board{Columns: []Column{{ID: "a"}}}).DoneColumn().ID; got != "" {
		t.Errorf("DoneColumn without a done column = %q, want none", got)
	}
}

func TestReviewEntry(t *testing.T) {
	load := func(board string) *Board {
		dir := testRoot(t)
		writeLayer(t, dir, Global, board)
		b, _, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	const columns = `"columns":[{"id":"first","name":"First"},
	  {"id":"reviewing","name":"Reviewing","lane":"review","autoSpawn":true},
	  {"id":"triage","name":"Triage","lane":"review","autoSpawn":true}]`
	cases := []struct {
		name, board, want string
	}{
		{"reviewFlow.started is ignored", `{` + columns + `,"reviewFlow":{"started":"Triage"}}`, "reviewing"},
		{"started outside the lane", `{` + columns + `,"reviewFlow":{"started":"first"}}`, "reviewing"},
		{"no reviewFlow", `{` + columns + `}`, "reviewing"},
		{"plain column first", `{"columns":[{"id":"first","name":"First"},
		  {"id":"requested","name":"Requested","lane":"review"},
		  {"id":"reviewing","name":"Reviewing","lane":"review","autoSpawn":true}]}`, "reviewing"},
		{"no agent column", `{"columns":[{"id":"first","name":"First"},
		  {"id":"requested","name":"Requested","lane":"review"},
		  {"id":"waiting","name":"Waiting","lane":"review"}]}`, "requested"},
	}
	for _, c := range cases {
		got, ok := load(c.board).ReviewEntry()
		if !ok || got.ID != c.want {
			t.Errorf("%s: ReviewEntry = %q, %v; want %q", c.name, got.ID, ok, c.want)
		}
	}
	if got, ok := Default().ReviewEntry(); !ok || got.ID != "reviewing" {
		t.Errorf("default board ReviewEntry = %q, %v; want reviewing", got.ID, ok)
	}
	if _, ok := load(`{"columns":[{"id":"first","name":"First"},{"id":"done","name":"Done"}]}`).ReviewEntry(); ok {
		t.Error("a board without a review lane has an entry column")
	}
}

func TestReviewImport(t *testing.T) {
	load := func(board string) *Board {
		dir := testRoot(t)
		writeLayer(t, dir, Global, board)
		b, _, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	board := Default()
	if got := board.ReviewImport(false).ID; got != "review-requested" {
		t.Errorf("default board, requested: %q", got)
	}
	if got := board.ReviewImport(true).ID; got != "waiting-on-author" {
		t.Errorf("default board, reviewed: %q", got)
	}
	const lane = `{"id":"first","name":"First","autoSpawn":true},
	  {"id":"reviewing","name":"Reviewing","lane":"review","autoSpawn":true},
	  {"id":"parked","name":"Parked","lane":"review"},
	  {"id":"waiting","name":"Waiting","lane":"review"}`
	for _, c := range []struct {
		name, board string
		reviewed    bool
		want        string
	}{
		{"requested by name", `{"columns":[` + lane + `],"reviewFlow":{"requested":"Waiting"}}`, false, "waiting"},
		{"no requested target", `{"columns":[` + lane + `],"reviewFlow":{"started":"reviewing"}}`, false, "parked"},
		{"no reviewed target", `{"columns":[` + lane + `],"reviewFlow":{"requested":"waiting"}}`, true, "parked"},
		{"no reviewFlow", `{"columns":[` + lane + `]}`, false, "parked"},
		{"lane of agents and Done", `{"columns":[{"id":"first","name":"First"},
		  {"id":"reviewing","name":"Reviewing","lane":"review","autoSpawn":true},
		  {"id":"rd","name":"Done","lane":"review","role":"done"}]}`, false, "first"},
		{"no lane", `{"columns":[{"id":"first","name":"First"},{"id":"done","name":"Done","role":"done"}]}`, true, "first"},
	} {
		if got := load(c.board).ReviewImport(c.reviewed).ID; got != c.want {
			t.Errorf("%s: ReviewImport(%v) = %q, want %q", c.name, c.reviewed, got, c.want)
		}
	}
}

func TestReReviewOnlyAfterAReview(t *testing.T) {
	c, _, _ := Default().Column("reviewing")
	row := c.Automations.OnEnter[0]
	if row.ConditionScript != `test -n "$DROVER_PRREVIEWEDSHA"` || row.Message != reReviewMessage {
		t.Errorf("Reviewing's entry row: %+v", row)
	}
}
