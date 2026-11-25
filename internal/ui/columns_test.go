package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/config"
)

const columnsBoard = `{
  "columns": [
    {"id": "plan", "name": "Planning", "autoSpawn": true, "permissionMode": "plan", "planExitTarget": "Executing"},
    {"id": "run", "name": "Executing", "autoSpawn": true},
    {"id": "done", "name": "Done", "role": "done"}
  ]
}
`

func columnsModel(t *testing.T) (Model, string) {
	t.Helper()
	root := newRoot(t)
	if err := os.MkdirAll(filepath.Dir(config.GlobalPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GlobalPath(), []byte(columnsBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	return configModel(t, root), root
}

func pressColumns(t *testing.T, m Model, k tea.KeyMsg) Model {
	t.Helper()
	next, _ := m.updateColumns(k)
	return next.(Model)
}

func openColumn(t *testing.T, m Model, id, option string) Model {
	t.Helper()
	m = m.openColumns()
	found := false
	for i, c := range m.snap.Board.Columns {
		if c.ID == id {
			m.columnSel, found = i, true
		}
	}
	if !found {
		t.Fatalf("no column %q", id)
	}
	m = pressColumns(t, m, keyPress("enter"))
	m.columnOptSel = columnOptionIndex(t, option)
	return m
}

func columnOptionIndex(t *testing.T, name string) int {
	t.Helper()
	for i, opt := range columnOptions {
		if opt.name == name {
			return i
		}
	}
	t.Fatalf("no column option %q", name)
	return 0
}

func typeColumn(t *testing.T, m Model, value string) Model {
	t.Helper()
	m = pressColumns(t, m, keyPress("enter"))
	if !m.columnEditing {
		t.Fatal("enter didn't start editing")
	}
	m.configInput.SetValue(value)
	return pressColumns(t, m, keyPress("enter"))
}

func rowOf(view, option string) string {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, option) {
			return line
		}
	}
	return ""
}

func TestColumnsOpenFromTheBoardAndSettings(t *testing.T) {
	m, _ := columnsModel(t)
	next, _ := m.updateBoard(key("O"))
	if next.(Model).mode != modeColumns {
		t.Error("O didn't open the columns view")
	}
	m.mode = modeConfig
	if pressConfig(t, m, key("c")).mode != modeColumns {
		t.Error("c in settings didn't open the columns view")
	}
}

func TestColumnsEditsGoToTheConfigHoldingTheColumns(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")
	m = pressColumns(t, m, keyPress("enter"))
	if m.configLayer != config.Global {
		t.Fatalf("layer = %v", m.configLayer)
	}
	m.configInput.SetValue("high")
	m = pressColumns(t, m, keyPress("enter"))

	if !config.Sets(root, config.Global, config.ColumnPath("run", "effortOverride")) {
		t.Fatalf("not saved to the global config; flash=%q", m.flash)
	}
	if _, err := os.Stat(config.ProjectPath(root)); !os.IsNotExist(err) {
		t.Errorf("the project config was created: %v", err)
	}
	if c, _, _ := m.snap.Board.Column("run"); c.EffortOverride != "high" {
		t.Errorf("running board column = %+v", c)
	}
	if row := rowOf(m.renderColumns(), "Effort"); !strings.Contains(row, "high") || !strings.Contains(row, "global") {
		t.Errorf("row = %q", row)
	}
	if data, _ := os.ReadFile(config.GlobalPath()); strings.Contains(string(data), "modelOverride") {
		t.Errorf("a default was written into the config:\n%s", data)
	}
}

func TestColumnsEditsGoToTheProjectConfigWhenItHoldsTheColumns(t *testing.T) {
	m, root := columnsModel(t)
	project := `{"columns":[{"id":"run","name":"Executing","autoSpawn":true}]}`
	if err := os.MkdirAll(filepath.Dir(config.ProjectPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ProjectPath(root), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	m = configModel(t, root)
	m = openColumn(t, m, "run", "Effort")
	m = typeColumn(t, m, "high")
	if !config.Sets(root, config.Project, config.ColumnPath("run", "effortOverride")) || config.Sets(root, config.Global, config.ColumnPath("run", "effortOverride")) {
		t.Errorf("the edit landed in the wrong config; flash=%q", m.flash)
	}
}

func TestColumnsTabDoesNothing(t *testing.T) {
	m, _ := columnsModel(t)
	m = openColumn(t, m, "run", "Plan exit target")
	m = pressColumns(t, m, keyPress("enter"))
	m = pressColumns(t, m, keyPress("tab"))
	if m.configLayer != config.Global || !m.columnEditing {
		t.Errorf("layer = %v editing=%v", m.configLayer, m.columnEditing)
	}
}

func TestColumnsRejectedValueKeepsThePromptOpen(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")
	m = typeColumn(t, m, "extreme")

	if config.Sets(root, config.Global, config.ColumnPath("run", "effortOverride")) {
		t.Error("an unknown effort was saved")
	}
	if !strings.Contains(m.flash, "one of") {
		t.Errorf("flash=%q", m.flash)
	}
	if !m.columnEditing || m.configInput.Value() != "extreme" {
		t.Errorf("editing=%v input=%q, the typed text should stay", m.columnEditing, m.configInput.Value())
	}
	m = pressColumns(t, m, keyPress("esc"))
	if m.columnEditing {
		t.Error("esc didn't cancel")
	}
}

func TestColumnsEmptyInputRemovesTheKey(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")
	m = typeColumn(t, m, "high")

	m = typeColumn(t, m, "")
	if config.Sets(root, config.Global, config.ColumnPath("run", "effortOverride")) || !strings.HasPrefix(m.flash, "Reset") {
		t.Errorf("key survived; flash=%q", m.flash)
	}

	m = typeColumn(t, m, "")
	if !strings.Contains(m.flash, "not set in the global config") || !m.columnEditing {
		t.Errorf("flash=%q editing=%v", m.flash, m.columnEditing)
	}

	m = openColumn(t, m, "plan", "Permission mode")
	m = typeColumn(t, m, "")
	if config.Sets(root, config.Global, config.ColumnPath("plan", "permissionMode")) {
		t.Errorf("key survived; flash=%q", m.flash)
	}
}

func TestColumnsToggleWritesAndOffRemovesTheKey(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Skip the worktree")
	m = pressColumns(t, m, keyPress("enter"))
	if !config.Sets(root, config.Global, config.ColumnPath("run", "noWorktree")) {
		t.Fatalf("toggle wasn't saved; flash=%q", m.flash)
	}
	m = pressColumns(t, m, keyPress("enter"))
	if config.Sets(root, config.Global, config.ColumnPath("run", "noWorktree")) {
		t.Error("toggling off left the key behind")
	}
}

func TestColumnsConflictingToggleIsRolledBack(t *testing.T) {
	m, _ := columnsModel(t)
	m = openColumn(t, m, "run", "Keep the agent running")
	m = pressColumns(t, m, keyPress("enter"))

	if !strings.HasPrefix(m.flash, "Error:") {
		t.Errorf("flash=%q", m.flash)
	}
	if data, _ := os.ReadFile(config.GlobalPath()); string(data) != columnsBoard {
		t.Errorf("invalid column was left on disk:\n%s", data)
	}
}

func TestColumnsResetRemovesTheOverride(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "plan", "Permission mode")
	if m.columnSrc[m.columnOptSel] != "global" {
		t.Fatalf("source = %q", m.columnSrc[m.columnOptSel])
	}
	m = pressColumns(t, m, key("x"))
	if config.Sets(root, config.Global, config.ColumnPath("plan", "permissionMode")) {
		t.Errorf("reset kept the key; flash=%q", m.flash)
	}
	m = pressColumns(t, m, key("x"))
	if !strings.Contains(m.flash, "already the default") {
		t.Errorf("flash=%q", m.flash)
	}
}

func TestColumnsNameCannotBeEmptiedOrReset(t *testing.T) {
	m, _ := columnsModel(t)
	m = openColumn(t, m, "run", "Name")
	m = pressColumns(t, m, key("x"))
	if !strings.Contains(m.flash, "can't be reset") {
		t.Errorf("flash=%q", m.flash)
	}
	m = typeColumn(t, m, "")
	if !strings.Contains(m.flash, "can't be empty") {
		t.Errorf("flash=%q", m.flash)
	}
	if data, _ := os.ReadFile(config.GlobalPath()); string(data) != columnsBoard {
		t.Errorf("the config changed:\n%s", data)
	}
}

func TestColumnsRenameWarnsWhenATargetNamesTheOldName(t *testing.T) {
	m, _ := columnsModel(t)
	m = openColumn(t, m, "run", "Name")
	m = typeColumn(t, m, "Building")
	if !strings.Contains(m.flash, `still name "Executing"`) {
		t.Errorf("flash=%q", m.flash)
	}

	m = openColumn(t, m, "done", "Name")
	m = typeColumn(t, m, "Shipped")
	if strings.Contains(m.flash, "still name") {
		t.Errorf("flash=%q", m.flash)
	}
}

func TestColumnsEditOfTheDefaultBoardWritesItToTheGlobalConfig(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = openColumn(t, m, m.snap.Board.Columns[0].ID, "Name")
	m = typeColumn(t, m, "Backlog")
	if !strings.HasPrefix(m.flash, "Saved") || !strings.Contains(m.flash, "global config") {
		t.Errorf("flash=%q", m.flash)
	}
	if got := m.snap.Board.Columns[0].Name; got != "Backlog" || len(m.snap.Board.Columns) != len(config.Default().Columns) {
		t.Errorf("columns = %+v", m.snap.Board.Columns)
	}
	if !config.Sets(root, config.Global, []string{"columns"}) {
		t.Error("the board wasn't written to the global config")
	}
}

func TestColumnsEditLeavesOtherColumnsAlone(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "plan", "Color")
	m = typeColumn(t, m, "#ff0000")

	board, _, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Columns) != 3 || board.Columns[0].Color != "#ff0000" || board.Columns[0].PermissionMode != "plan" || board.Columns[1].Name != "Executing" {
		t.Errorf("columns = %+v", board.Columns)
	}
	data, _ := os.ReadFile(config.GlobalPath())
	if !strings.Contains(string(data), `"role": "done"`) || !strings.HasSuffix(string(data), "\n") {
		t.Errorf("file formatting:\n%s", data)
	}
}

