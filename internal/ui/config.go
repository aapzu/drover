package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/config"
)

type optKind int

const (
	optToggle optKind = iota
	optText
	optNumber
)

// configOption is a setting the config view can change. A shared option only
// lives in drover.json; the others default to drover.local.json.
type configOption struct {
	name   string
	path   []string
	kind   optKind
	shared bool
	min    int
	get    func(*config.Board) any
}

var configOptions = []configOption{
	{name: "Auto-name cards", path: []string{"autoName"}, kind: optToggle,
		get: func(b *config.Board) any { return b.AutoName }},
	{name: "Split PRs into cards", path: []string{"splitByPR"}, kind: optToggle,
		get: func(b *config.Board) any { return b.SplitByPR }},
	{name: "Max concurrent agents", path: []string{"maxConcurrent"}, kind: optNumber, min: 1,
		get: func(b *config.Board) any { return b.Limit() }},
	{name: "Idle timeout (minutes)", path: []string{"idleTimeoutMinutes"}, kind: optNumber,
		get: func(b *config.Board) any { return b.IdleTimeoutMinutes }},
	{name: "Default base branch", path: []string{"defaultBaseBranch"}, kind: optText,
		get: func(b *config.Board) any { return b.DefaultBaseBranch }},
	{name: "Worktrees directory", path: []string{"worktreesDir"}, kind: optText,
		get: func(b *config.Board) any { return b.WorktreesDir }},
	{name: "Notify when a turn ends", path: []string{"notifications", "turnDone"}, kind: optToggle,
		get: func(b *config.Board) any { return b.Notifications.TurnDone }},
	{name: "Notify when an agent needs you", path: []string{"notifications", "needsYou"}, kind: optToggle,
		get: func(b *config.Board) any { return b.Notifications.NeedsYou }},
	{name: "Notify when a plan is ready", path: []string{"notifications", "planReady"}, kind: optToggle,
		get: func(b *config.Board) any { return b.Notifications.PlanReady }},
	{name: "Notify when an agent exits", path: []string{"notifications", "exited"}, kind: optToggle,
		get: func(b *config.Board) any { return b.Notifications.Exited }},
	{name: "Notification cooldown (seconds)", path: []string{"notifications", "cooldownSeconds"}, kind: optNumber,
		get: func(b *config.Board) any { return b.Notifications.CooldownSeconds }},
	{name: "Default permission mode", path: []string{"defaultPermissionMode"}, kind: optText, shared: true,
		get: func(b *config.Board) any { return b.DefaultPermissionMode }},
	{name: "Default model", path: []string{"defaultModel"}, kind: optText, shared: true,
		get: func(b *config.Board) any { return b.DefaultModel }},
	{name: "Archive Done after", path: []string{"archiveDoneAfter"}, kind: optText, shared: true,
		get: func(b *config.Board) any { return b.ArchiveDoneAfter }},
	{name: "Init script", path: []string{"initScript"}, kind: optText, shared: true,
		get: func(b *config.Board) any { return b.InitScript }},
	{name: "Symlink untracked files", path: []string{"linkUntracked"}, kind: optToggle, shared: true,
		get: func(b *config.Board) any { return b.LinkUntracked }},
}

func defaultConfigLayer(opt configOption) config.Layer {
	if opt.shared {
		return config.Shared
	}
	return config.Local
}

