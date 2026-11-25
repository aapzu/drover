package ui

import (
	"encoding/json"
	"os"
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
	m.configFilterInput = textinput.New()
	m.configSrc = configSources(root)
	m.columnPrompt = newColumnPrompt()
	return m
}

// newRoot returns an empty repo root with a global config directory of its own.
func newRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return t.TempDir()
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
	m := configModel(t, newRoot(t))
	next, _ := m.updateBoard(key("o"))
	if next.(Model).mode != modeConfig {
		t.Fatal("o didn't open the config view")
	}
}

func TestConfigToggleSavesToTheGlobalConfig(t *testing.T) {
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
			root := newRoot(t)
			m := configModel(t, root)
			if tt.value(m.snap.Board) != tt.onByDefault {
				t.Fatalf("%s starts as %v", tt.key, !tt.onByDefault)
			}
			m = selectOption(t, m, tt.option)

			m = pressConfig(t, m, keyPress("enter"))
			if tt.value(m.snap.Board) == tt.onByDefault {
				t.Error("toggle didn't change the running board")
			}
			if !config.Sets(root, config.Global, []string{tt.key}) {
				t.Error("toggle wasn't saved to the global config")
			}
			if !strings.Contains(m.renderConfig(), "global") {
				t.Errorf("row doesn't show where the value comes from:\n%s", m.renderConfig())
			}
		})
	}
}

func TestConfigNumberRejectsText(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = selectOption(t, m, "Max concurrent agents")

	m = pressConfig(t, m, keyPress("enter"))
	if !m.configEditing {
		t.Fatal("enter didn't start editing a number")
	}
	m.configInput.SetValue("many")
	m = pressConfig(t, m, keyPress("enter"))
	if config.Sets(root, config.Global, []string{"maxConcurrent"}) {
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
	root := newRoot(t)
	m := configModel(t, root)
	m = selectOption(t, m, "Default base branch")
	m.mode = modeConfig

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("develop")
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.configEditing || m.mode != modeConfig {
		t.Fatalf("esc in the prompt should cancel only the edit: editing=%v mode=%v", m.configEditing, m.mode)
	}
	if config.Sets(root, config.Project, []string{"defaultBaseBranch"}) {
		t.Error("cancelled edit was saved")
	}
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeBoard {
		t.Error("esc on the list didn't close the view")
	}
}

func TestConfigTabSwitchesTheTargetLayer(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = selectOption(t, m, "Worktrees directory")

	m = pressConfig(t, m, keyPress("enter"))
	if m.configLayer != config.Project {
		t.Fatal("a per-repo option didn't default to the project config")
	}
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.configLayer != config.Global {
		t.Fatal("tab didn't switch the target to the global config")
	}
	m.configInput.SetValue(".worktrees")
	m = pressConfig(t, m, keyPress("enter"))
	if !config.Sets(root, config.Global, []string{"worktreesDir"}) {
		t.Error("value wasn't saved to the global config")
	}
	if config.Sets(root, config.Project, []string{"worktreesDir"}) {
		t.Error("value also landed in the project config")
	}
}

func TestConfigOptionsDefaultToTheGlobalConfigUnlessPerRepo(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = selectOption(t, m, "Archive Done after")

	m = pressConfig(t, m, keyPress("enter"))
	if m.configLayer != config.Global {
		t.Fatal("a general option didn't default to the global config")
	}
	m.configInput.SetValue("30d")
	m = pressConfig(t, m, keyPress("enter"))
	if m.snap.Board.ArchiveAfter().Hours() != 30*24 {
		t.Errorf("archiveDoneAfter not applied: %v", m.snap.Board.ArchiveAfter())
	}
	if !config.Sets(root, config.Global, []string{"archiveDoneAfter"}) {
		t.Error("the first edit didn't create the global config")
	}

	for _, name := range []string{"Default base branch", "Worktrees directory", "Init script", "Symlink untracked files"} {
		if got := defaultConfigLayer(root, configOptions[optionIndex(t, name)]); got != config.Project {
			t.Errorf("%s defaults to the %s config", name, got)
		}
	}
}

func optionIndex(t *testing.T, name string) int {
	t.Helper()
	for i, opt := range configOptions {
		if opt.name == name {
			return i
		}
	}
	t.Fatalf("no option %q", name)
	return -1
}

func TestConfigResetOfASingleLayer(t *testing.T) {
	tests := []struct {
		name   string
		preset bool
		flash  string
	}{
		{"removes the global override", true, "Reset"},
		{"on a default row changes nothing", false, "already the default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t)
			if tt.preset {
				if err := config.Set(root, config.Global, []string{"autoName"}, false); err != nil {
					t.Fatal(err)
				}
			}
			m := configModel(t, root)
			m = selectOption(t, m, "Auto-name cards")
			if tt.preset && m.snap.Board.AutoName {
				t.Fatal("setup: global override not loaded")
			}

			m = pressConfig(t, m, key("x"))
			if config.Sets(root, config.Global, []string{"autoName"}) {
				t.Error("x didn't remove the global override")
			}
			if !m.snap.Board.AutoName {
				t.Error("autoName did not fall back to the default")
			}
			if !strings.Contains(m.flash, tt.flash) {
				t.Errorf("flash=%q", m.flash)
			}
			if _, err := os.Stat(config.GlobalPath()); !tt.preset && !os.IsNotExist(err) {
				t.Errorf("reset created the global config: %v", err)
			}
		})
	}
}

