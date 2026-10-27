package ui

import (
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
	get    func(*config.Board) any
}

var configOptions = []configOption{
	{name: "Auto-name cards", path: []string{"autoName"}, kind: optToggle,
		get: func(b *config.Board) any { return b.AutoName }},
	{name: "Max concurrent agents", path: []string{"maxConcurrent"}, kind: optNumber,
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

// configSource names the file a value comes from.
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

// commitConfig saves the typed value; an empty prompt removes the override.
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
		if err != nil {
			m.setFlash(opt.name + " needs a whole number")
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
// then shows the new board straight away.
func (m Model) saveConfig(layer config.Layer, opt configOption, value any) Model {
	var err error
	if value == nil {
		err = config.Unset(m.eng.Root, layer, opt.path)
	} else {
		err = config.Set(m.eng.Root, layer, opt.path, value)
	}
	if err != nil {
		m.setFlash("Error: " + err.Error())
		return m
	}
	m.eng.ReloadBoard()
	m.snap.Board = m.eng.Board()
	if value == nil {
		m.setFlash(fmt.Sprintf("Reset %s in %s", opt.name, layer.FileName()))
	} else {
		m.setFlash(fmt.Sprintf("Saved %s to %s", opt.name, layer.FileName()))
	}
	return m
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
		value, note := fmt.Sprintf("%-12s", configValue(opt, m.snap.Board)), dim.Render(configSource(m.eng.Root, opt))
		if selected && m.configEditing {
			value = m.configInput.View()
			note = dim.Render("→ " + m.configLayer.FileName())
		}
		lines = append(lines, prefix+name+value+"  "+note)
	}
	lines = append(lines, "", dim.Render("Columns, flows and automations live in drover.json only."))
	return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
}
