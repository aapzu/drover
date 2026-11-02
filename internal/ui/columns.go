package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
)

// columnOption is a setting of a column the columns view can change.
type columnOption struct {
	name     string
	key      string
	kind     optKind
	local    bool
	required bool
	values   []string
	get      func(config.Column) any
}

var columnOptions = []columnOption{
	{name: "Name", key: "name", kind: optText, required: true,
		get: func(c config.Column) any { return c.Name }},
	{name: "Color", key: "color", kind: optText, local: true,
		get: func(c config.Column) any { return c.Color }},
	{name: "Permission mode", key: "permissionMode", kind: optText, local: true, values: claude.PermissionModes(),
		get: func(c config.Column) any { return c.PermissionMode }},
	{name: "Model", key: "modelOverride", kind: optText, local: true,
		get: func(c config.Column) any { return c.ModelOverride }},
	{name: "Effort", key: "effortOverride", kind: optText, local: true, values: claude.Efforts,
		get: func(c config.Column) any { return c.EffortOverride }},
	{name: "Run an agent", key: "autoSpawn", kind: optToggle,
		get: func(c config.Column) any { return c.AutoSpawn }},
	{name: "Keep the agent running", key: "keepAgent", kind: optToggle,
		get: func(c config.Column) any { return c.KeepAgent }},
	{name: "Plan exit target", key: "planExitTarget", kind: optText,
		get: func(c config.Column) any { return c.PlanExitTarget }},
	{name: "Skip the worktree", key: "noWorktree", kind: optToggle,
		get: func(c config.Column) any { return c.NoWorktree }},
}

func defaultColumnLayer(opt columnOption) config.Layer {
	if opt.local {
		return config.Local
	}
	return config.Shared
}

func columnSourceLayer(root, id string, opt columnOption) (config.Layer, bool) {
	path := config.ColumnPath(id, opt.key)
	if opt.local && config.Sets(root, config.Local, path) {
		return config.Local, true
	}
	if config.Sets(root, config.Shared, path) {
		return config.Shared, true
	}
	return 0, false
}

// columnSources labels where each of the column's settings comes from.
func columnSources(root, id string) []string {
	sources := make([]string, len(columnOptions))
	for i, opt := range columnOptions {
		sources[i] = sourceLabel(columnSourceLayer(root, id, opt))
	}
	return sources
}

func columnValue(opt columnOption, c config.Column) string {
	switch v := opt.get(c).(type) {
	case bool:
		if v {
			return "on"
		}
		return "off"
	case string:
		if v == "" {
			return "—"
		}
		return v
	}
	return ""
}

func (m Model) openColumns() Model {
	cols := m.snap.Board.Columns
	if len(cols) == 0 {
		m.setFlash("No columns to edit")
		return m
	}
	m.mode = modeColumns
	m.columnOpen = false
	m.columnEditing = false
	m.columnSel = clamp(m.col, 0, len(cols)-1)
	return m
}

func (m Model) selectedColumn() (config.Column, bool) {
	if m.columnOpen {
		c, _, ok := m.snap.Board.Column(m.columnID)
		return c, ok
	}
	cols := m.snap.Board.Columns
	if m.columnSel < 0 || m.columnSel >= len(cols) {
		return config.Column{}, false
	}
	return cols[m.columnSel], true
}

