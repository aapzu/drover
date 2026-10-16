package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapelihaanpuu/kangentic-tui/internal/config"
	"github.com/aapelihaanpuu/kangentic-tui/internal/store"
)

var (
	accent     = lipgloss.Color("#8b5cf6")
	dim        = lipgloss.NewStyle().Foreground(lipgloss.Color("#6b7280"))
	bold       = lipgloss.NewStyle().Bold(true)
	thinking   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b"))
	needsYou   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Bold(true)
	idle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#10b981"))
	muted      = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ca3af"))
	headerBar  = lipgloss.NewStyle().Background(lipgloss.Color("#1f2937")).Foreground(lipgloss.Color("#e5e7eb"))
	cardStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#374151")).Padding(0, 1)
	cardActive = cardStyle.BorderForeground(accent)
	panelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#374151"))
	modalStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 2)
	spinner    = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

const (
	minColumnWidth = 28
	cardHeight     = 5
)

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	bodyHeight := m.height - lipgloss.Height(header) - lipgloss.Height(footer)

	var body string
	switch m.mode {
	case modeForm:
		body = m.place(bodyHeight, m.renderForm())
	case modeHelp:
		body = m.place(bodyHeight, m.renderHelp())
	case modeDetail:
		body = panelStyle.Width(m.width - 2).Height(bodyHeight - 2).Render(m.detail.View())
	default:
		body = m.renderMain(bodyHeight)
		switch m.mode {
		case modeMove:
			body = m.place(bodyHeight, m.renderMovePicker())
		case modeConfirm:
			body = m.place(bodyHeight, modalStyle.Width(min(70, m.width-4)).Render(m.confirmText))
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) place(height int, content string) string {
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, content)
}

func (m Model) renderHeader() string {
	var working, waiting, attention int
	for _, t := range m.snap.Tasks {
		for _, s := range t.Sessions {
			if s.Status != store.StatusRunning {
				continue
			}
			switch {
			case s.Activity == store.ActivityPermission || stuckStarting(s):
				attention++
			case s.Activity == store.ActivityIdle:
				waiting++
			default:
				working++
			}
		}
	}
	left := bold.Render(" ▦ kangentic") + "  " + m.eng.ProjectName
	if m.branch != "" {
		left += dim.Render(" on ") + m.branch
	}
	var stats []string
	if working > 0 {
		stats = append(stats, thinking.Render(fmt.Sprintf("%s %d working", spinner[m.frame%len(spinner)], working)))
	}
	if waiting > 0 {
		stats = append(stats, idle.Render(fmt.Sprintf("● %d idle", waiting)))
	}
	if attention > 0 {
		stats = append(stats, needsYou.Render(fmt.Sprintf("◆ %d need you", attention)))
	}
	right := strings.Join(stats, "  ") + " "
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	return headerBar.Width(m.width).Render(left + strings.Repeat(" ", gap) + right)
}

func (m Model) renderFooter() string {
	if m.mode == modeSend {
		return " " + bold.Render("Send ›") + " " + m.sendInput.View()
	}
	if m.flash != "" {
		return " " + muted.Render(ansi.Truncate(m.flash, m.width-2, "…"))
	}
	hints := map[mode]string{
		modeBoard:   "←→↑↓ navigate  H/L move  m move to…  n new  enter attach  s send  r start  S suspend  p preview  i info  ? help  q quit",
		modeForm:    "tab switch field  ctrl+s save  esc cancel",
		modeMove:    "1-9 pick column  any other key cancels",
		modeConfirm: "y confirm  any other key cancels",
		modeDetail:  "↑↓ scroll  esc close",
		modeHelp:    "any key closes",
	}
	return " " + dim.Render(ansi.Truncate(hints[m.mode], m.width-2, "…"))
}

func (m Model) renderMain(height int) string {
	previewHeight := 0
	if m.preview && height > 24 {
		previewHeight = height * 2 / 5
	}
	board := m.renderBoard(height - previewHeight)
	if previewHeight == 0 {
		return board
	}
	return lipgloss.JoinVertical(lipgloss.Left, board, m.renderPreview(previewHeight))
}

// visibleColumns returns the window of columns that fits the width, keeping
// the selected one in view.
func (m Model) visibleColumns() (start, end, width int) {
	n := len(m.columns())
	fit := max(1, m.width/minColumnWidth)
	if fit >= n {
		return 0, n, m.width / n
	}
	start = max(0, min(m.col-fit/2, n-fit))
	return start, start + fit, m.width / fit
}

func (m Model) renderBoard(height int) string {
	start, end, width := m.visibleColumns()
	var cols []string
	for i := start; i < end; i++ {
		cols = append(cols, m.renderColumn(i, width, height))
	}
	board := lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	if start > 0 || end < len(m.columns()) {
		board = lipgloss.PlaceHorizontal(m.width, lipgloss.Left, board)
	}
	return board
}

