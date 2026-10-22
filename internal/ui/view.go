package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

var (
	accent     = lipgloss.Color("#8b5cf6")
	dim        = lipgloss.NewStyle().Foreground(lipgloss.Color("#6b7280"))
	bold       = lipgloss.NewStyle().Bold(true)
	activeText = bold.Underline(true)
	thinking   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b"))
	needsYou   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Bold(true)
	alert      = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444"))
	idle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#10b981"))
	muted      = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ca3af"))
	headerBar  = lipgloss.NewStyle().Background(lipgloss.Color("#1f2937")).Foreground(lipgloss.Color("#e5e7eb"))
	cardStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#374151")).Padding(0, 1)
	cardActive = cardStyle.Border(lipgloss.DoubleBorder()).BorderForeground(lipgloss.Color("#f9fafb"))
	panelStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#374151"))
	modalStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 2)
	spinner    = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
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
	case modeImport:
		body = m.place(bodyHeight, m.renderImport(bodyHeight))
	case modeDetail:
		body = panelStyle.Width(m.width - 2).Height(bodyHeight - 2).Render(m.detail.View())
	default:
		l := m.layout()
		body = m.renderMain(l)
		if m.drag.kind == dragCard {
			if t := m.task(m.drag.task); t != nil {
				w := min(36, m.width)
				ghost := m.renderCard(t, w, true, false)
				body = overlay(body, ghost, clamp(m.drag.x-w/2, 0, m.width-w), clamp(m.drag.y-l.bodyY-1, 0, max(0, l.bodyH-1)))
			}
		}
		switch m.mode {
		case modeMove:
			body = m.place(bodyHeight, m.renderMovePicker())
		case modePalette:
			body = m.place(bodyHeight, m.renderPalette(bodyHeight))
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
	var working, waiting, attention, queued int
	cost := 0.0
	for _, t := range m.snap.Tasks {
		for _, s := range t.Sessions {
			cost += s.Usage.Cost
			if s.Status == store.StatusQueued {
				queued++
			}
			if s.Status != store.StatusRunning && s.Status != store.StatusExternal {
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
	left := bold.Render(" ▦ drover") + "  " + m.eng.ProjectName
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
	if queued > 0 {
		stats = append(stats, muted.Render(fmt.Sprintf("◷ %d queued · %d/%d slots", queued, m.snap.Running, m.snap.MaxConcurrent)))
	}
	if m.filter != "" {
		stats = append(stats, bold.Render("filter: "+m.filter)+dim.Render(" (esc clears)"))
	}
	if cost >= 0.005 {
		stats = append(stats, dim.Render(fmt.Sprintf("$%.2f", cost)))
	}
	right := strings.Join(stats, "  ") + " "
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	// layout assumes a one-row header.
	return headerBar.Width(m.width).Render(ansi.Truncate(left+strings.Repeat(" ", gap)+right, m.width, "…"))
}

func (m Model) renderFooter() string {
	if m.mode == modeSend {
		return " " + bold.Render("Send ›") + " " + m.sendInput.View()
	}
	if m.mode == modeFilter {
		return " " + bold.Render("Filter ›") + " " + m.filterInput.View()
	}
	if m.flash != "" {
		return " " + muted.Render(ansi.Truncate(m.flash, m.width-2, "…"))
	}
	hints := map[mode]string{
		modeBoard:     "←→↑↓ navigate  ctrl+k commands  H/L move  n new  / filter  A archive  enter attach  tab type into agent  s send  r start  S suspend  Q suspend all  +/- </> resize  z density  : assistant  ? help  q quit",
		modePalette:   "↑↓ select  enter run  esc cancel",
		modeLive:      "LIVE: ctrl+q back to board",
		modeAssistant: "ASSISTANT: ctrl+q back to board",
		modeForm:      "tab switch field  ctrl+s save  esc cancel",
		modeMove:      "1-9 pick column  any other key cancels",
		modeConfirm:   "y confirm  any other key cancels",
		modeDetail:    "↑↓ scroll  esc close",
		modeHelp:      "any key closes",
	}
	return " " + dim.Render(ansi.Truncate(hints[m.mode], m.width-2, "…"))
}

func (m Model) renderMain(l layout) string {
	main := m.renderBoard(l)
	if l.previewH > 0 {
		main = lipgloss.JoinVertical(lipgloss.Left, main, m.renderPreview(l.previewH))
	}
	if l.assistantW == 0 {
		return main
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, main, m.renderAssistant(l.assistantW, l.bodyH))
}

func (m Model) renderBoard(l layout) string {
	drop := m.dropTarget(l)
	var cols []string
	for _, c := range l.cols {
		cols = append(cols, m.renderColumn(c, l.boardH, c.index == drop))
	}
	board := lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	if l.hiddenColumns {
		board = lipgloss.PlaceHorizontal(l.boardW, lipgloss.Left, board)
	}
	return board
}

func columnSummary(c config.Column) string {
	switch {
	case c.IsTodo():
		return "sessions · main checkout"
	case c.IsDone():
		return "finished · archive removes worktree"
	case !c.AutoSpawn:
		return "agent paused"
	}
	parts := []string{}
	for _, p := range []string{c.PermissionMode, c.ModelOverride, c.EffortOverride} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if n := len(c.Automations.OnEnter) + len(c.Automations.OnTurnEnd); n > 0 {
		parts = append(parts, fmt.Sprintf("⚡%d", n))
	}
	if len(parts) == 0 {
		return "agent"
	}
	return strings.Join(parts, " · ")
}

func (m Model) renderColumn(box colBox, height int, dropping bool) string {
	i, width := box.index, box.width
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
	if hidden := m.archivedIn(col.ID); hidden > 0 {
		count += fmt.Sprintf(" +%d", hidden)
	}
	name := ansi.Truncate(fmt.Sprintf("%d %s", i+1, col.Name), inner-1-len(count), "…")
	note := ""
	if m.prefs.ShowSettings {
		note = columnSummary(col)
	}
	summary := dim.Render(ansi.Truncate(note, inner-1, "…"))
	if dropping {
		nameStyle = nameStyle.Reverse(true)
		summary = lipgloss.NewStyle().Foreground(accent).Bold(true).Render(ansi.Truncate("⤓ drop to move here", inner-1, "…"))
	}
	lines := []string{
		" " + nameStyle.Render(name) + dim.Render(count),
		" " + summary,
	}

	sel := -1
	if selected {
		sel = m.selectedIndex()
	}
	for j := box.first; j < len(tasks) && j < box.first+box.slots; j++ {
		dragged := m.drag.kind == dragCard && tasks[j].ID == m.drag.task
		lines = append(lines, m.renderCard(tasks[j], inner, selected && j == sel, dragged))
	}
	if hidden := len(tasks) - (box.first + box.slots); hidden > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("  +%d more", hidden)))
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Padding(0, 1, 0, 0).Render(strings.Join(lines, "\n"))
}

// sessionFor returns the session a card shows: its own, or for a card split
// off a session that opened several PRs, the one its group shares.
func (m Model) sessionFor(t *store.Task) *store.Session {
	return store.Owner(m.snap.Tasks, t).AgentSession()
}

func (m Model) status(t *store.Task) string {
	// The engine's step is more specific than the board's own label.
	what := m.snap.Busy[t.ID]
	if what == "" {
		what = m.busy[t.ID].label
	}
	if what != "" {
		return thinking.Render(spinner[m.frame%len(spinner)] + " " + what + "…")
	}
	s := m.sessionFor(t)
	if s == nil {
		return ""
	}
	// A group card the agent isn't working for doesn't show the agent's
	// activity unless it sits in an agent column itself.
	driver := store.Driver(m.snap.Tasks, store.Owner(m.snap.Tasks, t))
	if col, _, _ := m.snap.Board.Column(t.ColumnID); driver != t && !col.AutoSpawn {
		return ""
	}
	switch s.Status {
	case store.StatusQueued:
		return muted.Render(fmt.Sprintf("◷ queued (%d/%d slots busy)", m.snap.Running, m.snap.MaxConcurrent))
	case store.StatusSuspended:
		return dim.Render("⏸ suspended")
	case store.StatusExited:
		return dim.Render("■ exited")
	case store.StatusExternal:
		if s.Activity == store.ActivityThinking {
			return thinking.Render(spinner[m.frame%len(spinner)] + " working elsewhere")
		}
		return idle.Render("↗ idle elsewhere")
	}
	pending := ""
	if len(s.Pending) > 0 {
		pending = dim.Render(fmt.Sprintf(" +%d queued", len(s.Pending)))
	}
	if col, _, ok := m.snap.Board.Column(driver.ColumnID); ok && s.RespawnPending {
		pending += dim.Render(" → " + cmp.Or(m.snap.Board.PermissionMode(col), "default") + " next turn")
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

// renderCard draws a card exactly Density.cardHeight() rows tall. A dragged
// card is drawn faint where it came from.
func (m Model) renderCard(t *store.Task, width int, active, dragged bool) string {
	content := width - 4
	title := fmt.Sprintf("#%d %s", t.Number, taskTitle(t))
	if t.Issue != nil {
		title = fmt.Sprintf("#%d %s %s", t.Number, t.Issue.Key, taskTitle(t))
	}
	g := m.groupOf(t)
	sibling := m.isSibling(g, t)
	style := cardStyle
	switch {
	case active:
		style = cardActive
	case sibling:
		style = style.Border(lipgloss.ThickBorder()).BorderForeground(g.color)
	case g != nil:
		style = style.BorderForeground(g.color)
	}
	if dragged {
		style = style.Faint(true).BorderForeground(lipgloss.Color("#1f2937"))
	} else if t.Archived {
		style = style.Faint(true)
	}
	group := groupMarker(g, t, sibling)
	if m.prefs.Density == densityCompact {
		status := strings.TrimSpace(group + " " + m.status(t))
		line := ansi.Truncate(title, max(1, content-lipgloss.Width(status)-1), "…")
		if active {
			line = activeText.Render(line)
		}
		if status != "" {
			line += strings.Repeat(" ", max(1, content-lipgloss.Width(line)-lipgloss.Width(status))) + status
		}
		return style.Width(width - 2).Render(ansi.Truncate(line, content, "…"))
	}
	wrapped := strings.Split(ansi.Wordwrap(title, content, " -"), "\n")
	line1 := ansi.Truncate(wrapped[0], content, "…")
	line2 := ""
	if len(wrapped) > 1 {
		line2 = ansi.Truncate(strings.Join(wrapped[1:], " "), content, "…")
	}
	if active {
		line1, line2 = activeText.Render(line1), activeText.Render(line2)
	}
	var meta []string
	for _, part := range []string{group, m.status(t), m.cardRef(t)} {
		if part != "" {
			meta = append(meta, part)
		}
	}
	metaLine := ansi.Truncate(strings.Join(meta, " "), content, "…")
	if u := usageLabel(m.sessionFor(t)); u != "" && lipgloss.Width(metaLine)+lipgloss.Width(u)+1 <= content {
		metaLine += strings.Repeat(" ", content-lipgloss.Width(metaLine)-lipgloss.Width(u)) + dim.Render(u)
	}
	lines := []string{line1, line2, metaLine}
	if m.prefs.Density == densityTall {
		lines = append(lines, replyLines(m.sessionFor(t), content, 3)...)
	}
	// Width excludes the border, which adds one cell on each side.
	return style.Width(width - 2).Render(strings.Join(lines, "\n"))
}

// usageLabel is how full the session's context is and what it has cost, like
// "42% · $1.20".
func usageLabel(s *store.Session) string {
	if s == nil {
		return ""
	}
	var parts []string
	if window := claude.ContextWindow(s.Usage.Model); window > 0 && s.Usage.Context > 0 {
		parts = append(parts, fmt.Sprintf("%d%%", s.Usage.Context*100/window))
	}
	if s.Usage.Cost >= 0.005 {
		parts = append(parts, fmt.Sprintf("$%.2f", s.Usage.Cost))
	}
	return strings.Join(parts, " · ")
}

var groupColors = []lipgloss.Color{"#22d3ee", "#f472b6", "#a3e635", "#fb923c", "#c084fc", "#facc15"}

// cardGroup is the set of cards that share one agent session.
type cardGroup struct {
	members []*store.Task
	color   lipgloss.Color
}

// groupOf returns the group t belongs to, or nil for a card of its own.
func (m Model) groupOf(t *store.Task) *cardGroup {
	owner := store.Owner(m.snap.Tasks, t)
	members := store.Members(m.snap.Tasks, owner)
	if len(members) < 2 {
		return nil
	}
	var before []string
	for _, task := range m.snap.Tasks {
		if task.GroupID == owner.ID {
			break
		}
		if task.GroupID != "" && !slices.Contains(before, task.GroupID) {
			before = append(before, task.GroupID)
		}
	}
	return &cardGroup{members: members, color: groupColors[len(before)%len(groupColors)]}
}

// groupMarker shows "⧉ 2/3" on cards that share one agent session, in the
// group's color; bold on the siblings of the selected card.
func groupMarker(g *cardGroup, t *store.Task, sibling bool) string {
	if g == nil {
		return ""
	}
	pos := slices.Index(g.members, t) + 1
	return lipgloss.NewStyle().Foreground(g.color).Bold(sibling).Render(fmt.Sprintf("⧉ %d/%d", pos, len(g.members)))
}

// isSibling reports whether t shares a session with the selected card.
func (m Model) isSibling(g *cardGroup, t *store.Task) bool {
	if g == nil {
		return false
	}
	cur := m.current()
	return cur != nil && cur != t && slices.Contains(g.members, cur)
}

// cardRef is the card's PR, or the branch its agent works on.
func (m Model) cardRef(t *store.Task) string {
	if t.PR != nil {
		ref := fmt.Sprintf("PR #%d", t.PR.Number)
		if t.PR.URL != "" {
			ref = ansi.SetHyperlink(t.PR.URL) + ref + ansi.ResetHyperlink()
		}
		if t.PR.IsReview() {
			ref = "review " + ref
			if t.PR.Author != "" {
				ref += " @" + t.PR.Author
			}
		}
		switch {
		case t.PR.Released:
			ref += " released"
		case t.PR.State != "" && t.PR.State != "OPEN":
			ref += " " + strings.ToLower(t.PR.State)
		}
		if t.PR.Branch != "" {
			ref += " " + t.PR.Branch
		}
		badge := ""
		switch {
		case t.PR.Error != "":
			badge = alert.Render("⚠ gh") + " "
		case t.PR.State != "OPEN":
		case t.PR.Review:
			badge = reviewBadge(t.PR)
		case t.PR.ReviewDecision == "APPROVED":
			badge = idle.Render("✓ approved") + " "
		case t.PR.ReviewDecision == "CHANGES_REQUESTED":
			badge = alert.Render("✗ changes") + " "
		}
		return badge + dim.Render(ref)
	}
	if branch := store.Owner(m.snap.Tasks, t).Branch; branch != "" {
		return dim.Render(branch)
	}
	return ""
}

// reviewBadge shows where your review of a PR stands.
func reviewBadge(pr *store.PR) string {
	switch {
	case pr.ReviewedSHA != "" && pr.HeadSHA != pr.ReviewedSHA:
		return alert.Render("↻ new commits") + " "
	case pr.MyReview == "APPROVED":
		return idle.Render("✓ you approved") + " "
	case pr.MyReview == "CHANGES_REQUESTED":
		return alert.Render("✗ you requested changes") + " "
	case pr.MyReview != "":
		return idle.Render("✓ you reviewed") + " "
	}
	return ""
}

// replyLines is the start of the agent's last reply, padded to n lines.
func replyLines(s *store.Session, width, n int) []string {
	var lines []string
	if s != nil && s.LastReply != "" {
		flat := strings.Join(strings.Fields(ansi.Strip(s.LastReply)), " ")
		lines = strings.Split(ansi.Wordwrap(flat, width, " -"), "\n")
	}
	out := make([]string, n)
	for i := range out {
		if i < len(lines) {
			out[i] = muted.Render(ansi.Truncate(lines[i], width, "…"))
		}
	}
	return out
}

func (m Model) renderPreview(height int) string {
	t := m.current()
	title := " no task selected "
	body := ""
	if t != nil {
		title = fmt.Sprintf(" #%d %s ", t.Number, taskTitle(t))
		if s := m.sessionFor(t); s != nil && s.Status == store.StatusExternal {
			body = dim.Render(fmt.Sprintf("Running in another terminal (pid %d); press enter to open it or take it over. Its latest reply:", s.PID)) + "\n\n" + s.LastReply
		} else if _, ok := m.liveSession(t.ID); !ok {
			body = dim.Render("No running agent. Move the card into an agent column, or press r to resume.")
			if s := m.sessionFor(t); s != nil && s.LastReply != "" {
				body += "\n\n" + muted.Render("Last reply:") + "\n" + s.LastReply
			}
		} else if m.previewFor == t.ID {
			body = previewWindow(m.previewTxt, height-2, m.previewScroll)
			if m.previewScroll > 0 {
				title += thinking.Render(fmt.Sprintf("· ↑ %d lines up, scroll down to return ", m.previewScroll))
			} else if m.mode != modeLive {
				title += dim.Render("· tab to type here, enter for full screen ")
			}
		}
	}
	style := panelStyle
	if m.mode == modeLive {
		style = style.BorderForeground(accent)
		title = lipgloss.NewStyle().Foreground(accent).Bold(true).Render(" ● LIVE") + title + dim.Render("· ctrl+q back to board ")
	} else if m.drag.kind == dragPreview {
		style = style.BorderForeground(accent)
	}
	return titledBox(style, title, body, m.boardWidth(), height)
}

// titledBox draws body in a bordered box of width x height.
func titledBox(style lipgloss.Style, title, body string, width, height int) string {
	inner, rows := width-2, max(0, height-2)
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, inner, "")
	}
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	box := style.Width(inner).Height(rows).Render(strings.Join(lines, "\n"))
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

// previewWindow shows n lines ending scroll lines above the bottom.
func previewWindow(s string, n, scroll int) string {
	if scroll == 0 {
		return lastLines(s, n)
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	end := clamp(len(lines)-scroll, min(n, len(lines)), len(lines))
	return strings.Join(lines[max(0, end-n):end], "\n")
}

func (m Model) renderForm() string {
	return modalStyle.Width(min(104, m.width-4)).Render(strings.Join([]string{
		bold.Render("Edit task"),
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
		if c.ID == m.columnOf(t) {
			marker = "▸ "
		}
		lines = append(lines, fmt.Sprintf("%s%s  %s  %s", marker, bold.Render(fmt.Sprint(i+1)), c.Name, dim.Render(columnSummary(c))))
	}
	return modalStyle.Render(strings.Join(lines, "\n"))
}

func (m Model) renderHelp() string {
	rows := [][2]string{
		{"ctrl+k", "search and run a command"},
		{"←→ / h l", "select column"},
		{"↑↓ / j k, g G", "select card"},
		{"H / L, shift+←→", "move card to previous / next column"},
		{"m 1-9", "move card to column"},
		{"n", "new Claude session (no branch until moved right)"},
		{"e", "edit title and prompt"},
		{"x / D", "delete task (branch is kept)"},
		{"enter / a", "attach to the agent's terminal, starting it if needed (Ctrl-q returns)"},
		{"tab", "type into the agent from the preview (Ctrl-q returns)"},
		{"s", "send a message to the agent"},
		{"r", "start or resume the agent in place"},
		{"S", "suspend the agent (resume later with r)"},
		{"c", "open a shell in the card's worktree (Ctrl-q returns)"},
		{":", "open and type into the assistant panel (Ctrl-q returns, again hides it)"},
		{"N", "start a new assistant conversation"},
		{"/", "filter cards by number, title, branch, issue or PR (esc clears)"},
		{"u", "update the worktree from its base branch"},
		{"p", "toggle live preview"},
		{"+ / -", "taller / shorter preview"},
		{"< / >", "narrower / wider column"},
		{"z", "card size: normal, compact, tall with last reply"},
		{"0", "reset layout"},
		{"M", "toggle mouse (off lets the terminal select text)"},
		{"i", "task details and automation log"},
		{"I", "import running Claude Code agents, past sessions or Linear issues"},
		{"A", "archive or restore a card in Done"},
		{"C", "archive every card in Done"},
		{"v", "show or hide archived cards"},
		{"t", "show or hide column settings under column names"},
		{"W", "write the default board to drover.json"},
		{"q", "quit (agents keep running in tmux)"},
		{"Q", "suspend every agent and quit, printing claude --resume commands"},
	}
	section := func(title string, rows [][2]string) string {
		lines := []string{bold.Render(title), ""}
		for _, r := range rows {
			lines = append(lines, fmt.Sprintf("%-18s %s", r[0], muted.Render(r[1])))
		}
		return strings.Join(lines, "\n")
	}
	right := section("Mouse", [][2]string{
		{"drag a card", "move it to another column"},
		{"double-click", "attach"},
		{"drag column edge", "resize the column"},
		{"drag preview top", "resize the preview"},
		{"click preview", "type into the agent"},
		{"click assistant", "type into the assistant"},
	}) + "\n\n" + section("Live mode", [][2]string{
		{"ctrl+q", "back to board"},
		{"mouse wheel", "scroll the agent's history"},
		{"typing", "jumps back to the bottom"},
		{"anything else", "goes to the agent"},
	})
	body := lipgloss.JoinHorizontal(lipgloss.Top, section("Keys", rows), "    ", right)
	if lipgloss.Width(body)+8 > m.width {
		body = section("Keys", rows) + "\n\n" + right
	}
	footer := dim.Render("Moving a card into an agent column creates its worktree and starts Claude\n" +
		"with that column's permission mode, model and effort, then runs its automations.")
	return modalStyle.Render(body + "\n\n" + footer)
}

// taskTitle stands in for a session that has not been sent a message yet.
func taskTitle(t *store.Task) string {
	if t.Title == "" {
		return "New session"
	}
	return t.Title
}

func (m Model) renderDetail(t *store.Task) string {
	col, _, _ := m.snap.Board.Column(m.columnOf(t))
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", bold.Render(fmt.Sprintf("#%d %s", t.Number, taskTitle(t))))
	field := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s %s\n", muted.Render(fmt.Sprintf("%-10s", k)), v)
		}
	}
	field("Column", col.Name)
	if t.Issue != nil {
		field("Issue", fmt.Sprintf("%s  %s  %s", t.Issue.Key, t.Issue.State, t.Issue.URL))
	}
	if t.PR != nil {
		field("PR", strings.TrimSpace(fmt.Sprintf("#%d  %s  %s  %s", t.PR.Number, t.PR.State, t.PR.Branch, t.PR.URL)))
		if t.PR.Review && t.PR.Author != "" {
			field("Reviewing", "@"+t.PR.Author)
		}
	}
	owner := store.Owner(m.snap.Tasks, t)
	field("Branch", owner.Branch)
	field("Base", owner.BaseBranch)
	field("Worktree", owner.WorktreePath)
	field("Created", t.CreatedAt.Format(time.DateTime))
	if t.Prompt != "" {
		fmt.Fprintf(&b, "\n%s\n%s\n", muted.Render("Prompt"), t.Prompt)
	}
	if members := store.Members(m.snap.Tasks, owner); len(members) > 1 {
		fmt.Fprintf(&b, "\n%s\n", muted.Render("Shares its agent with"))
		for _, other := range members {
			if other != t {
				c, _, _ := m.snap.Board.Column(other.ColumnID)
				fmt.Fprintf(&b, "  #%d %s  %s\n", other.Number, other.Title, dim.Render(c.Name))
			}
		}
	}
	if len(owner.Sessions) > 0 {
		fmt.Fprintf(&b, "\n%s\n", muted.Render("Sessions"))
		for key, s := range owner.Sessions {
			fmt.Fprintf(&b, "  %s  %s/%s  %s  tmux:%s\n", key, s.Status, s.Activity, dim.Render(s.AgentSessionID), s.Tmux)
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
