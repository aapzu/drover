package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/engine"
)

var activeTab = lipgloss.NewStyle().Bold(true).Background(accent).Foreground(lipgloss.Color("#ffffff")).Padding(0, 1)

const (
	sourceClaude = iota
	sourceIssues
	sourceReviews
	sources
)

const importPageSize = 40

// importItem is a session, an issue, a PR to review, or a header over the
// sessions started in one directory or the PRs of one source.
type importItem struct {
	session  *claude.Found
	issue    *engine.Issue
	review   *engine.ReviewPR
	selected bool
	header   string
}

func (it importItem) isHeader() bool { return it.header != "" }

type importState struct {
	// gen discards results from a load that was superseded.
	gen      int
	source   int
	items    []importItem
	cursor   int
	loading  bool
	err      string
	keyEntry bool
	keyInput textinput.Model
	limit    int
	more     bool
}

type sessionsLoadedMsg struct {
	gen      int
	sessions []claude.Found
	more     bool
}
type issuesLoadedMsg struct {
	gen    int
	issues []engine.Issue
	err    error
	single bool
}
type reviewsLoadedMsg struct {
	gen     int
	reviews []engine.ReviewPR
	err     error
	single  bool
}

func newImportState() importState {
	return importState{keyInput: textinput.New(), limit: importPageSize}
}

// issueSetup says what the tracker needs before its issues can be listed.
func issueSetup(tracker string) string {
	if tracker == config.TrackerGitHub {
		return "Install the GitHub CLI and sign in with `gh auth login`."
	}
	return "Set LINEAR_API_KEY (linear.app → Settings → Security & access → Personal API keys) and restart."
}

func issuePlaceholder(tracker string) string {
	if tracker == config.TrackerGitHub {
		return "123 or a github.com issue URL"
	}
	return "DX-123 or a linear.app issue URL"
}

func (m *Model) openImport(source int) tea.Cmd {
	m.mode = modeImport
	m.imp.source = source
	m.imp.items, m.imp.cursor, m.imp.err, m.imp.keyEntry = nil, 0, "", false
	m.imp.limit, m.imp.more = importPageSize, false
	if source == sourceClaude {
		return m.loadSessions()
	}
	m.imp.loading = true
	m.imp.gen++
	gen, eng := m.imp.gen, m.eng
	if source == sourceReviews {
		return func() tea.Msg {
			reviews, err := eng.ReviewPRs()
			return reviewsLoadedMsg{gen: gen, reviews: reviews, err: err}
		}
	}
	if !eng.TrackerReady() {
		m.imp.loading = false
		m.imp.err = issueSetup(eng.Tracker())
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		issues, err := eng.IssueList(ctx)
		return issuesLoadedMsg{gen: gen, issues: issues, err: err}
	}
}

func (m *Model) loadSessions() tea.Cmd {
	m.imp.loading = true
	m.imp.gen++
	gen, eng, limit := m.imp.gen, m.eng, m.imp.limit
	return func() tea.Msg {
		sessions, more := eng.DiscoverSessions(limit)
		return sessionsLoadedMsg{gen: gen, sessions: sessions, more: more}
	}
}

// groupSessions lists sessions under a header per directory, in the order each
// directory first appears.
func groupSessions(sessions []claude.Found, root string, prev map[string]bool) []importItem {
	var dirs []string
	byDir := map[string][]int{}
	for i, s := range sessions {
		if _, ok := byDir[s.Cwd]; !ok {
			dirs = append(dirs, s.Cwd)
		}
		byDir[s.Cwd] = append(byDir[s.Cwd], i)
	}
	var items []importItem
	for _, dir := range dirs {
		where, _ := strings.CutPrefix(dir, root)
		if where = strings.TrimPrefix(where, "/"); where == "" {
			where = "repo root"
		}
		items = append(items, importItem{header: where})
		for _, i := range byDir[dir] {
			s := &sessions[i]
			selected, seen := prev[s.SessionID]
			items = append(items, importItem{session: s, selected: selected || (!seen && s.Running)})
		}
	}
	return items
}

const (
	reviewsRequested = "Requested from me"
	reviewsReviewed  = "Reviewed by me, still open"
)

