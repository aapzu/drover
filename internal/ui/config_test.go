package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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

func TestConfigToggleSavesToTheLocalFile(t *testing.T) {
	tests := []struct {
		option, key string
		value       func(*config.Board) bool
		onByDefault bool
	}{
		{"Auto-name cards", "autoName", func(b *config.Board) bool { return b.AutoName }, true},
		{"Split PRs into cards", "splitByPR", func(b *config.Board) bool { return b.SplitByPR }, true},
		{"Disable mouse in agents", "disableMouse", func(b *config.Board) bool { return b.DisableMouse }, false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			root := t.TempDir()
			m := configModel(t, root)
			if tt.value(m.snap.Board) != tt.onByDefault {
				t.Fatalf("%s starts as %v", tt.key, !tt.onByDefault)
			}
			m = selectOption(t, m, tt.option)

			m = pressConfig(t, m, keyPress("enter"))
			if tt.value(m.snap.Board) == tt.onByDefault {
				t.Error("toggle didn't change the running board")
			}
			if !config.Sets(root, config.Local, []string{tt.key}) {
				t.Error("toggle wasn't saved to drover.local.json")
			}
			if !strings.Contains(m.renderConfig(), "local") {
				t.Errorf("row doesn't show where the value comes from:\n%s", m.renderConfig())
			}
		})
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

func TestConfigResetOfASingleLayer(t *testing.T) {
	tests := []struct {
		name   string
		preset bool
		flash  string
	}{
		{"removes the local override", true, "Reset"},
		{"on a default row changes nothing", false, "already the default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.preset {
				if err := config.Set(root, config.Local, []string{"autoName"}, false); err != nil {
					t.Fatal(err)
				}
			}
			m := configModel(t, root)
			m = selectOption(t, m, "Auto-name cards")
			if tt.preset && m.snap.Board.AutoName {
				t.Fatal("setup: local override not loaded")
			}

			m = pressConfig(t, m, key("x"))
			if config.Sets(root, config.Local, []string{"autoName"}) {
				t.Error("x didn't remove the local override")
			}
			if !m.snap.Board.AutoName {
				t.Error("autoName did not fall back to the default")
			}
			if !strings.Contains(m.flash, tt.flash) {
				t.Errorf("flash=%q", m.flash)
			}
			if _, err := os.Stat(filepath.Join(root, config.LocalFileName)); !tt.preset && !os.IsNotExist(err) {
				t.Errorf("reset created drover.local.json: %v", err)
			}
		})
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
	board.MaxConcurrent, board.IdleTimeoutMinutes, board.LinkUntracked, board.DisableMouse = 4, 5, true, true
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
		case optText, optChoice:
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

func TestConfigFixModeCyclesThroughTheChoices(t *testing.T) {
	root := t.TempDir()
	if err := config.Default().Save(root); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "PR review fix mode")
	if got := configValue(configOptions[m.configSel], m.snap.Board); got != "propose" {
		t.Fatalf("starts as %q, want propose", got)
	}

	for _, want := range []string{"fix", "off", "propose"} {
		m = pressConfig(t, m, keyPress("enter"))
		if got := configValue(configOptions[m.configSel], m.snap.Board); got != want {
			t.Fatalf("after enter = %q, want %q (flash %q)", got, want, m.flash)
		}
		data, _ := os.ReadFile(filepath.Join(root, config.FileName))
		if !strings.Contains(string(data), `"changesRequestedMode": "`+want+`"`) {
			t.Errorf("drover.json doesn't hold %q:\n%s", want, data)
		}
	}

	m = pressConfig(t, m, keyPress("x"))
	if config.Sets(root, config.Shared, configOptions[m.configSel].path) {
		t.Error("x left changesRequestedMode in drover.json")
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

func TestConfigEmptyPrompt(t *testing.T) {
	path := []string{"defaultBaseBranch"}
	tests := []struct {
		name       string
		setup      func(t *testing.T, root string)
		input      string
		flash      string
		wantShared bool
	}{
		{"removes the key from the chosen layer", func(t *testing.T, root string) {
			if err := config.Set(root, config.Local, path, "develop"); err != nil {
				t.Fatal(err)
			}
		}, "", "Reset", false},
		{"where nothing is set says so", func(t *testing.T, root string) {
			if err := config.Default().Save(root); err != nil {
				t.Fatal(err)
			}
			if err := config.Set(root, config.Shared, path, "main"); err != nil {
				t.Fatal(err)
			}
		}, "  ", "is not set in drover.local.json", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			m := configModel(t, root)
			m = selectOption(t, m, "Default base branch")

			m = pressConfig(t, m, keyPress("enter"))
			m.configInput.SetValue(tt.input)
			m = pressConfig(t, m, keyPress("enter"))
			if config.Sets(root, config.Local, path) {
				t.Error("an empty prompt left the local key")
			}
			if config.Sets(root, config.Shared, path) != tt.wantShared {
				t.Errorf("shared key set = %v, want %v", !tt.wantShared, tt.wantShared)
			}
			if !strings.Contains(m.flash, tt.flash) {
				t.Errorf("flash=%q", m.flash)
			}
		})
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

func TestConfigListKeysStopAtBothEnds(t *testing.T) {
	m := configModel(t, t.TempDir())
	m.mode = modeConfig
	m = pressConfig(t, m, key("k"))
	m = pressConfig(t, m, special(tea.KeyUp))
	if m.configSel != 0 {
		t.Errorf("selection %d after going up from the first option", m.configSel)
	}
	for range len(configOptions) + 3 {
		m = pressConfig(t, m, key("j"))
	}
	last := len(configOptions) - 1
	if m.configSel != last {
		t.Fatalf("selection %d after going down past the end, want the last option %d", m.configSel, last)
	}
	m = pressConfig(t, m, special(tea.KeyDown))
	if m = pressConfig(t, m, key("k")); m.configSel != last-1 {
		t.Errorf("selection %d after one up from the end, want %d", m.configSel, last-1)
	}
}

func TestConfigClosesOnEscQAndOAndOpensTheColumns(t *testing.T) {
	for _, k := range []tea.KeyMsg{special(tea.KeyEsc), key("q"), key("o")} {
		m := configModel(t, t.TempDir())
		m.mode = modeConfig
		if m = pressConfig(t, m, k); m.mode != modeBoard {
			t.Errorf("%q left the mode at %v, want the board", k.String(), m.mode)
		}
	}
	m := configModel(t, t.TempDir())
	m.mode = modeConfig
	if m = pressConfig(t, m, key("c")); m.mode != modeColumns {
		t.Errorf("c opened mode %v, want the columns view", m.mode)
	}
}

func TestConfigToggleInsideANestedObjectWritesTheNestedKey(t *testing.T) {
	root := t.TempDir()
	m := configModel(t, root)
	m = selectOption(t, m, "Notify when a turn ends")
	before := m.snap.Board.Notifications.TurnDone

	m = pressConfig(t, m, keyPress("enter"))
	if m.snap.Board.Notifications.TurnDone == before {
		t.Error("the toggle didn't change the running board")
	}
	if !config.Sets(root, config.Local, []string{"notifications", "turnDone"}) {
		t.Errorf("notifications.turnDone wasn't saved to drover.local.json (flash %q)", m.flash)
	}
	if config.Sets(root, config.Local, []string{"turnDone"}) {
		t.Error("the key was written at the top level")
	}
}

func TestConfigNumbersRejectNegativesAndAcceptZero(t *testing.T) {
	tests := []struct {
		option, input string
		saved         bool
	}{
		{"Idle timeout (minutes)", "-5", false},
		{"Idle timeout (minutes)", "0", true},
		{"Notification cooldown (seconds)", "-1", false},
	}
	for _, tt := range tests {
		root := t.TempDir()
		m := configModel(t, root)
		m = selectOption(t, m, tt.option)
		m = pressConfig(t, m, keyPress("enter"))
		m.configInput.SetValue(tt.input)
		m = pressConfig(t, m, keyPress("enter"))
		if got := config.Sets(root, config.Local, configOptions[m.configSel].path); got != tt.saved {
			t.Errorf("%s %q: saved = %v, want %v (flash %q)", tt.option, tt.input, got, tt.saved, m.flash)
		}
		if rejected := strings.Contains(m.flash, "at least 0"); rejected == tt.saved {
			t.Errorf("%s %q: flash %q, rejected = %v, want %v", tt.option, tt.input, m.flash, rejected, !tt.saved)
		}
	}
}

func TestConfigListScrollsToKeepTheSelectionInView(t *testing.T) {
	first, last := configOptions[0].name, configOptions[len(configOptions)-1].name
	m := configModel(t, t.TempDir())
	m.configSrc = configSources(m.eng.Root)
	m.width, m.height = 80, 24
	m.mode = modeConfig

	out := ansi.Strip(m.View())
	if !strings.Contains(out, first) || strings.Contains(out, last) {
		t.Fatalf("with the first option selected the view should start at the top:\n%s", out)
	}
	m.configSel = len(configOptions) - 1
	out = ansi.Strip(m.View())
	if !strings.Contains(out, last) || strings.Contains(out, first) {
		t.Errorf("with the last option selected the view should end at the bottom:\n%s", out)
	}
	if !strings.Contains(out, "▸ "+last) {
		t.Errorf("the selected option has no cursor:\n%s", out)
	}
	fits(t, "config", m.View(), 80, 24)
}
