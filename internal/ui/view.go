package ui

import (
	"fmt"
	"hash/crc32"
	"slices"
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
	// layout assumes a one-row header.
	return headerBar.Width(m.width).Render(ansi.Truncate(left+strings.Repeat(" ", gap)+right, m.width, "…"))
}

func (m Model) renderFooter() string {
	if m.mode == modeSend {
		return " " + bold.Render("Send ›") + " " + m.sendInput.View()
	}
	if m.flash != "" {
		return " " + muted.Render(ansi.Truncate(m.flash, m.width-2, "…"))
	}
	hints := map[mode]string{
		modeBoard:   "←→↑↓ navigate  H/L move  n new  enter attach  tab type into agent  s send  r start  S suspend  +/- </> resize  z density  ? help  q quit",
		modeLive:    "LIVE: every key goes to the agent  ctrl+q back to board  mouse wheel scrolls",
		modeForm:    "tab switch field  ctrl+s save  esc cancel",
		modeMove:    "1-9 pick column  any other key cancels",
		modeConfirm: "y confirm  any other key cancels",
		modeDetail:  "↑↓ scroll  esc close",
		modeHelp:    "any key closes",
	}
	return " " + dim.Render(ansi.Truncate(hints[m.mode], m.width-2, "…"))
}

func (m Model) renderMain(l layout) string {
	board := m.renderBoard(l)
	if l.previewH == 0 {
		return board
	}
	return lipgloss.JoinVertical(lipgloss.Left, board, m.renderPreview(l.previewH))
}

func (m Model) renderBoard(l layout) string {
	drop := m.dropTarget(l)
	var cols []string
	for _, c := range l.cols {
		cols = append(cols, m.renderColumn(c, l.boardH, c.index == drop))
	}
	board := lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	if l.hiddenColumns {
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
	name := ansi.Truncate(fmt.Sprintf("%d %s", i+1, col.Name), inner-1-len(count), "…")
	summary := dim.Render(ansi.Truncate(columnSummary(col), inner-1, "…"))
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
	owner := store.Owner(m.snap.Tasks, t)
	col, _, _ := m.snap.Board.Column(store.Driver(m.snap.Tasks, owner).ColumnID)
	key := store.MainSession
	if col.Isolated() {
		key = col.ID
	}
	if s := owner.Sessions[key]; s != nil {
		return s
	}
	for _, s := range owner.Sessions {
		if s.Status == store.StatusRunning {
			return s
		}
	}
	return nil
}

func (m Model) status(t *store.Task) string {
	if what := m.snap.Busy[t.ID]; what != "" {
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
		return muted.Render("◷ queued")
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
		pending += dim.Render(" → " + col.PermissionMode + " next turn")
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
	title := fmt.Sprintf("#%d %s", t.Number, t.Title)
	if t.Issue != nil {
		title = fmt.Sprintf("#%d %s %s", t.Number, t.Issue.Key, t.Title)
	}
	style := cardStyle
	if active {
		style = cardActive
	}
	if dragged {
		style = style.Faint(true).BorderForeground(lipgloss.Color("#1f2937"))
	}
	group := m.groupMarker(t)
	if m.prefs.Density == densityCompact {
		status := strings.TrimSpace(group + " " + m.status(t))
		line := ansi.Truncate(title, max(1, content-lipgloss.Width(status)-1), "…")
		if active {
			line = bold.Render(line)
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
		line1, line2 = bold.Render(line1), bold.Render(line2)
	}
	var meta []string
	for _, part := range []string{group, m.status(t), m.cardRef(t)} {
		if part != "" {
			meta = append(meta, part)
		}
	}
	lines := []string{line1, line2, ansi.Truncate(strings.Join(meta, " "), content, "…")}
	if m.prefs.Density == densityTall {
		lines = append(lines, replyLines(m.sessionFor(t), content, 3)...)
	}
	// Width excludes the border, which adds one cell on each side.
	return style.Width(width - 2).Render(strings.Join(lines, "\n"))
}

var groupColors = []lipgloss.Color{"#22d3ee", "#f472b6", "#a3e635", "#fb923c", "#c084fc", "#facc15"}

// groupMarker shows "⧉ 2/3" on cards that share one agent session, colored
// per group so the cards of a group are easy to spot across columns.
func (m Model) groupMarker(t *store.Task) string {
	owner := store.Owner(m.snap.Tasks, t)
	members := store.Members(m.snap.Tasks, owner)
	if len(members) < 2 {
		return ""
	}
	pos := slices.Index(members, t) + 1
	color := groupColors[int(crc32.ChecksumIEEE([]byte(owner.ID)))%len(groupColors)]
	return lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("⧉ %d/%d", pos, len(members)))
}

// cardRef is the card's PR, or the branch its agent works on.
func (m Model) cardRef(t *store.Task) string {
	if t.PR != nil {
		ref := fmt.Sprintf("PR #%d", t.PR.Number)
		if t.PR.State != "" && t.PR.State != "OPEN" {
			ref += " " + strings.ToLower(t.PR.State)
		}
		if t.PR.Branch != "" {
			ref += " " + t.PR.Branch
		}
		return dim.Render(ref)
	}
	if branch := store.Owner(m.snap.Tasks, t).Branch; branch != "" {
		return dim.Render(branch)
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
		title = fmt.Sprintf(" #%d %s ", t.Number, t.Title)
		if s := m.sessionFor(t); s != nil && s.Status == store.StatusExternal {
			body = dim.Render(fmt.Sprintf("Running in another terminal (pid %d). Its latest reply:", s.PID)) + "\n\n" + s.LastReply
		} else if _, ok := m.eng.LiveSession(t.ID); !ok {
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
	inner := m.width - 2
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, inner, "")
	}
	if len(lines) > height-2 {
		lines = lines[len(lines)-(height-2):]
	}
	box := style.Width(inner).Height(height - 2).Render(strings.Join(lines, "\n"))
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
		{"n", "new task (starts an agent)"},
		{"e", "edit title and prompt"},
		{"x / D", "delete task (branch is kept)"},
		{"enter / a", "attach to the agent's terminal (Ctrl-q returns)"},
		{"tab", "type into the agent from the preview (Ctrl-q returns)"},
		{"s", "send a message to the agent"},
		{"r", "start or resume the agent in place"},
		{"S", "suspend the agent (resume later with r)"},
		{"p", "toggle live preview"},
		{"+ / -", "taller / shorter preview"},
		{"< / >", "narrower / wider column"},
		{"z", "card size: normal, compact, tall with last reply"},
		{"0", "reset layout"},
		{"M", "toggle mouse (off lets the terminal select text)"},
		{"i", "task details and automation log"},
		{"I", "import running Claude Code agents, past sessions or Linear issues"},
		{"W", "write the default board to kangentic.json"},
		{"q", "quit (agents keep running in tmux)"},
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
	if t.Issue != nil {
		field("Issue", fmt.Sprintf("%s  %s  %s", t.Issue.Key, t.Issue.State, t.Issue.URL))
	}
	if t.PR != nil {
		field("PR", strings.TrimSpace(fmt.Sprintf("#%d  %s  %s  %s", t.PR.Number, t.PR.State, t.PR.Branch, t.PR.URL)))
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