// groupReviews lists the PRs awaiting your review, then the ones you have
// reviewed, each under a header. URLs in selected start selected.
func groupReviews(reviews []engine.ReviewPR, selected map[string]bool) []importItem {
	var items []importItem
	for _, group := range []struct {
		header   string
		reviewed bool
	}{{reviewsRequested, false}, {reviewsReviewed, true}} {
		start := len(items)
		for i := range reviews {
			if r := &reviews[i]; r.Reviewed == group.reviewed {
				items = append(items, importItem{review: r, selected: selected[r.URL]})
			}
		}
		if len(items) > start {
			items = slices.Insert(items, start, importItem{header: group.header})
		}
	}
	return items
}

// group returns the items under the header at index h.
func (s *importState) group(h int) []importItem {
	end := h + 1
	for end < len(s.items) && !s.items[end].isHeader() {
		end++
	}
	return s.items[h+1 : end]
}

// toggleGroup selects every session under the header at index h, or none if
// all are selected.
func (s *importState) toggleGroup(h int) {
	group := s.group(h)
	all := !slices.ContainsFunc(group, func(it importItem) bool { return !it.selected })
	for i := range group {
		group[i].selected = !all
	}
}

func (m Model) updateImportLoaded(msg tea.Msg) Model {
	switch msg := msg.(type) {
	case sessionsLoadedMsg:
		if msg.gen != m.imp.gen || m.mode != modeImport {
			return m
		}
		m.imp.loading = false
		prev, cursorID := map[string]bool{}, ""
		for i, it := range m.imp.items {
			if it.session != nil {
				prev[it.session.SessionID] = it.selected
				if i == m.imp.cursor {
					cursorID = it.session.SessionID
				}
			}
		}
		m.imp.more = msg.more
		m.imp.items, m.imp.cursor = groupSessions(msg.sessions, m.eng.Root, prev), 0
		for i, it := range m.imp.items {
			if it.session != nil && it.session.SessionID == cursorID {
				m.imp.cursor = i
			}
		}
	case reviewsLoadedMsg:
		if msg.gen != m.imp.gen || m.mode != modeImport {
			return m
		}
		m.imp.loading = false
		if msg.err != nil {
			m.imp.err = msg.err.Error()
			return m
		}
		m.imp.err = ""
		var reviews []engine.ReviewPR
		selected := map[string]bool{}
		if msg.single {
			for _, it := range m.imp.items {
				if it.review != nil && it.review.URL != msg.reviews[0].URL {
					reviews = append(reviews, *it.review)
					selected[it.review.URL] = it.selected
				}
			}
			selected[msg.reviews[0].URL] = true
		}
		reviews = append(reviews, msg.reviews...)
		m.imp.items, m.imp.cursor = groupReviews(reviews, selected), 0
		for i, it := range m.imp.items {
			if msg.single && it.review != nil && it.review.URL == msg.reviews[0].URL {
				m.imp.cursor = i
			}
		}
	case issuesLoadedMsg:
		if msg.gen != m.imp.gen || m.mode != modeImport {
			return m
		}
		if !msg.single {
			m.imp.items, m.imp.cursor = nil, 0
		}
		m.imp.loading = false
		if msg.err != nil {
			m.imp.err = msg.err.Error()
			return m
		}
		m.imp.err = ""
		for i := range msg.issues {
			m.imp.items = append(m.imp.items, importItem{issue: &msg.issues[i], selected: msg.single})
		}
		if msg.single {
			m.imp.cursor = len(m.imp.items) - 1
		}
	}
	return m
}

