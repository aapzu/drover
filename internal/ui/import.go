package ui

import (
	"context"
	"fmt"
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

type importItem struct {
	session  *claude.Found
	issue    *linear.Issue
	selected bool
}

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
}

type sessionsLoadedMsg struct {
	gen      int
	sessions []claude.Found
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
	return importState{keyInput: ki}
}

func (m *Model) openImport(source int) tea.Cmd {
	m.mode = modeImport
	m.imp.source = source
	m.imp.items, m.imp.cursor, m.imp.err, m.imp.keyEntry = nil, 0, "", false
	m.imp.loading = true
	m.imp.gen++
	gen, eng := m.imp.gen, m.eng
	if source == sourceClaude {
		return func() tea.Msg { return sessionsLoadedMsg{gen: gen, sessions: eng.DiscoverSessions()} }
	}
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

func (m Model) updateImportLoaded(msg tea.Msg) Model {
	switch msg := msg.(type) {
	case sessionsLoadedMsg:
		if msg.gen != m.imp.gen || m.mode != modeImport {
			return m
		}
		m.imp.loading = false
		m.imp.items, m.imp.cursor = nil, 0
		for i := range msg.sessions {
			// Running agents are what you most likely want on the board.
			m.imp.items = append(m.imp.items, importItem{session: &msg.sessions[i], selected: msg.sessions[i].Running})
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
			imp.items[imp.cursor].selected = !imp.items[imp.cursor].selected
		}
	case "a":
		all := true
		for _, it := range imp.items {
			all = all && it.selected
		}
		for i := range imp.items {
			imp.items[i].selected = !all
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
		n := m.eng.ImportSessions(sessions) + m.eng.ImportIssues(issues)
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
		lines = append(lines, dim.Render("No Claude Code sessions for this repository that aren't on the board yet."))
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
		var label, detail string
		if s := it.session; s != nil {
			state := dim.Render(ago(s.Updated))
			if s.Running {
				state = thinking.Render("● running")
				if !s.Busy {
					state = idle.Render("● running · idle")
				}
			}
			where, _ := strings.CutPrefix(s.Cwd, m.eng.Root)
			if where == "" {
				where = "repo root"
			}
			label = s.Title
			detail = state + dim.Render("  "+strings.TrimPrefix(where, "/"))
		} else if is := it.issue; is != nil {
			label = bold.Render(is.Identifier) + " " + is.Title
			detail = dim.Render(is.State.Name + "  " + is.BranchName)
		}
		line := pointer + check + " " + label
		if i == imp.cursor {
			line = bold.Render(pointer+check+" ") + label
		}
		lines = append(lines, ansi.Truncate(line, inner, "…"), "      "+ansi.Truncate(detail, inner-6, "…"))
	}
	if imp.keyEntry {
		lines = append(lines, "", "Issue › "+imp.keyInput.View())
	}
	hint := "space select · a all · enter import · tab switch source · esc close"
	if imp.source == sourceLinear {
		hint = "space select · a all · / fetch by key · enter import · tab switch · esc close"
	}
	lines = append(lines, "", dim.Render(hint))
	if imp.source == sourceClaude {
		lines = append(lines, dim.Render("Running agents keep running where they are; the card mirrors them until they exit."))
	}
	return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
}