func TestColumnsFollowTheColumnWhenTheBoardIsReordered(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")

	cols := m.snap.Board.Columns
	board := *m.snap.Board
	board.Columns = []config.Column{cols[1], cols[0], cols[2]}
	m.snap.Board = &board
	m.columnSel = 0

	m = typeColumn(t, m, "high")
	if !config.Sets(root, config.Global, config.ColumnPath("run", "effortOverride")) || config.Sets(root, config.Global, config.ColumnPath("plan", "effortOverride")) {
		t.Errorf("the edit landed on the wrong column; flash=%q", m.flash)
	}
}

func TestColumnsCloseWhenTheOpenColumnIsRemoved(t *testing.T) {
	m, _ := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")

	board := *m.snap.Board
	board.Columns = board.Columns[:1]
	m.snap.Board = &board

	m = pressColumns(t, m, key("j"))
	if m.mode != modeBoard || !strings.Contains(m.flash, "removed") {
		t.Errorf("mode=%v flash=%q", m.mode, m.flash)
	}
}

func TestColumnsRenderWithoutAColumnDoesNotPanic(t *testing.T) {
	m, _ := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")
	m.columnID = "gone"
	if got := m.renderColumns(); got != "" {
		t.Errorf("render = %q", got)
	}
}

func TestColumnsDoNotOpenWithoutColumns(t *testing.T) {
	m, _ := columnsModel(t)
	board := *m.snap.Board
	board.Columns = nil
	m.snap.Board = &board
	next, _ := m.updateBoard(key("O"))
	if got := next.(Model); got.mode == modeColumns || got.flash == "" {
		t.Errorf("mode=%v flash=%q", got.mode, got.flash)
	}
}