func columnSummary(c config.Column) string {
	switch {
	case c.IsTodo():
		return "backlog"
	case c.IsDone():
		return "archived · worktree removed"
	case !c.AutoSpawn:
		return "agent paused"
	}
	parts := []string{}
	for _, p := range []string{c.PermissionMode, c.ModelOverride, c.EffortOverride} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if c.Isolated() {
		parts = append(parts, "isolated")
	}
	if n := len(c.Automations.OnEnter) + len(c.Automations.OnTurnEnd) + len(c.Automations.OnExit); n > 0 {
		parts = append(parts, fmt.Sprintf("⚡%d", n))
	}
	if len(parts) == 0 {
		return "agent"
	}
	return strings.Join(parts, " · ")
}

func (m Model) renderColumn(i, width, height int) string {
	col := m.columns()[i]
	tasks := m.tasksForColumn(i)
	selected := i == m.col
	color := lipgloss.Color(col.Color)
	if col.Color == "" {
		color = lipgloss.Color("#9ca3af")
	}

	inner := width - 2
	nameStyle := lipgloss.NewStyle().Foreground(color).Bold(true).Underline(selected)
	count := fmt.Sprintf(" %d", len(tasks))
	name := ansi.Truncate(fmt.Sprintf("%d %s", i+1, col.Name), inner-1-len(count), "…")
	lines := []string{
		" " + nameStyle.Render(name) + dim.Render(count),
		" " + dim.Render(ansi.Truncate(columnSummary(col), inner-1, "…")),
	}

	slots := max(1, (height-len(lines))/cardHeight)
	sel := -1
	if selected {
		sel = m.selectedIndex()
	}
	first := 0
	if sel >= slots {
		first = sel - slots + 1
	}
	for j := first; j < len(tasks) && j < first+slots; j++ {
		lines = append(lines, m.renderCard(tasks[j], inner, selected && j == sel))
	}
	if hidden := len(tasks) - (first + slots); hidden > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("  +%d more", hidden)))
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Padding(0, 1, 0, 0).Render(strings.Join(lines, "\n"))
}

func (m Model) sessionFor(t *store.Task) *store.Session {
	col, _, _ := m.snap.Board.Column(t.ColumnID)
	key := store.MainSession
	if col.Isolated() {
		key = col.ID
	}
	if s := t.Sessions[key]; s != nil {
		return s
	}
	for _, s := range t.Sessions {
		if s.Status == store.StatusRunning {
			return s
		}
	}
	return nil
}

func (m Model) status(t *store.Task) string {
	s := m.sessionFor(t)
	if s == nil {
		return ""
	}
	switch s.Status {
	case store.StatusQueued:
		return muted.Render("◷ queued")
	case store.StatusSuspended:
		return dim.Render("⏸ suspended")
	case store.StatusExited:
		return dim.Render("■ exited")
	}
	pending := ""
	if len(s.Pending) > 0 {
		pending = dim.Render(fmt.Sprintf(" +%d queued", len(s.Pending)))
	}
	if stuckStarting(s) {
		return needsYou.Render("◆ waiting to start") + pending
	}
	switch s.Activity {
	case store.ActivityPermission:
		return needsYou.Render("◆ needs you") + pending
	case store.ActivityIdle:
		return idle.Render("● idle") + pending
	case store.ActivityStarting:
		return thinking.Render(spinner[m.frame%len(spinner)]+" starting") + pending
	default:
		return thinking.Render(spinner[m.frame%len(spinner)]+" working") + pending
	}
}

// stuckStarting flags an agent that never reported SessionStart, usually
// because a prompt such as the folder-trust dialog is waiting in its terminal.
func stuckStarting(s *store.Session) bool {
	return s.Status == store.StatusRunning && s.Activity == store.ActivityStarting && time.Since(s.UpdatedAt) > 10*time.Second
}

func (m Model) renderCard(t *store.Task, width int, active bool) string {
	content := width - 4
	title := fmt.Sprintf("#%d %s", t.Number, t.Title)
	wrapped := strings.Split(ansi.Wordwrap(title, content, " -"), "\n")
	line1 := ansi.Truncate(wrapped[0], content, "…")
	line2 := ""
	if len(wrapped) > 1 {
		line2 = ansi.Truncate(strings.Join(wrapped[1:], " "), content, "…")
	}
	if active {
		line1, line2 = bold.Render(line1), bold.Render(line2)
	}
	meta := m.status(t)
	if t.Branch != "" {
		branch := dim.Render(" " + t.Branch)
		if meta == "" {
			branch = dim.Render(t.Branch)
		}
		meta = ansi.Truncate(meta+branch, content, "…")
	}
	style := cardStyle
	if active {
		style = cardActive
	}
	// Width excludes the border, which adds one cell on each side.
	return style.Width(width - 2).Render(line1 + "\n" + line2 + "\n" + meta)
}