func TestConfigOptionsMatchTheBoard(t *testing.T) {
	board := config.Default()
	board.DefaultBaseBranch, board.DefaultPermissionMode, board.DefaultModel = "main", "auto", "opus"
	board.WorktreesDir, board.ArchiveDoneAfter, board.InitScript = "wt", "7d", "setup"
	board.MaxConcurrent, board.IdleTimeoutMinutes, board.LinkUntracked, board.DisableMouse = 4, 5, true, true
	board.TeamReviewRequests = true
	data, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	boardSeen := false
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

		if opt.board {
			boardSeen = true
		} else if boardSeen {
			t.Errorf("%s: board options must come last, renderConfig groups them by position", opt.name)
		}
	}
}

func TestConfigFixModeCyclesThroughTheChoices(t *testing.T) {
	root := newRoot(t)
	if err := config.WriteDefaultBoard(root, config.Global); err != nil {
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
		data, _ := os.ReadFile(config.GlobalPath())
		if !strings.Contains(string(data), `"changesRequestedMode": "`+want+`"`) {
			t.Errorf("the global config doesn't hold %q:\n%s", want, data)
		}
	}

	m = pressConfig(t, m, keyPress("x"))
	if config.Sets(root, config.Global, configOptions[m.configSel].path) {
		t.Error("x left changesRequestedMode in the global config")
	}
}

func TestConfigResetWithBothLayersRemovesProjectFirst(t *testing.T) {
	root := newRoot(t)
	if err := config.WriteDefaultBoard(root, config.Global); err != nil {
		t.Fatal(err)
	}
	path := []string{"worktreesDir"}
	if err := config.Set(root, config.Global, path, ".global-wt"); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(root, config.Project, path, ".project-wt"); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Worktrees directory")
	m.configSrc = configSources(root)

	m = pressConfig(t, m, key("x"))
	if config.Sets(root, config.Project, path) || !config.Sets(root, config.Global, path) {
		t.Fatal("the first x should remove only the project key")
	}
	if m.snap.Board.WorktreesDir != ".global-wt" || m.configSrc[m.configSel] != "global" {
		t.Errorf("worktreesDir=%q label=%q", m.snap.Board.WorktreesDir, m.configSrc[m.configSel])
	}

	m = pressConfig(t, m, key("x"))
	if config.Sets(root, config.Global, path) || m.configSrc[m.configSel] != "default" {
		t.Errorf("the second x should remove the global key: label=%q", m.configSrc[m.configSel])
	}
}