func TestColumnsListKeysNavigateAndClose(t *testing.T) {
	m, _ := columnsModel(t)
	m = m.openColumns()
	last := len(m.snap.Board.Columns) - 1

	m = pressColumns(t, m, key("k"))
	if m.columnSel != 0 {
		t.Errorf("selection %d after going up from the first column", m.columnSel)
	}
	for range last + 3 {
		m = pressColumns(t, m, key("j"))
	}
	if m.columnSel != last {
		t.Fatalf("selection %d after going down past the end, want %d", m.columnSel, last)
	}

	for _, open := range []tea.KeyMsg{keyPress("enter"), key("l"), special(tea.KeyRight)} {
		m = pressColumns(t, m, open)
		if !m.columnOpen || m.columnID != m.snap.Board.Columns[last].ID {
			t.Errorf("%q: open %v on %q, want the last column opened", open.String(), m.columnOpen, m.columnID)
		}
		m.columnOpen = false
	}
	for _, closing := range []tea.KeyMsg{special(tea.KeyEsc), key("q"), key("O")} {
		m.mode = modeBoard
		m = m.openColumns()
		if m = pressColumns(t, m, closing); m.mode != modeBoard {
			t.Errorf("%q left mode %v, want the board", closing.String(), m.mode)
		}
	}
}