func (m Model) renderPreview(height int) string {
	t := m.current()
	title := " no task selected "
	body := ""
	if t != nil {
		title = fmt.Sprintf(" #%d %s ", t.Number, t.Title)
		if _, ok := m.eng.LiveSession(t.ID); !ok {
			body = dim.Render("No running agent. Move the card into an agent column, or press r to resume.")
			if s := m.sessionFor(t); s != nil && s.LastReply != "" {
				body += "\n\n" + muted.Render("Last reply:") + "\n" + s.LastReply
			}
		} else if m.previewFor == t.ID {
			body = lastLines(m.previewTxt, height-2)
			title += dim.Render("· enter to attach, Ctrl-q inside to return ")
		}
	}
	inner := m.width - 2
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, inner, "")
	}
	if len(lines) > height-2 {
		lines = lines[len(lines)-(height-2):]
	}
	box := panelStyle.Width(inner).Height(height - 2).Render(strings.Join(lines, "\n"))
	// Draw the title into the top border.
	top, rest, _ := strings.Cut(box, "\n")
	label := ansi.Truncate(title, inner-2, "…")
	top = ansi.Truncate(top, 2, "") + label + ansi.TruncateLeft(top, 2+lipgloss.Width(label), "")
	return top + "\n" + rest
}

// lastLines keeps the bottom n non-trailing-blank lines of a pane capture.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderForm() string {
	heading := "New task"
	if m.editingID != "" {
		heading = "Edit task"
	}
	return modalStyle.Width(min(104, m.width-4)).Render(strings.Join([]string{
		bold.Render(heading),
		"",
		muted.Render("Title"),
		m.titleInput.View(),
		"",
		muted.Render("Prompt"),
		m.promptInput.View(),
		"",
		dim.Render("tab: switch field · ctrl+s: save · esc: cancel"),
	}, "\n"))
}

func (m Model) renderMovePicker() string {
	t := m.task(m.modalTask)
	if t == nil {
		return ""
	}
	lines := []string{bold.Render(fmt.Sprintf("Move #%d to…", t.Number)), ""}
	for i, c := range m.columns() {
		if i >= 9 {
			break
		}
		marker := "  "
		if c.ID == t.ColumnID {
			marker = "▸ "
		}
		lines = append(lines, fmt.Sprintf("%s%s  %s  %s", marker, bold.Render(fmt.Sprint(i+1)), c.Name, dim.Render(columnSummary(c))))
	}
	return modalStyle.Render(strings.Join(lines, "\n"))
}

func (m Model) renderHelp() string {
	rows := [][2]string{
		{"←→ / h l", "select column"},
		{"↑↓ / j k, g G", "select card"},
		{"H / L, shift+←→", "move card to previous / next column"},
		{"m 1-9", "move card to column"},
		{"n", "new task (lands in To Do)"},
		{"e", "edit title and prompt"},
		{"x / D", "delete task (branch is kept)"},
		{"enter / a", "attach to the agent's terminal (Ctrl-q returns)"},
		{"s", "send a message to the agent"},
		{"r", "start or resume the agent in place"},
		{"S", "suspend the agent (resume later with r)"},
		{"p", "toggle live preview"},
		{"i", "task details and automation log"},
		{"W", "write the default board to kangentic.json"},
		{"q", "quit (agents keep running in tmux)"},
	}
	var lines []string
	lines = append(lines, bold.Render("Keys"), "")
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("%-18s %s", r[0], muted.Render(r[1])))
	}
	lines = append(lines, "", dim.Render("Moving a card into an agent column creates its worktree and starts Claude"),
		dim.Render("with that column's permission mode, model and effort, then runs its automations."))
	return modalStyle.Render(strings.Join(lines, "\n"))
}

func (m Model) renderDetail(t *store.Task) string {
	col, _, _ := m.snap.Board.Column(t.ColumnID)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", bold.Render(fmt.Sprintf("#%d %s", t.Number, t.Title)))
	field := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s %s\n", muted.Render(fmt.Sprintf("%-10s", k)), v)
		}
	}
	field("Column", col.Name)
	field("Branch", t.Branch)
	field("Base", t.BaseBranch)
	field("Worktree", t.WorktreePath)
	field("Created", t.CreatedAt.Format(time.DateTime))
	if t.Prompt != "" {
		fmt.Fprintf(&b, "\n%s\n%s\n", muted.Render("Prompt"), t.Prompt)
	}
	if len(t.Sessions) > 0 {
		fmt.Fprintf(&b, "\n%s\n", muted.Render("Sessions"))
		for key, s := range t.Sessions {
			name := "main"
			if key != store.MainSession {
				if c, _, ok := m.snap.Board.Column(key); ok {
					name = c.Name + " (isolated)"
				}
			}
			fmt.Fprintf(&b, "  %s  %s/%s  %s  tmux:%s\n", name, s.Status, s.Activity, dim.Render(s.AgentSessionID), s.Tmux)
		}
	}
	if s := m.sessionFor(t); s != nil && s.LastReply != "" {
		fmt.Fprintf(&b, "\n%s\n%s\n", muted.Render("Last reply"), s.LastReply)
	}
	fmt.Fprintf(&b, "\n%s\n", muted.Render("Activity"))
	for i := len(t.Log) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "  %s  %s\n", dim.Render(t.Log[i].At.Format("01-02 15:04:05")), t.Log[i].Text)
	}
	return lipgloss.NewStyle().Width(m.width - 6).Render(b.String())
}