func (m Model) updateImport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	imp := &m.imp
	if imp.keyEntry {
		switch msg.String() {
		case "esc":
			imp.keyEntry = false
			return m, nil
		case "enter":
			imp.keyEntry = false
			ref := imp.keyInput.Value()
			imp.gen++
			gen, eng := imp.gen, m.eng
			imp.loading = true
			if imp.source == sourceReviews {
				return m, func() tea.Msg {
					pr, err := eng.ReviewPRByURL(ref)
					if err != nil {
						return reviewsLoadedMsg{gen: gen, err: err}
					}
					return reviewsLoadedMsg{gen: gen, reviews: []engine.ReviewPR{pr}, single: true}
				}
			}
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				issue, err := eng.IssueByRef(ctx, ref)
				if err != nil {
					return issuesLoadedMsg{gen: gen, err: err}
				}
				return issuesLoadedMsg{gen: gen, issues: []engine.Issue{issue}, single: true}
			}
		}
		var cmd tea.Cmd
		imp.keyInput, cmd = imp.keyInput.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "esc", "q":
		m.mode = modeBoard
	case "tab":
		cmd := m.openImport((imp.source + 1) % sources)
		return m, cmd
	case "shift+tab":
		cmd := m.openImport((imp.source + sources - 1) % sources)
		return m, cmd
	case "up", "k":
		imp.cursor = max(0, imp.cursor-1)
	case "down", "j":
		imp.cursor = max(0, min(len(imp.items)-1, imp.cursor+1))
	case " ", "x":
		if imp.cursor >= 0 && imp.cursor < len(imp.items) {
			if it := imp.items[imp.cursor]; it.isHeader() {
				imp.toggleGroup(imp.cursor)
			} else {
				imp.items[imp.cursor].selected = !it.selected
			}
		}
	case "a":
		all := true
		for _, it := range imp.items {
			all = all && (it.isHeader() || it.selected)
		}
		for i, it := range imp.items {
			if !it.isHeader() {
				imp.items[i].selected = !all
			}
		}
	case "m":
		if imp.source == sourceClaude && imp.more && !imp.loading {
			imp.limit += importPageSize
			cmd := m.loadSessions()
			return m, cmd
		}
	case "/":
		if imp.source == sourceReviews || imp.source == sourceIssues && m.eng.TrackerReady() {
			imp.keyEntry = true
			imp.keyInput.Reset()
			imp.keyInput.Placeholder = issuePlaceholder(m.eng.Tracker())
			if imp.source == sourceReviews {
				imp.keyInput.Placeholder = "a GitHub pull request URL"
			}
			cmd := imp.keyInput.Focus()
			return m, cmd
		}
	case "enter":
		var sessions []claude.Found
		var issues []engine.Issue
		var reviews []engine.ReviewPR
		for i, it := range imp.items {
			if !it.selected && i != imp.cursor {
				continue
			}
			if !it.selected && m.anySelected() {
				continue
			}
			if it.session != nil {
				sessions = append(sessions, *it.session)
			}
			if it.issue != nil {
				issues = append(issues, *it.issue)
			}
			if it.review != nil {
				reviews = append(reviews, *it.review)
			}
		}
		n := m.eng.ImportSessions(sessions) + len(m.eng.ImportIssues(issues)) + len(m.eng.ImportReviews(reviews))
		m.snap = m.eng.Snapshot()
		m.mode = modeBoard
		switch {
		case n > 0:
			m.selectTask(m.snap.Tasks[len(m.snap.Tasks)-1])
			m.setFlash(fmt.Sprintf("Imported %d card(s)", n))
		case len(sessions)+len(issues)+len(reviews) > 0:
			m.setFlash("Already on the board")
		}
	}
	return m, nil
}

