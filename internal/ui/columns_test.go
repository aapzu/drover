package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(columnsBoard), 0o644); err != nil {
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
	for i, opt := range columnOptions {
		if opt.name == option {
			m.columnOptSel = i
			return m
		}
	}
	t.Fatalf("no option %q", option)
	return m
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

func TestColumnsLocalEditDoesNotTouchTheSharedFile(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")
	m = pressColumns(t, m, keyPress("enter"))
	if m.configLayer != config.Local {
		t.Fatalf("layer = %v", m.configLayer)
	}
	m.configInput.SetValue("high")
	m = pressColumns(t, m, keyPress("enter"))

	if !config.Sets(root, config.Local, config.ColumnPath("run", "effortOverride")) {
		t.Fatalf("not saved to drover.local.json; flash=%q", m.flash)
	}
	if shared, _ := os.ReadFile(filepath.Join(root, config.FileName)); string(shared) != columnsBoard {
		t.Errorf("drover.json changed:\n%s", shared)
	}
	if c, _, _ := m.snap.Board.Column("run"); c.EffortOverride != "high" {
		t.Errorf("running board column = %+v", c)
	}
	if row := rowOf(m.renderColumns(), "Effort"); !strings.Contains(row, "high") || !strings.Contains(row, "local") {
		t.Errorf("row = %q", row)
	}
}

func TestColumnsSharedEditViaTab(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Permission mode")
	m = pressColumns(t, m, keyPress("enter"))
	m = pressColumns(t, m, keyPress("tab"))
	if m.configLayer != config.Shared {
		t.Fatal("tab didn't switch to drover.json")
	}
	m.configInput.SetValue("acceptEdits")
	m = pressColumns(t, m, keyPress("enter"))

	if !config.Sets(root, config.Shared, config.ColumnPath("run", "permissionMode")) {
		t.Fatalf("not saved to drover.json; flash=%q", m.flash)
	}
	if data, _ := os.ReadFile(filepath.Join(root, config.FileName)); strings.Contains(string(data), "modelOverride") {
		t.Errorf("a default was written into drover.json:\n%s", data)
	}
}

func TestColumnsTabDoesNothingForSharedOnlyOptions(t *testing.T) {
	m, _ := columnsModel(t)
	m = openColumn(t, m, "run", "Plan exit target")
	m = pressColumns(t, m, keyPress("enter"))
	m = pressColumns(t, m, keyPress("tab"))
	if m.configLayer != config.Shared {
		t.Errorf("layer = %v", m.configLayer)
	}
}

func TestColumnsRejectedValueKeepsThePromptOpen(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")
	m = typeColumn(t, m, "extreme")

	if config.Sets(root, config.Local, config.ColumnPath("run", "effortOverride")) {
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

func TestColumnsEmptyInputRemovesTheKeyFromTheChosenLayer(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")
	m = typeColumn(t, m, "high")

	m = typeColumn(t, m, "")
	if config.Sets(root, config.Local, config.ColumnPath("run", "effortOverride")) || !strings.HasPrefix(m.flash, "Reset") {
		t.Errorf("local key survived; flash=%q", m.flash)
	}

	m = typeColumn(t, m, "")
	if !strings.Contains(m.flash, "not set in "+config.LocalFileName) || !m.columnEditing {
		t.Errorf("flash=%q editing=%v", m.flash, m.columnEditing)
	}

	m = openColumn(t, m, "plan", "Permission mode")
	m = pressColumns(t, m, keyPress("enter"))
	m = pressColumns(t, m, keyPress("tab"))
	m.configInput.SetValue("")
	m = pressColumns(t, m, keyPress("enter"))
	if config.Sets(root, config.Shared, config.ColumnPath("plan", "permissionMode")) {
		t.Errorf("shared key survived; flash=%q", m.flash)
	}
}

func TestColumnsToggleWritesSharedAndOffRemovesTheKey(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Skip the worktree")
	if columnOptions[m.columnOptSel].local {
		t.Fatal("noWorktree must be shared-only")
	}
	m = pressColumns(t, m, keyPress("enter"))
	if !config.Sets(root, config.Shared, config.ColumnPath("run", "noWorktree")) {
		t.Fatalf("toggle wasn't saved; flash=%q", m.flash)
	}
	m = pressColumns(t, m, keyPress("enter"))
	if config.Sets(root, config.Shared, config.ColumnPath("run", "noWorktree")) {
		t.Error("toggling off left the key behind")
	}
}

func TestColumnsConflictingToggleIsRolledBack(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Keep the agent running")
	m = pressColumns(t, m, keyPress("enter"))

	if !strings.HasPrefix(m.flash, "Error:") {
		t.Errorf("flash=%q", m.flash)
	}
	if data, _ := os.ReadFile(filepath.Join(root, config.FileName)); string(data) != columnsBoard {
		t.Errorf("invalid column was left on disk:\n%s", data)
	}
}

func TestColumnsSavingInTheShadowedLayerWarns(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Effort")
	m = typeColumn(t, m, "high")

	m = pressColumns(t, m, keyPress("enter"))
	m = pressColumns(t, m, keyPress("tab"))
	m.configInput.SetValue("low")
	m = pressColumns(t, m, keyPress("enter"))
	if !strings.Contains(m.flash, "overridden by "+config.LocalFileName) {
		t.Errorf("flash=%q", m.flash)
	}
	if m.columnSrc[m.columnOptSel] != "local" {
		t.Errorf("source = %q", m.columnSrc[m.columnOptSel])
	}

	m = pressColumns(t, m, key("x"))
	if m.columnSrc[m.columnOptSel] != "shared" || config.Sets(root, config.Local, config.ColumnPath("run", "effortOverride")) {
		t.Errorf("first x should drop the local value; source = %q", m.columnSrc[m.columnOptSel])
	}
	m = pressColumns(t, m, key("x"))
	if config.Sets(root, config.Shared, config.ColumnPath("run", "effortOverride")) {
		t.Error("second x should drop the shared value")
	}
}

func TestColumnsResetRemovesTheOverride(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "plan", "Permission mode")
	if m.columnSrc[m.columnOptSel] != "shared" {
		t.Fatalf("source = %q", m.columnSrc[m.columnOptSel])
	}
	m = pressColumns(t, m, key("x"))
	if config.Sets(root, config.Shared, config.ColumnPath("plan", "permissionMode")) {
		t.Errorf("reset kept the key; flash=%q", m.flash)
	}
	m = pressColumns(t, m, key("x"))
	if !strings.Contains(m.flash, "already the default") {
		t.Errorf("flash=%q", m.flash)
	}
}

func TestColumnsNameCannotBeEmptiedOrReset(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "run", "Name")
	m = pressColumns(t, m, key("x"))
	if !strings.Contains(m.flash, "can't be reset") {
		t.Errorf("flash=%q", m.flash)
	}
	m = typeColumn(t, m, "")
	if !strings.Contains(m.flash, "can't be empty") {
		t.Errorf("flash=%q", m.flash)
	}
	if data, _ := os.ReadFile(filepath.Join(root, config.FileName)); string(data) != columnsBoard {
		t.Errorf("drover.json changed:\n%s", data)
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

func TestColumnsMissingSharedFilePointsToW(t *testing.T) {
	m := configModel(t, t.TempDir())
	m = openColumn(t, m, m.snap.Board.Columns[0].ID, "Name")
	m = typeColumn(t, m, "Backlog")
	if !strings.Contains(m.flash, "press W") {
		t.Errorf("flash=%q", m.flash)
	}
	m = openColumn(t, m, m.snap.Board.Columns[0].ID, "Skip the worktree")
	m = pressColumns(t, m, keyPress("enter"))
	if !strings.Contains(m.flash, "press W") {
		t.Errorf("toggle flash=%q", m.flash)
	}
}

func TestColumnsEditLeavesOtherColumnsAlone(t *testing.T) {
	m, root := columnsModel(t)
	m = openColumn(t, m, "plan", "Color")
	m = pressColumns(t, m, keyPress("enter"))
	m = pressColumns(t, m, keyPress("tab"))
	m.configInput.SetValue("#ff0000")
	m = pressColumns(t, m, keyPress("enter"))

	board, _, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Columns) != 3 || board.Columns[0].Color != "#ff0000" || board.Columns[0].PermissionMode != "plan" || board.Columns[1].Name != "Executing" {
		t.Errorf("columns = %+v", board.Columns)
	}
	data, _ := os.ReadFile(filepath.Join(root, config.FileName))
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
	if !config.Sets(root, config.Local, config.ColumnPath("run", "effortOverride")) || config.Sets(root, config.Local, config.ColumnPath("plan", "effortOverride")) {
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
