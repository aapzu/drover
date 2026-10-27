package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/engine"
)

func configModel(t *testing.T, root string) Model {
	t.Helper()
	board, _, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(root)
	if err != nil {
		t.Fatal(err)
	}
	m := testModel(120, 40, 1, 0)
	m.snap.Board = board
	m.eng = eng
	m.configInput = textinput.New()
	return m
}

func selectOption(t *testing.T, m Model, name string) Model {
	t.Helper()
	for i, opt := range configOptions {
		if opt.name == name {
			m.configSel = i
			return m
		}
	}
	t.Fatalf("no option %q", name)
	return m
}

func pressConfig(t *testing.T, m Model, k tea.KeyMsg) Model {
	t.Helper()
	next, _ := m.updateConfig(k)
	return next.(Model)
}

func TestConfigOpensFromTheBoard(t *testing.T) {
	m := configModel(t, t.TempDir())
	next, _ := m.updateBoard(key("o"))
	if next.(Model).mode != modeConfig {
		t.Fatal("o didn't open the config view")
	}
}

func TestConfigToggleSavesToTheLocalFile(t *testing.T) {
	root := t.TempDir()
	m := configModel(t, root)
	m = selectOption(t, m, "Auto-name cards")

	m = pressConfig(t, m, keyPress("enter"))
	if m.snap.Board.AutoName {
		t.Error("toggle didn't change the running board")
	}
	if !config.Sets(root, config.Local, []string{"autoName"}) {
		t.Error("toggle wasn't saved to drover.local.json")
	}
	if !strings.Contains(m.renderConfig(), "local") {
		t.Errorf("row doesn't show where the value comes from:\n%s", m.renderConfig())
	}
}

func TestConfigNumberRejectsText(t *testing.T) {
	root := t.TempDir()
	m := configModel(t, root)
	m = selectOption(t, m, "Max concurrent agents")

	m = pressConfig(t, m, keyPress("enter"))
	if !m.configEditing {
		t.Fatal("enter didn't start editing a number")
	}
	m.configInput.SetValue("many")
	m = pressConfig(t, m, keyPress("enter"))
	if config.Sets(root, config.Local, []string{"maxConcurrent"}) {
		t.Error("text was saved into a number field")
	}
	if !strings.Contains(m.flash, "whole number") {
		t.Errorf("flash=%q", m.flash)
	}

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("12")
	m = pressConfig(t, m, keyPress("enter"))
	if m.snap.Board.Limit() != 12 {
		t.Errorf("limit=%d after saving 12", m.snap.Board.Limit())
	}
}

func TestConfigEscCancelsAnEdit(t *testing.T) {
	root := t.TempDir()
	m := configModel(t, root)
	m = selectOption(t, m, "Default base branch")
	m.mode = modeConfig

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("develop")
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.configEditing || m.mode != modeConfig {
		t.Fatalf("esc in the prompt should cancel only the edit: editing=%v mode=%v", m.configEditing, m.mode)
	}
	if config.Sets(root, config.Local, []string{"defaultBaseBranch"}) {
		t.Error("cancelled edit was saved")
	}
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeBoard {
		t.Error("esc on the list didn't close the view")
	}
}

func TestConfigTabTargetsDroverJSON(t *testing.T) {
	root := t.TempDir()
	if err := config.Default().Save(root); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Worktrees directory")

	m = pressConfig(t, m, keyPress("enter"))
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.configLayer != config.Shared {
		t.Fatal("tab didn't switch the target to drover.json")
	}
	m.configInput.SetValue(".worktrees")
	m = pressConfig(t, m, keyPress("enter"))
	if !config.Sets(root, config.Shared, []string{"worktreesDir"}) {
		t.Error("value wasn't saved to drover.json")
	}
	if config.Sets(root, config.Local, []string{"worktreesDir"}) {
		t.Error("value also landed in drover.local.json")
	}
}

func TestConfigSharedOnlyOptionGoesToDroverJSON(t *testing.T) {
	root := t.TempDir()
	if err := config.Default().Save(root); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Archive Done after")

	m = pressConfig(t, m, keyPress("enter"))
	if m.configLayer != config.Shared {
		t.Fatal("shared-only option defaulted to a local target")
	}
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.configLayer != config.Shared {
		t.Error("tab moved a shared-only option off drover.json")
	}
	m.configInput.SetValue("30d")
	m = pressConfig(t, m, keyPress("enter"))
	if m.snap.Board.ArchiveAfter().Hours() != 30*24 {
		t.Errorf("archiveDoneAfter not applied: %v", m.snap.Board.ArchiveAfter())
	}
}