func TestConfigEmptyPrompt(t *testing.T) {
	path := []string{"defaultBaseBranch"}
	tests := []struct {
		name       string
		setup      func(t *testing.T, root string)
		input      string
		flash      string
		wantGlobal bool
		tab        bool
	}{
		{"removes the key from the chosen layer", func(t *testing.T, root string) {
			if err := config.Set(root, config.Project, path, "develop"); err != nil {
				t.Fatal(err)
			}
		}, "", "Reset", false, false},
		{"where nothing is set says so, after tab to the other layer", func(t *testing.T, root string) {
			if err := config.WriteDefaultBoard(root, config.Global); err != nil {
				t.Fatal(err)
			}
			if err := config.Set(root, config.Global, path, "main"); err != nil {
				t.Fatal(err)
			}
		}, "  ", "is not set in the project config", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t)
			tt.setup(t, root)
			m := configModel(t, root)
			m = selectOption(t, m, "Default base branch")

			m = pressConfig(t, m, keyPress("enter"))
			if tt.tab {
				m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyTab})
			}
			m.configInput.SetValue(tt.input)
			m = pressConfig(t, m, keyPress("enter"))
			if config.Sets(root, config.Project, path) {
				t.Error("an empty prompt left the project key")
			}
			if config.Sets(root, config.Global, path) != tt.wantGlobal {
				t.Errorf("global key set = %v, want %v", !tt.wantGlobal, tt.wantGlobal)
			}
			if !strings.Contains(m.flash, tt.flash) {
				t.Errorf("flash=%q", m.flash)
			}
		})
	}
}

func TestConfigListKeysAreTextInsideThePrompt(t *testing.T) {
	m := configModel(t, newRoot(t))
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
	root := newRoot(t)
	if err := config.WriteDefaultBoard(root, config.Global); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(config.GlobalPath())
	m := configModel(t, root)
	m = selectOption(t, m, "Archive Done after")

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("soon")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.HasPrefix(m.flash, "Error:") {
		t.Errorf("flash=%q", m.flash)
	}
	after, _ := os.ReadFile(config.GlobalPath())
	if string(before) != string(after) {
		t.Errorf("the global config changed after a rejected value:\n%s", after)
	}
}

func TestConfigMaxConcurrentNeedsAtLeastOne(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = selectOption(t, m, "Max concurrent agents")

	m = pressConfig(t, m, keyPress("enter"))
	m.configInput.SetValue("0")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.Contains(m.flash, "at least 1") {
		t.Errorf("flash=%q", m.flash)
	}
	if config.Sets(root, config.Global, []string{"maxConcurrent"}) {
		t.Error("0 was saved as the concurrency limit")
	}
}

func TestConfigWarnsWhenTheProjectConfigOverridesAGlobalSave(t *testing.T) {
	root := newRoot(t)
	if err := config.WriteDefaultBoard(root, config.Global); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(root, config.Project, []string{"worktreesDir"}, ".project-wt"); err != nil {
		t.Fatal(err)
	}
	m := configModel(t, root)
	m = selectOption(t, m, "Worktrees directory")

	m = pressConfig(t, m, keyPress("enter"))
	m = pressConfig(t, m, tea.KeyMsg{Type: tea.KeyTab})
	m.configInput.SetValue(".global-wt")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.Contains(m.flash, "overridden by project") {
		t.Errorf("flash=%q", m.flash)
	}
}

func TestConfigFooterAndRows(t *testing.T) {
	m := configModel(t, newRoot(t))
	m.width, m.height = 120, 40
	next, _ := m.updateBoard(key("o"))
	m = next.(Model)
	if !strings.Contains(m.renderFooter(), "x reset") {
		t.Errorf("footer=%q", m.renderFooter())
	}
	out := m.renderConfig()
	for _, want := range []string{"Auto-name cards", "Archive Done after", "in the config that holds the columns", "default"} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q:\n%s", want, out)
		}
	}
}

func TestConfigBoardOptionFollowsAProjectFlow(t *testing.T) {
	root := newRoot(t)
	if err := config.WriteDefaultBoard(root, config.Global); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(root, config.Project, []string{"prFlow"}, map[string]string{"opened": "in-review"}); err != nil {
		t.Fatal(err)
	}
	opt := configOptions[optionIndex(t, "PR review fix mode")]
	if got := defaultConfigLayer(root, opt); got != config.Project {
		t.Errorf("layer = %v, want the project config whose prFlow replaces the global one", got)
	}
}

func TestConfigListKeysStopAtBothEnds(t *testing.T) {
	m := configModel(t, newRoot(t))
	m.mode = modeConfig
	m = pressConfig(t, m, key("k"))
	m = pressConfig(t, m, special(tea.KeyUp))
	if m.configSel != 0 {
		t.Errorf("selection %d after going up from the first option", m.configSel)
	}
	for range len(configOptions) + 3 {
		m = pressConfig(t, m, key("j"))
	}
	last := len(configOptions)
	if m.configSel != last {
		t.Fatalf("selection %d after going down past the end, want the Columns… link at %d", m.configSel, last)
	}
	m = pressConfig(t, m, special(tea.KeyDown))
	if m = pressConfig(t, m, key("k")); m.configSel != last-1 {
		t.Errorf("selection %d after one up from the end, want %d", m.configSel, last-1)
	}
}