func TestColumnsOptionKeysNavigateAndStepBack(t *testing.T) {
	m, _ := columnsModel(t)
	m = openColumn(t, m, "run", "Name")
	for range len(columnOptions) + 3 {
		m = pressColumns(t, m, key("j"))
	}
	if m.columnOptSel != len(columnOptions)-1 {
		t.Errorf("option %d after going down past the end", m.columnOptSel)
	}
	for range len(columnOptions) + 3 {
		m = pressColumns(t, m, key("k"))
	}
	if m.columnOptSel != 0 {
		t.Errorf("option %d after going up past the start", m.columnOptSel)
	}
	for _, back := range []tea.KeyMsg{special(tea.KeyEsc), key("q"), key("h"), special(tea.KeyLeft)} {
		open := openColumn(t, m, "run", "Name")
		if open = pressColumns(t, open, back); open.columnOpen || open.mode != modeColumns {
			t.Errorf("%q: open %v mode %v, want the list again inside the columns view", back.String(), open.columnOpen, open.mode)
		}
	}
}

func TestColumnsFooterHintFollowsWhatYouAreDoing(t *testing.T) {
	m, _ := columnsModel(t)
	m.mode = modeColumns
	tests := []struct {
		name  string
		setup func(m *Model)
		want  string
	}{
		{"the list", func(*Model) {}, "enter open"},
		{"a column's options", func(m *Model) { m.columnOpen = true }, "x reset"},
		{"editing a setting", func(m *Model) {
			m.columnOpen, m.columnEditing = true, true
			m.columnOptSel = columnOptionIndex(t, "Color")
		}, "enter save  esc cancel"},
		{"editing another setting", func(m *Model) {
			m.columnOpen, m.columnEditing = true, true
			m.columnOptSel = columnOptionIndex(t, "Run an agent")
		}, "enter save  esc cancel"},
	}
	for _, tt := range tests {
		next := m
		tt.setup(&next)
		if got := ansi.Strip(next.renderFooter()); !strings.Contains(got, tt.want) {
			t.Errorf("%s: footer %q, want %q", tt.name, got, tt.want)
		}
	}
}

const promptBoard = `{
  "columns": [
    {"id": "review", "name": "Review", "autoSpawn": true, "automations": {"onEnter": [
      {"name": "Review", "type": "send_message", "message": "/code-review\nbe strict"},
      {"type": "notify"}
    ]}},
    {"id": "cond", "name": "Cond", "autoSpawn": true, "automations": {"onEnter": [
      {"type": "send_message", "message": "later", "conditionScript": "true"}
    ]}},
    {"id": "plain", "name": "Plain", "autoSpawn": true}
  ]
}
`