func (m Model) updateColumns(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	col, ok := m.selectedColumn()
	if !ok {
		if m.columnOpen {
			m.setFlash("The column was removed from the board")
		}
		m.mode = modeBoard
		return m, nil
	}
	if m.columnOpen {
		m.columnSrc = columnSources(m.eng.Root, col.ID)
	}
	if m.columnEditing {
		return m.updateColumnInput(msg, col)
	}
	if !m.columnOpen {
		switch msg.String() {
		case "esc", "q", "O":
			m.mode = modeBoard
		case "up", "k":
			m.columnSel = max(0, m.columnSel-1)
		case "down", "j":
			m.columnSel = min(len(m.snap.Board.Columns)-1, m.columnSel+1)
		case "enter", "right", "l":
			m.columnOpen, m.columnID, m.columnOptSel = true, col.ID, 0
			m.columnSrc = columnSources(m.eng.Root, col.ID)
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q", "left", "h":
		m.columnOpen = false
	case "up", "k":
		m.columnOptSel = max(0, m.columnOptSel-1)
	case "down", "j":
		m.columnOptSel = min(len(columnOptions)-1, m.columnOptSel+1)
	case "enter":
		return m.beginColumnEdit(col)
	case "x":
		m, _ = m.resetColumn(col)
	}
	return m, nil
}

func (m Model) beginColumnEdit(col config.Column) (tea.Model, tea.Cmd) {
	opt := columnOptions[m.columnOptSel]
	if opt.kind == optToggle {
		on, _ := opt.get(col).(bool)
		m, _ = m.saveColumn(col, opt, defaultColumnLayer(opt), !on)
		return m, nil
	}
	m.columnEditing = true
	m.configLayer = defaultColumnLayer(opt)
	m.configInput.SetValue(configText(opt.get(col)))
	m.configInput.CursorEnd()
	return m, m.configInput.Focus()
}

func (m Model) updateColumnInput(msg tea.KeyMsg, col config.Column) (tea.Model, tea.Cmd) {
	opt := columnOptions[m.columnOptSel]
	switch msg.String() {
	case "esc":
		m.columnEditing = false
		m.configInput.Blur()
		return m, nil
	case "enter":
		var saved bool
		if m, saved = m.commitColumn(col, opt); saved {
			m.columnEditing = false
			m.configInput.Blur()
		}
		return m, nil
	case "tab":
		if opt.local {
			m.configLayer = otherLayer(m.configLayer)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.configInput, cmd = m.configInput.Update(msg)
	return m, cmd
}

// commitColumn saves the typed value and reports whether it was written.
func (m Model) commitColumn(col config.Column, opt columnOption) (Model, bool) {
	text := strings.TrimSpace(m.configInput.Value())
	switch {
	case text == "" && opt.required:
		m.setFlash(opt.name + " can't be empty")
	case text == "":
		if !config.Sets(m.eng.Root, m.configLayer, config.ColumnPath(col.ID, opt.key)) {
			m.setFlash(fmt.Sprintf("%s is not set in %s", opt.name, m.configLayer.FileName()))
			break
		}
		return m.saveColumn(col, opt, m.configLayer, nil)
	case opt.values != nil && !slices.Contains(opt.values, text):
		m.setFlash(fmt.Sprintf("%s is one of %s", opt.name, strings.Join(opt.values, ", ")))
	default:
		return m.saveColumn(col, opt, m.configLayer, text)
	}
	return m, false
}

// resetColumn removes the override the list shows for the selected setting.
func (m Model) resetColumn(col config.Column) (Model, bool) {
	opt := columnOptions[m.columnOptSel]
	layer, ok := columnSourceLayer(m.eng.Root, col.ID, opt)
	switch {
	case opt.required:
		m.setFlash(opt.name + " can't be reset")
		return m, false
	case !ok:
		m.setFlash(opt.name + " is already the default")
		return m, false
	}
	return m.saveColumn(col, opt, layer, nil)
}

// saveColumn writes value to the layer, or removes the key when value is nil
// or false, then reloads the board. It reports whether the file changed.
func (m Model) saveColumn(col config.Column, opt columnOption, layer config.Layer, value any) (Model, bool) {
	if on, ok := value.(bool); ok && !on {
		value = nil
	}
	m, err := m.writeSetting(layer, config.ColumnPath(col.ID, opt.key), value, "this view")
	if err != nil {
		m.setFlash("Error: " + err.Error())
		return m, false
	}
	m.columnSrc = columnSources(m.eng.Root, col.ID)
	done := "Saved %s of %s to %s"
	if value == nil {
		done = "Reset %s of %s in %s"
	}
	msg := fmt.Sprintf(done, opt.name, col.Name, layer.FileName())
	if src, ok := columnSourceLayer(m.eng.Root, col.ID, opt); value != nil && ok && src != layer {
		msg += fmt.Sprintf(" (overridden by %s)", src.FileName())
	}
	if opt.key == "name" && namedByTargets(m.snap.Board, col.Name) {
		msg += fmt.Sprintf("; planExitTarget or automations still name %q", col.Name)
	}
	m.setFlash(msg)
	return m, true
}

func namedByTargets(b *config.Board, name string) bool {
	if _, ok := b.Find(name); ok {
		return false
	}
	for _, c := range b.Columns {
		targets := []string{c.PlanExitTarget}
		for _, a := range slices.Concat(c.Automations.OnEnter, c.Automations.OnTurnEnd) {
			targets = append(targets, a.TargetColumn)
		}
		if slices.ContainsFunc(targets, func(t string) bool { return strings.EqualFold(t, name) }) {
			return true
		}
	}
	return false
}

func (m Model) columnsHint() string {
	switch {
	case m.columnEditing && columnOptions[m.columnOptSel].local:
		return "enter save  tab drover.json / drover.local.json  esc cancel"
	case m.columnEditing:
		return "enter save  esc cancel"
	case m.columnOpen:
		return "↑↓ select  enter change  x reset  esc back"
	}
	return "↑↓ select  enter open  esc close"
}

func (m Model) renderColumns() string {
	width := min(76, m.width-4)
	cols := m.snap.Board.Columns
	if !m.columnOpen {
		lines := []string{bold.Render("Columns"), ""}
		for i, c := range cols {
			prefix, name := "  ", fmt.Sprintf("%-20s", c.Name)
			if i == m.columnSel {
				prefix, name = "▸ ", bold.Render(name)
			}
			lines = append(lines, prefix+name+dim.Render(columnSummary(c)))
		}
		return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
	}
	col, ok := m.selectedColumn()
	if !ok {
		return ""
	}
	lines := []string{bold.Render("Columns › " + col.Name), ""}
	for i, opt := range columnOptions {
		prefix, name := "  ", fmt.Sprintf("%-26s", opt.name)
		if i == m.columnOptSel {
			prefix, name = "▸ ", bold.Render(name)
		}
		value, note := fmt.Sprintf("%-14s", columnValue(opt, col)), dim.Render(m.columnSrc[i])
		if i == m.columnOptSel && m.columnEditing {
			value = m.configInput.View()
			note = dim.Render("→ " + m.configLayer.FileName())
		}
		lines = append(lines, prefix+name+value+"  "+note)
	}
	lines = append(lines, "", dim.Render("Flows and automations live in drover.json only."))
	return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
}