func TestConfigClosesOnEscQAndOAndOpensTheColumns(t *testing.T) {
	for _, k := range []tea.KeyMsg{special(tea.KeyEsc), key("q"), key("o")} {
		m := configModel(t, newRoot(t))
		m.mode = modeConfig
		if m = pressConfig(t, m, k); m.mode != modeBoard {
			t.Errorf("%q left the mode at %v, want the board", k.String(), m.mode)
		}
	}
	m := configModel(t, newRoot(t))
	m.mode = modeConfig
	if m = pressConfig(t, m, key("c")); m.mode != modeColumns {
		t.Errorf("c opened mode %v, want the columns view", m.mode)
	}
}

func TestConfigToggleInsideANestedObjectWritesTheNestedKey(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = selectOption(t, m, "Notify when a turn ends")
	before := m.snap.Board.Notifications.TurnDone

	m = pressConfig(t, m, keyPress("enter"))
	if m.snap.Board.Notifications.TurnDone == before {
		t.Error("the toggle didn't change the running board")
	}
	if !config.Sets(root, config.Global, []string{"notifications", "turnDone"}) {
		t.Errorf("notifications.turnDone wasn't saved to the global config (flash %q)", m.flash)
	}
	if config.Sets(root, config.Global, []string{"turnDone"}) {
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
		root := newRoot(t)
		m := configModel(t, root)
		m = selectOption(t, m, tt.option)
		m = pressConfig(t, m, keyPress("enter"))
		m.configInput.SetValue(tt.input)
		m = pressConfig(t, m, keyPress("enter"))
		if got := config.Sets(root, config.Global, configOptions[m.configSel].path); got != tt.saved {
			t.Errorf("%s %q: saved = %v, want %v (flash %q)", tt.option, tt.input, got, tt.saved, m.flash)
		}
		if rejected := strings.Contains(m.flash, "at least 0"); rejected == tt.saved {
			t.Errorf("%s %q: flash %q, rejected = %v, want %v", tt.option, tt.input, m.flash, rejected, !tt.saved)
		}
	}
}

func TestConfigListScrollsToKeepTheSelectionInView(t *testing.T) {
	first, last := configOptions[0].name, configOptions[len(configOptions)-1].name
	m := configModel(t, newRoot(t))
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

func typeInConfigSearch(t *testing.T, m Model, text string) Model {
	t.Helper()
	m.mode = modeConfig
	m = pressConfig(t, m, key("/"))
	for _, r := range text {
		m = pressConfig(t, m, key(string(r)))
	}
	return m
}

func visibleConfigNames(m Model) []string {
	var names []string
	for _, i := range m.configVisible() {
		names = append(names, configOptions[i].name)
	}
	return names
}

func TestConfigSearchNarrowsLiveAndKeepsOnEnter(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "notify")
	if !m.configFilterInput.Focused() {
		t.Fatal("/ didn't open the search")
	}
	if got := visibleConfigNames(m); len(got) != 4 {
		t.Errorf("visible=%v", got)
	}
	if configOptions[m.configSel].name != "Notify when a turn ends" {
		t.Errorf("selected %q", configOptions[m.configSel].name)
	}
	m = pressConfig(t, m, keyPress("enter"))
	if m.configFilterInput.Focused() || m.configQuery() != "notify" {
		t.Errorf("filtering=%v filter=%q", m.configFilterInput.Focused(), m.configQuery())
	}
	if !strings.Contains(m.renderConfig(), "search: notify") {
		t.Error("committed search isn't shown")
	}
}

func TestConfigSearchEscClearsBeforeClosing(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "mouse")
	m = pressConfig(t, m, keyPress("esc"))
	if m.configFilterInput.Focused() || m.configQuery() != "" || m.mode != modeConfig {
		t.Errorf("filtering=%v filter=%q mode=%v", m.configFilterInput.Focused(), m.configQuery(), m.mode)
	}

	m = typeInConfigSearch(t, m, "mouse")
	m = pressConfig(t, m, keyPress("enter"))
	m = pressConfig(t, m, keyPress("esc"))
	if m.configQuery() != "" || m.mode != modeConfig {
		t.Errorf("first esc: filter=%q mode=%v", m.configQuery(), m.mode)
	}
	m = pressConfig(t, m, keyPress("esc"))
	if m.mode != modeBoard {
		t.Error("second esc didn't close the view")
	}
}