func promptModel(t *testing.T) (Model, string) {
	t.Helper()
	root := newRoot(t)
	if err := os.MkdirAll(filepath.Dir(config.GlobalPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GlobalPath(), []byte(promptBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	return configModel(t, root), root
}

func savePrompt(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = pressColumns(t, m, keyPress("enter"))
	if !m.columnEditing {
		t.Fatal("enter didn't start editing")
	}
	m.columnPrompt.SetValue(text)
	return pressColumns(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
}

func onEnter(t *testing.T, m Model, id string) []config.Automation {
	t.Helper()
	c, _, ok := m.snap.Board.Column(id)
	if !ok {
		t.Fatalf("no column %q", id)
	}
	return c.Automations.OnEnter
}

func TestColumnsShowAndEditTheOpeningPrompt(t *testing.T) {
	m, root := promptModel(t)
	m = openColumn(t, m, "review", "Opening prompt")
	if row := rowOf(m.renderColumns(), "Opening prompt"); !strings.Contains(row, "/code-review …") || !strings.Contains(row, "global") {
		t.Errorf("row = %q", row)
	}
	m = pressColumns(t, m, keyPress("enter"))
	if got := m.columnPrompt.Value(); got != "/code-review\nbe strict" {
		t.Errorf("editor starts with %q", got)
	}
	m.columnPrompt.SetValue("/review\nlook at {{prUrl}}\n")
	m = pressColumns(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})

	if m.columnEditing {
		t.Fatalf("still editing; flash=%q", m.flash)
	}
	rows := onEnter(t, m, "review")
	if len(rows) != 2 || rows[0].Message != "/review\nlook at {{prUrl}}" || rows[0].Name != "Review" || rows[1].Type != "notify" {
		t.Errorf("rows = %+v", rows)
	}
	board, _, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c, _, _ := board.Column("review"); c.Automations.OnEnter[0].Message != "/review\nlook at {{prUrl}}" {
		t.Errorf("not on disk: %+v", c.Automations)
	}
}

func TestColumnsEscCancelsPromptEdit(t *testing.T) {
	m, _ := promptModel(t)
	m = openColumn(t, m, "review", "Opening prompt")
	m = pressColumns(t, m, keyPress("enter"))
	m.columnPrompt.SetValue("changed")
	m = pressColumns(t, m, keyPress("esc"))
	if m.columnEditing {
		t.Error("esc kept editing")
	}
	if data, _ := os.ReadFile(config.GlobalPath()); string(data) != promptBoard {
		t.Errorf("the config changed:\n%s", data)
	}
}

func TestColumnsAddOpeningPromptBeforeOtherRows(t *testing.T) {
	m, _ := promptModel(t)
	m = openColumn(t, m, "cond", "Opening prompt")
	if row := rowOf(m.renderColumns(), "Opening prompt"); !strings.Contains(row, "—") {
		t.Errorf("a conditional row was shown as the prompt: %q", row)
	}
	m = savePrompt(t, m, "start here")
	rows := onEnter(t, m, "cond")
	if len(rows) != 2 || rows[0].Message != "start here" || rows[0].Type != "send_message" || rows[1].Message != "later" {
		t.Errorf("rows = %+v", rows)
	}

	m = openColumn(t, m, "plain", "Opening prompt")
	m = savePrompt(t, m, "hello")
	if rows := onEnter(t, m, "plain"); len(rows) != 1 || rows[0].Message != "hello" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestColumnsEmptyOpeningPromptRemovesTheRow(t *testing.T) {
	m, _ := promptModel(t)
	m = openColumn(t, m, "review", "Opening prompt")
	m = savePrompt(t, m, "  ")
	if rows := onEnter(t, m, "review"); len(rows) != 1 || rows[0].Type != "notify" {
		t.Errorf("rows = %+v", rows)
	}

	m = openColumn(t, m, "cond", "Opening prompt")
	m = savePrompt(t, m, "x")
	m = pressColumns(t, m, keyPress("x"))
	if rows := onEnter(t, m, "cond"); len(rows) != 1 || rows[0].Message != "later" {
		t.Errorf("reset removed the wrong row: %+v", rows)
	}

	m = openColumn(t, m, "plain", "Opening prompt")
	m = savePrompt(t, m, "x")
	m = pressColumns(t, m, keyPress("x"))
	if data, _ := os.ReadFile(config.GlobalPath()); strings.Contains(string(data), `"message": "x"`) {
		t.Errorf("row left behind:\n%s", data)
	}
	c, _, _ := m.snap.Board.Column("plain")
	if len(c.Automations.OnEnter) != 0 {
		t.Errorf("rows = %+v", c.Automations)
	}
}

func TestColumnsEmptyOpeningPromptWhenUnsetSaysSo(t *testing.T) {
	m, _ := promptModel(t)
	m = openColumn(t, m, "plain", "Opening prompt")
	m = savePrompt(t, m, "")
	if !m.columnEditing || !strings.Contains(m.flash, "not set") {
		t.Errorf("editing=%v flash=%q", m.columnEditing, m.flash)
	}
}

func TestColumnsOpeningPromptOfTheDefaultBoardWritesItToTheGlobalConfig(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	id := m.snap.Board.Columns[0].ID
	m = openColumn(t, m, id, "Opening prompt")
	m = savePrompt(t, m, "hi")
	if !strings.HasPrefix(m.flash, "Saved") || !config.Sets(root, config.Global, config.ColumnPath(id, "automations", "onEnter")) {
		t.Errorf("flash=%q", m.flash)
	}
}

func TestColumnsEscReturnsToWhereTheViewWasOpened(t *testing.T) {
	m, _ := columnsModel(t)
	if got := pressColumns(t, m.openColumns(), keyPress("esc")).mode; got != modeBoard {
		t.Errorf("from the board, esc went to mode %v", got)
	}
	m.mode = modeConfig
	if got := pressColumns(t, m.openColumns(), keyPress("esc")).mode; got != modeConfig {
		t.Errorf("from settings, esc went to mode %v", got)
	}
}
