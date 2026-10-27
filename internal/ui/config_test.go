package ui

import (
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
	m := testModel(120, 40, 1, 0)
	m.snap.Board = board
	m.eng = &engine.Engine{Root: root}
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

func TestConfigFooterAndRows(t *testing.T) {
	m := configModel(t, t.TempDir())
	m.width, m.height = 120, 40
	m.mode = modeConfig
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