func TestConfigSearchMatchesTheJSONKey(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "disableMouse")
	if got := visibleConfigNames(m); len(got) != 1 || got[0] != "Disable mouse in agents" {
		t.Errorf("visible=%v", got)
	}
}

func TestConfigSearchMovesOverVisibleRowsAndEditsTheRightOption(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = typeInConfigSearch(t, m, "notify")
	m = pressConfig(t, m, keyPress("enter"))
	m = pressConfig(t, m, key("j"))
	if name := configOptions[m.configSel].name; name != "Notify when an agent needs you" {
		t.Fatalf("selected %q", name)
	}
	for range 10 {
		m = pressConfig(t, m, key("j"))
	}
	if name := configOptions[m.configSel].name; name != "Notify when an agent exits" {
		t.Errorf("selected %q after moving past the end", name)
	}
	m = selectOption(t, m, "Notify when an agent needs you")
	m = pressConfig(t, m, keyPress("enter"))
	if m.snap.Board.Notifications.NeedsYou {
		t.Error("toggling the filtered option didn't save it")
	}
}

func TestConfigListKeysAreTextInsideTheSearch(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "qxoj")
	if m.mode != modeConfig || !m.configFilterInput.Focused() || m.configFilterInput.Value() != "qxoj" {
		t.Errorf("mode=%v filtering=%v value=%q", m.mode, m.configFilterInput.Focused(), m.configFilterInput.Value())
	}
}

func TestConfigSearchWithoutMatchesIsHarmless(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = typeInConfigSearch(t, m, "zzzz")
	m = pressConfig(t, m, keyPress("enter"))
	for _, k := range []tea.KeyMsg{keyPress("enter"), key("x"), key("j"), key("k")} {
		m = pressConfig(t, m, k)
	}
	if m.configEditing || !strings.Contains(m.renderConfig(), "No matching settings") {
		t.Error("an empty search misbehaved")
	}
	if !m.snap.Board.AutoName || config.Sets(root, config.Global, []string{"autoName"}) {
		t.Error("enter changed an option that no row shows")
	}
}

func TestConfigSearchDoesNotResetAnOptionThatIsHidden(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = selectOption(t, m, "Auto-name cards")
	m = pressConfig(t, m, keyPress("enter"))
	if !config.Sets(root, config.Global, []string{"autoName"}) {
		t.Fatal("setup: the toggle wasn't saved")
	}
	m = typeInConfigSearch(t, m, "zzzz")
	m = pressConfig(t, m, keyPress("enter"))
	m = pressConfig(t, m, key("x"))
	if !config.Sets(root, config.Global, []string{"autoName"}) {
		t.Error("x removed the override of an option that no row shows")
	}
}

func TestConfigSearchRowsShowTheirOwnSource(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = selectOption(t, m, "Split PRs into cards")
	m = pressConfig(t, m, keyPress("enter"))
	m = typeInConfigSearch(t, m, "split")
	m = pressConfig(t, m, keyPress("enter"))
	if !strings.Contains(m.renderConfig(), "global") {
		t.Errorf("filtered row lost its source label:\n%s", m.renderConfig())
	}
}

func TestConfigSearchEditsANonToggleOptionWhileFiltered(t *testing.T) {
	root := newRoot(t)
	m := configModel(t, root)
	m = typeInConfigSearch(t, m, "base branch")
	m = pressConfig(t, m, keyPress("enter"))
	m = pressConfig(t, m, keyPress("enter"))
	if !m.configEditing {
		t.Fatal("enter didn't open the prompt for the filtered option")
	}
	m.configFilterInput.SetValue("base branch")
	m.configInput.SetValue("develop")
	m = pressConfig(t, m, keyPress("enter"))
	if m.snap.Board.DefaultBaseBranch != "develop" {
		t.Errorf("DefaultBaseBranch=%q", m.snap.Board.DefaultBaseBranch)
	}
}

func TestConfigSearchIgnoresWhitespaceOnly(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "  ")
	if len(m.configVisible()) != len(configOptions) {
		t.Errorf("visible=%d", len(m.configVisible()))
	}
	m = pressConfig(t, m, keyPress("enter"))
	if strings.Contains(m.renderConfig(), "search:") {
		t.Error("a blank search shows a banner")
	}
}