func TestConfigResetRemovesTheLocalOverride(t *testing.T) {
	root := t.TempDir()
	if err := config.Set(root, config.Local, []string{"autoName"}, false); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Auto-name cards")
	if m.snap.Board.AutoName {
		t.Fatal("setup: local override not loaded")
	}

	m = pressConfig(t, m, key("x"))
	if config.Sets(root, config.Local, []string{"autoName"}) {
		t.Error("x didn't remove the local override")
	}
	if !m.snap.Board.AutoName {
		t.Error("autoName did not fall back to the default")
	}
	if !strings.Contains(m.flash, "Reset") {
		t.Errorf("flash=%q", m.flash)
	}
}

func TestConfigSharedEditWithoutDroverJSONSaysHowToCreateIt(t *testing.T) {
	root := t.TempDir()
	m := configModel(t, root)
	m = selectOption(t, m, "Archive Done after")

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("30d")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.Contains(m.flash, "press W") {
		t.Errorf("flash=%q", m.flash)
	}
	if _, err := os.Stat(filepath.Join(root, config.FileName)); !os.IsNotExist(err) {
		t.Errorf("drover.json was created: %v", err)
	}
}

func TestConfigOptionsMatchTheBoard(t *testing.T) {
	board := config.Default()
	board.DefaultBaseBranch, board.DefaultPermissionMode, board.DefaultModel = "main", "auto", "opus"
	board.WorktreesDir, board.ArchiveDoneAfter, board.InitScript = "wt", "7d", "setup"
	board.MaxConcurrent, board.IdleTimeoutMinutes = 4, 5
	data, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	sharedSeen := false
	for _, opt := range configOptions {
		var typed bool
		switch got := opt.get(board); opt.kind {
		case optToggle:
			_, typed = got.(bool)
		case optNumber:
			_, typed = got.(int)
		case optText:
			_, typed = got.(string)
		}
		if !typed {
			t.Errorf("%s: getter type doesn't match its kind", opt.name)
		}

		var node any = doc
		for _, key := range opt.path {
			obj, ok := node.(map[string]any)
			if !ok {
				t.Fatalf("%s: %v does not resolve in the board JSON", opt.name, opt.path)
			}
			if node, ok = obj[key]; !ok {
				t.Fatalf("%s: %v does not resolve in the board JSON", opt.name, opt.path)
			}
		}

		if opt.shared {
			sharedSeen = true
		} else if sharedSeen {
			t.Errorf("%s: shared options must come last, renderConfig groups them by position", opt.name)
		}
	}
}

func TestConfigFooterHidesTabForSharedOnlyOptions(t *testing.T) {
	root := t.TempDir()
	m := configModel(t, root)
	m.mode = modeConfig
	m.configEditing = true

	m = selectOption(t, m, "Auto-name cards")
	if !strings.Contains(m.renderFooter(), "tab") {
		t.Errorf("footer=%q, want a tab hint for a local-capable option", m.renderFooter())
	}
	m = selectOption(t, m, "Archive Done after")
	if strings.Contains(m.renderFooter(), "tab") {
		t.Errorf("footer=%q, tab does nothing for a shared-only option", m.renderFooter())
	}
}

func TestConfigResetWithBothLayersRemovesLocalFirst(t *testing.T) {
	root := t.TempDir()
	if err := config.Default().Save(root); err != nil {
		t.Fatal(err)
	}
	path := []string{"worktreesDir"}
	if err := config.Set(root, config.Shared, path, ".shared-wt"); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(root, config.Local, path, ".local-wt"); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Worktrees directory")
	m.configSrc = configSources(root)

	m = pressConfig(t, m, key("x"))
	if config.Sets(root, config.Local, path) || !config.Sets(root, config.Shared, path) {
		t.Fatal("the first x should remove only the local key")
	}
	if m.snap.Board.WorktreesDir != ".shared-wt" || m.configSrc[m.configSel] != "shared" {
		t.Errorf("worktreesDir=%q label=%q", m.snap.Board.WorktreesDir, m.configSrc[m.configSel])
	}

	m = pressConfig(t, m, key("x"))
	if config.Sets(root, config.Shared, path) || m.configSrc[m.configSel] != "default" {
		t.Errorf("the second x should remove the shared key: label=%q", m.configSrc[m.configSel])
	}
}