// configText is the value as typed into the prompt.
func configText(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

// configValue is the value as shown in the list.
func configValue(opt configOption, b *config.Board) string {
	v := opt.get(b)
	if on, ok := v.(bool); ok {
		if on {
			return "on"
		}
		return "off"
	}
	if s, ok := v.(string); ok && s == "" {
		return "—"
	}
	return configText(v)
}

// sourceLayer is the layer that sets opt's value; false when it is the default.
func sourceLayer(root string, opt configOption) (config.Layer, bool) {
	if !opt.shared && config.Sets(root, config.Local, opt.path) {
		return config.Local, true
	}
	if config.Sets(root, config.Shared, opt.path) {
		return config.Shared, true
	}
	return 0, false
}

// configSource labels where opt's value comes from: local, shared or default.
func configSource(root string, opt configOption) string {
	layer, ok := sourceLayer(root, opt)
	switch {
	case !ok:
		return "default"
	case layer == config.Local:
		return "local"
	}
	return "shared"
}

func (m Model) updateConfig(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.configEditing {
		return m.updateConfigInput(msg)
	}
	switch msg.String() {
	case "esc", "q", "o":
		m.mode = modeBoard
	case "up", "k":
		m.configSel = max(0, m.configSel-1)
	case "down", "j":
		m.configSel = min(len(configOptions)-1, m.configSel+1)
	case "enter":
		return m.beginConfigEdit()
	case "x":
		return m.resetConfig(), nil
	}
	return m, nil
}

func (m Model) beginConfigEdit() (tea.Model, tea.Cmd) {
	opt := configOptions[m.configSel]
	if opt.kind == optToggle {
		on, _ := opt.get(m.snap.Board).(bool)
		return m.saveConfig(defaultConfigLayer(opt), opt, !on), nil
	}
	m.configEditing = true
	m.configLayer = defaultConfigLayer(opt)
	m.configInput.SetValue(configText(opt.get(m.snap.Board)))
	m.configInput.CursorEnd()
	return m, m.configInput.Focus()
}

func (m Model) updateConfigInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	opt := configOptions[m.configSel]
	switch msg.String() {
	case "esc":
		m.configEditing = false
		m.configInput.Blur()
		return m, nil
	case "enter":
		m.configEditing = false
		m.configInput.Blur()
		return m.commitConfig(opt), nil
	case "tab":
		if !opt.shared {
			if m.configLayer == config.Local {
				m.configLayer = config.Shared
			} else {
				m.configLayer = config.Local
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.configInput, cmd = m.configInput.Update(msg)
	return m, cmd
}

// commitConfig saves the typed value. An empty prompt removes the key from the
// chosen layer, or says it isn't set there.
func (m Model) commitConfig(opt configOption) Model {
	text := strings.TrimSpace(m.configInput.Value())
	switch {
	case text == "":
		if !config.Sets(m.eng.Root, m.configLayer, opt.path) {
			m.setFlash(fmt.Sprintf("%s is not set in %s", opt.name, m.configLayer.FileName()))
			return m
		}
		return m.saveConfig(m.configLayer, opt, nil)
	case opt.kind == optNumber:
		n, err := strconv.Atoi(text)
		if err != nil || n < opt.min {
			m.setFlash(fmt.Sprintf("%s needs a whole number of at least %d", opt.name, opt.min))
			return m
		}
		return m.saveConfig(m.configLayer, opt, n)
	}
	return m.saveConfig(m.configLayer, opt, text)
}

// resetConfig removes the override the list shows for the selected option.
func (m Model) resetConfig() Model {
	opt := configOptions[m.configSel]
	layer, ok := sourceLayer(m.eng.Root, opt)
	if !ok {
		m.setFlash(opt.name + " is already the default")
		return m
	}
	return m.saveConfig(layer, opt, nil)
}

// saveConfig writes value to the layer, or removes the key when value is nil,
// then reloads the board and the source labels.
func (m Model) saveConfig(layer config.Layer, opt configOption, value any) Model {
	var err error
	done := "Saved %s to %s"
	if value == nil {
		err = config.Unset(m.eng.Root, layer, opt.path)
		done = "Reset %s in %s"
	} else {
		err = config.Set(m.eng.Root, layer, opt.path, value)
	}
	if errors.Is(err, config.ErrNoSharedFile) {
		err = fmt.Errorf("%w; close settings and press W to write the default board", err)
	}
	if err != nil {
		m.setFlash("Error: " + err.Error())
		return m
	}
	m.eng.ReloadBoard()
	m.snap.Board = m.eng.Board()
	m.configSrc = configSources(m.eng.Root)
	msg := fmt.Sprintf(done, opt.name, layer.FileName())
	if src, ok := sourceLayer(m.eng.Root, opt); value != nil && ok && src != layer {
		msg += fmt.Sprintf(" (overridden by %s)", src.FileName())
	}
	m.setFlash(msg)
	return m
}

// configSources labels where each option's value comes from. It runs when the
// view opens and after each save, so rendering doesn't read the files.
func configSources(root string) []string {
	sources := make([]string, len(configOptions))
	for i, opt := range configOptions {
		sources[i] = configSource(root, opt)
	}
	return sources
}

func (m Model) renderConfig() string {
	width := min(76, m.width-4)
	lines := []string{bold.Render("Settings"), ""}
	for i, opt := range configOptions {
		if opt.shared && (i == 0 || !configOptions[i-1].shared) {
			lines = append(lines, "", dim.Render("drover.json only"))
		}
		selected := i == m.configSel
		prefix, name := "  ", fmt.Sprintf("%-32s", opt.name)
		if selected {
			prefix, name = "▸ ", bold.Render(name)
		}
		value, note := fmt.Sprintf("%-12s", configValue(opt, m.snap.Board)), dim.Render(m.configSrc[i])
		if selected && m.configEditing {
			value = m.configInput.View()
			note = dim.Render("→ " + m.configLayer.FileName())
		}
		lines = append(lines, prefix+name+value+"  "+note)
	}
	lines = append(lines, "", dim.Render("Columns, flows and automations live in drover.json only."))
	return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
}