func TestConfigSearchShowsTheBoardLabelOnlyForBoardRows(t *testing.T) {
	labels := func(m Model) int { return strings.Count(m.renderConfig(), "in the config that holds the columns") - 1 }
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "mouse")
	if labels(m) != 0 {
		t.Error("label shown without a board row")
	}
	m = pressConfig(t, m, keyPress("esc"))
	m = typeInConfigSearch(t, m, "fix mode")
	if labels(m) != 1 {
		t.Error("label missing for a board row")
	}
}

func TestConfigFooterShowsTheSearchPrompt(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "abc")
	if footer := m.renderFooter(); !strings.Contains(footer, "Search ›") || !strings.Contains(footer, "abc") {
		t.Errorf("footer=%q", footer)
	}
}

func TestConfigSearchReopensWithTheCommittedText(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "notify")
	m = pressConfig(t, m, keyPress("enter"))
	m = pressConfig(t, m, key("/"))
	if m.configFilterInput.Value() != "notify" {
		t.Errorf("value=%q", m.configFilterInput.Value())
	}
}

func TestConfigSearchSelectionRecoversAfterNoMatches(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "zzz")
	for range 3 {
		m = pressConfig(t, m, keyPress("backspace"))
	}
	for _, r := range "notify" {
		m = pressConfig(t, m, key(string(r)))
	}
	if name := configOptions[m.configSel].name; name != "Notify when a turn ends" {
		t.Errorf("selected %q", name)
	}
}

func TestConfigSearchArrowsMoveWhileTyping(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "notify")
	m = pressConfig(t, m, keyPress("down"))
	if name := configOptions[m.configSel].name; name != "Notify when an agent needs you" {
		t.Errorf("down selected %q", name)
	}
	m = pressConfig(t, m, keyPress("up"))
	if name := configOptions[m.configSel].name; name != "Notify when a turn ends" {
		t.Errorf("up selected %q", name)
	}
}

func TestConfigReopeningFromTheBoardClearsTheSearch(t *testing.T) {
	m := configModel(t, newRoot(t))
	m = typeInConfigSearch(t, m, "notify")
	m.mode = modeBoard
	next, _ := m.updateBoard(key("o"))
	m = next.(Model)
	if m.configQuery() != "" || m.configFilterInput.Focused() {
		t.Errorf("filter=%q filtering=%v", m.configQuery(), m.configFilterInput.Focused())
	}
}

func TestConfigColumnsRowOpensTheColumnEditor(t *testing.T) {
	m := configModel(t, newRoot(t))
	m.mode = modeConfig
	m.configSrc = configSources(m.eng.Root)
	for range len(configOptions) + 5 {
		m = pressConfig(t, m, key("j"))
	}
	if !m.onColumnsRow() {
		t.Fatalf("configSel = %d", m.configSel)
	}
	if out := m.renderConfig(); !strings.Contains(out, "Columns…") {
		t.Errorf("view is missing the link:\n%s", out)
	}
	if got := pressConfig(t, m, key("x")); got.mode != modeConfig || got.flash != "" {
		t.Errorf("x on the link did something: mode=%v flash=%q", got.mode, got.flash)
	}
	m = pressConfig(t, m, keyPress("enter"))
	if m.mode != modeColumns || m.columnsFrom != modeConfig {
		t.Errorf("mode=%v from=%v", m.mode, m.columnsFrom)
	}
}

func TestConfigSearchHidesTheColumnsLink(t *testing.T) {
	m := configModel(t, newRoot(t))
	m.mode = modeConfig
	for range len(configOptions) + 1 {
		m = pressConfig(t, m, key("j"))
	}
	if !m.onColumnsRow() {
		t.Fatalf("configSel=%d", m.configSel)
	}
	m = typeInConfigSearch(t, m, "zzzz")
	m = pressConfig(t, m, keyPress("enter"))
	if m.onColumnsRow() || strings.Contains(m.renderConfig(), "Columns…") {
		t.Error("the columns link is still reachable while a search matches nothing")
	}
	if got := pressConfig(t, m, keyPress("enter")); got.mode != modeConfig {
		t.Errorf("enter opened %v", got.mode)
	}
}