func TestConfigResetOnADefaultRowChangesNothing(t *testing.T) {
	root := t.TempDir()
	m := configModel(t, root)
	m = selectOption(t, m, "Auto-name cards")

	m = pressConfig(t, m, key("x"))
	if !strings.Contains(m.flash, "already the default") {
		t.Errorf("flash=%q", m.flash)
	}
	if _, err := os.Stat(filepath.Join(root, config.LocalFileName)); !os.IsNotExist(err) {
		t.Errorf("reset created drover.local.json: %v", err)
	}
}

func TestConfigEmptyPromptRemovesTheKeyFromTheChosenLayer(t *testing.T) {
	root := t.TempDir()
	if err := config.Set(root, config.Local, []string{"defaultBaseBranch"}, "develop"); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Default base branch")

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("")
	m = pressConfig(t, m, keyPress("enter"))
	if config.Sets(root, config.Local, []string{"defaultBaseBranch"}) {
		t.Error("an empty prompt didn't remove the local key")
	}
	if !strings.Contains(m.flash, "Reset") {
		t.Errorf("flash=%q", m.flash)
	}
}

func TestConfigEmptyPromptWhereNothingIsSetSaysSo(t *testing.T) {
	root := t.TempDir()
	if err := config.Default().Save(root); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(root, config.Shared, []string{"defaultBaseBranch"}, "main"); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Default base branch")

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("  ")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.Contains(m.flash, "is not set in drover.local.json") {
		t.Errorf("flash=%q", m.flash)
	}
	if !config.Sets(root, config.Shared, []string{"defaultBaseBranch"}) {
		t.Error("an empty local prompt removed the shared value")
	}
}

func TestConfigListKeysAreTextInsideThePrompt(t *testing.T) {
	m := configModel(t, t.TempDir())
	m = selectOption(t, m, "Default base branch")
	m.mode = modeConfig

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("")
	for _, r := range "qxoj" {
		m = pressConfig(t, m, key(string(r)))
	}
	if m.mode != modeConfig || !m.configEditing || m.configInput.Value() != "qxoj" {
		t.Errorf("mode=%v editing=%v value=%q", m.mode, m.configEditing, m.configInput.Value())
	}
}

func TestConfigRejectedTextValueShowsTheErrorAndKeepsTheFile(t *testing.T) {
	root := t.TempDir()
	if err := config.Default().Save(root); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, config.FileName))
	m := configModel(t, root)
	m = selectOption(t, m, "Archive Done after")

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("soon")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.HasPrefix(m.flash, "Error:") {
		t.Errorf("flash=%q", m.flash)
	}
	after, _ := os.ReadFile(filepath.Join(root, config.FileName))
	if string(before) != string(after) {
		t.Errorf("drover.json changed after a rejected value:\n%s", after)
	}
}

func TestConfigMaxConcurrentNeedsAtLeastOne(t *testing.T) {
	root := t.TempDir()
	m := configModel(t, root)
	m = selectOption(t, m, "Max concurrent agents")

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("0")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.Contains(m.flash, "at least 1") {
		t.Errorf("flash=%q", m.flash)
	}
	if config.Sets(root, config.Local, []string{"maxConcurrent"}) {
		t.Error("0 was saved as the concurrency limit")
	}
}

func TestConfigWarnsWhenTheLocalFileOverridesASharedSave(t *testing.T) {
	root := t.TempDir()
	if err := config.Default().Save(root); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(root, config.Local, []string{"worktreesDir"}, ".local-wt"); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Worktrees directory")

	m = pressConfig(t, m, keyPress("enter"))
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m.configInput.SetValue(".shared-wt")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.Contains(m.flash, "overridden by drover.local.json") {
		t.Errorf("flash=%q", m.flash)
	}
}

func TestConfigFooterAndRows(t *testing.T) {
	m := configModel(t, t.TempDir())
	m.width, m.height = 120, 40
	next, _ := m.updateBoard(key("o"))
	m = next.(Model)
	if !strings.Contains(m.renderFooter(), "x reset") {
		t.Errorf("footer=%q", m.renderFooter())
	}
	out := m.renderConfig()
	for _, want := range []string{"Auto-name cards", "Archive Done after", "drover.json only", "default"} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q:\n%s", want, out)
		}
	}
}
