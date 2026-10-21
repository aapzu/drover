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
	"github.com/aapzu/drover/internal/linear"
)

var activeTab = lipgloss.NewStyle().Bold(true).Background(accent).Foreground(lipgloss.Color("#ffffff")).Padding(0, 1)

const (
	sourceClaude = iota
	sourceLinear
)

const importPageSize = 40

// importItem is a session, an issue, or a header over the sessions started in
// one directory.
type importItem struct {
	session  *claude.Found
	issue    *linear.Issue
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
	issues []linear.Issue
	err    error
	single bool
}

func newImportState() importState {
	ki := textinput.New()
	ki.Placeholder = "DX-123 or a linear.app issue URL"
	return importState{keyInput: ki, limit: importPageSize}
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
	if !eng.HasLinear() {
		m.imp.loading = false
		m.imp.err = "Set LINEAR_API_KEY (linear.app → Settings → Security & access → Personal API keys) and restart."
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		issues, err := eng.LinearIssues(ctx)
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

// group returns the sessions under the header at index h.
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
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				issue, err := eng.LinearIssue(ctx, ref)
				if err != nil {
					return issuesLoadedMsg{gen: gen, err: err}
				}
				return issuesLoadedMsg{gen: gen, issues: []linear.Issue{issue}, single: true}
			}
		}
		var cmd tea.Cmd
		imp.keyInput, cmd = imp.keyInput.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "esc", "q":
		m.mode = modeBoard
	case "tab", "shift+tab":
		cmd := m.openImport(1 - imp.source)
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
		if imp.source == sourceLinear && m.eng.HasLinear() {
			imp.keyEntry = true
			imp.keyInput.Reset()
			cmd := imp.keyInput.Focus()
			return m, cmd
		}
	case "enter":
		var sessions []claude.Found
		var issues []linear.Issue
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
		}
		n := m.eng.ImportSessions(sessions) + len(m.eng.ImportIssues(issues))
		m.snap = m.eng.Snapshot()
		m.mode = modeBoard
		switch {
		case n > 0:
			m.selectTask(m.snap.Tasks[len(m.snap.Tasks)-1])
			m.setFlash(fmt.Sprintf("Imported %d card(s)", n))
		case len(sessions)+len(issues) > 0:
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
	lines := []string{
		tab("Claude sessions", imp.source == sourceClaude) + "  " + tab("Linear", imp.source == sourceLinear),
		"",
	}
	switch {
	case imp.loading:
		lines = append(lines, thinking.Render(spinner[m.frame%len(spinner)]+" loading…"))
	case imp.err != "":
		lines = append(lines, needsYou.Render(ansi.Wordwrap(imp.err, inner, " ")))
	case len(imp.items) == 0 && imp.source == sourceClaude:
		lines = append(lines, dim.Render("No running agents or `claude agents` sessions for this repository that aren't on the board yet."))
	case len(imp.items) == 0:
		lines = append(lines, dim.Render("No open issues assigned to you. Press / to fetch one by key."))
	}

	rows := max(3, height-12)
	first := max(0, imp.cursor-rows+1)
	for i := first; i < len(imp.items) && i < first+rows; i++ {
		it := imp.items[i]
		check := "[ ]"
		if it.selected {
			check = idle.Render("[x]")
		}
		pointer := "  "
		if i == imp.cursor {
			pointer = "▸ "
		}
		if it.isHeader() {
			group := imp.group(i)
			picked := 0
			for _, s := range group {
				if s.selected {
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
			lines = append(lines, ansi.Truncate(line, inner, "…"))
			continue
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
			label = bold.Render(is.Identifier) + " " + is.Title
			detail = dim.Render(is.State.Name + "  " + is.BranchName)
		}
		line := pointer + check + " " + label
		if i == imp.cursor {
			line = bold.Render(pointer+check+" ") + label
		}
		indent := ""
		if it.session != nil {
			indent = "  "
		}
		lines = append(lines, ansi.Truncate(indent+line, inner, "…"), indent+"      "+ansi.Truncate(detail, inner-6-len(indent), "…"))
	}
	if imp.keyEntry {
		lines = append(lines, "", "Issue › "+imp.keyInput.View())
	}
	hint := "space select · a all · enter import · tab switch source · esc close"
	if imp.more {
		hint = "space select · a all · m load more · enter import · tab switch · esc close"
	}
	if imp.source == sourceLinear {
		hint = "space select · a all · / fetch by key · enter import · tab switch · esc close"
	}
	lines = append(lines, "", dim.Render(hint))
	if imp.source == sourceClaude {
		lines = append(lines, dim.Render("Running agents keep running where they are; the card mirrors them until they exit."))
	}
	return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
}