func (m Model) anySelected() bool {
	for _, it := range m.imp.items {
		if it.selected {
			return true
		}
	}
	return false
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// height is the lines an item takes: a header one, anything else a label over
// a detail.
func (it importItem) height() int {
	if it.isHeader() {
		return 1
	}
	return 2
}

// itemLines renders the item at index i, with the cursor marker if it is there.
func (s importState) itemLines(i, inner int) []string {
	it := s.items[i]
	check := "[ ]"
	if it.selected {
		check = idle.Render("[x]")
	}
	pointer := "  "
	if i == s.cursor {
		pointer = "▸ "
	}
	if it.isHeader() {
		group := s.group(i)
		picked := 0
		for _, g := range group {
			if g.selected {
				picked++
			}
		}
		switch {
		case picked == len(group):
			check = idle.Render("[x]")
		case picked > 0:
			check = idle.Render("[-]")
		}
		line := pointer + check + " " + bold.Render(it.header) + dim.Render(fmt.Sprintf("  %d", len(group)))
		return []string{ansi.Truncate(line, inner, "…")}
	}
	var label, detail string
	if s := it.session; s != nil {
		state := dim.Render(ago(s.Updated))
		if s.Running {
			state = thinking.Render("● running")
			if !s.Busy {
				state = idle.Render("● running · idle")
			}
		}
		label = s.Title
		detail = state + dim.Render("  "+s.Branch)
	} else if is := it.issue; is != nil {
		label = bold.Render(is.Key) + " " + is.Title
		detail = dim.Render(strings.TrimSpace(is.State + "  " + is.Branch))
	} else if r := it.review; r != nil {
		label = bold.Render(fmt.Sprintf("#%d", r.Number)) + " " + r.Title
		detail = "@" + r.Author.Login
		if r.IsDraft {
			detail += " · draft"
		}
		detail = dim.Render(detail + "  " + r.URL)
	}
	line := pointer + check + " " + label
	if i == s.cursor {
		line = bold.Render(pointer+check+" ") + label
	}
	indent := ""
	if it.session != nil || it.review != nil {
		indent = "  "
	}
	return []string{ansi.Truncate(indent+line, inner, "…"), indent + "      " + ansi.Truncate(detail, inner-6-len(indent), "…")}
}

// window picks the items [start, end) that fit in budget lines with the cursor
// in view, keeping about half of them above it and filling any slack at the
// ends of the list.
func (s importState) window(budget int) (start, end int) {
	n := len(s.items)
	if n == 0 {
		return 0, 0
	}
	cursor := clamp(s.cursor, 0, n-1)
	budget = max(budget, s.items[cursor].height())
	start, end, used := cursor, cursor+1, s.items[cursor].height()
	for start > 0 && used+s.items[start-1].height() <= budget/2 {
		start--
		used += s.items[start].height()
	}
	for end < n && used+s.items[end].height() <= budget {
		used += s.items[end].height()
		end++
	}
	for start > 0 && used+s.items[start-1].height() <= budget {
		start--
		used += s.items[start].height()
	}
	return start, end
}

func (m Model) renderImport(height int) string {
	imp := m.imp
	width := min(120, m.width-4)
	inner := width - 6
	tab := func(name string, on bool) string {
		if on {
			return activeTab.Render(name)
		}
		return dim.Render(" " + name + " ")
	}
	head := []string{
		tab("Claude sessions", imp.source == sourceClaude) + "  " + tab(m.eng.TrackerName(), imp.source == sourceIssues) + "  " + tab("Reviews", imp.source == sourceReviews),
		"",
	}
	switch {
	case imp.loading:
		head = append(head, thinking.Render(spinner[m.frame%len(spinner)]+" loading…"))
	case imp.err != "":
		head = append(head, needsYou.Render(ansi.Wordwrap(imp.err, inner, " ")))
	case len(imp.items) == 0 && imp.source == sourceClaude:
		head = append(head, dim.Render("No running agents or `claude agents` sessions for this repository that aren't on the board yet."))
	case len(imp.items) == 0 && imp.source == sourceReviews:
		head = append(head, dim.Render("No open PRs to review that aren't on the board yet. Press / to fetch one by URL."))
	case len(imp.items) == 0:
		head = append(head, dim.Render("No open issues assigned to you. Press / to fetch one by key."))
	}

	var tail []string
	if imp.keyEntry {
		prompt := "Issue › "
		if imp.source == sourceReviews {
			prompt = "PR › "
		}
		tail = append(tail, "", prompt+imp.keyInput.View())
	}
	hint := "space select · a all · enter import · tab switch source · esc close"
	if imp.more {
		hint = "space select · a all · m load more · enter import · tab switch · esc close"
	}
	if imp.source == sourceIssues {
		fetch := "key"
		if m.eng.Tracker() == config.TrackerGitHub {
			fetch = "number"
		}
		hint = "space select · a all · / fetch by " + fetch + " · enter import · tab switch · esc close"
	}
	if imp.source == sourceReviews {
		hint = "space select · a all · / fetch by URL · enter import · tab switch · esc close"
	}
	tail = append(tail, "", dim.Render(hint))
	if imp.source == sourceClaude {
		tail = append(tail, dim.Render("Running agents keep running where they are; the card mirrors them until they exit."))
	}
	if imp.source == sourceReviews {
		tail = append(tail, dim.Render("No agent starts; enter on a card, or moving it to an agent column, reviews the PR."))
	}

	// The modal's border and padding take 4 lines.
	budget := height - 4 - lipgloss.Height(strings.Join(head, "\n")) - lipgloss.Height(strings.Join(tail, "\n"))
	start, end := imp.window(budget)
	var more string
	if start > 0 || end < len(imp.items) {
		start, end = imp.window(budget - 1)
		var parts []string
		if start > 0 {
			parts = append(parts, fmt.Sprintf("↑ %d above", start))
		}
		if end < len(imp.items) {
			parts = append(parts, fmt.Sprintf("↓ %d below", len(imp.items)-end))
		}
		more = dim.Render(strings.Join(parts, " · "))
	}

	lines := head
	for i := start; i < end; i++ {
		lines = append(lines, imp.itemLines(i, inner)...)
	}
	if more != "" {
		lines = append(lines, more)
	}
	lines = append(lines, tail...)
	return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
}
