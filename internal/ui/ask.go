package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aapzu/drover/internal/assistant"
)

const askKey = ":"

// askAssistant is replaced in tests.
var askAssistant = assistant.Ask

type askEntry struct {
	question, answer string
	err              error
}

type askDoneMsg struct {
	answer string
	err    error
}

type askResetMsg struct{ err error }

func newAskInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Ask about the board or tell it to move cards"
	return ti
}

func (m *Model) openAsk() tea.Cmd {
	m.mode = modeAsk
	m.askInput.Focus()
	m.syncAsk()
	return textinput.Blink
}

func (m Model) updateAsk(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeBoard
		return m, nil
	case "up", "down", "pgup", "pgdown":
		var cmd tea.Cmd
		m.askView, cmd = m.askView.Update(msg)
		return m, cmd
	case "ctrl+n":
		if m.askBusy {
			return m, nil
		}
		m.askBusy = true
		root := m.eng.Root
		return m, func() tea.Msg { return askResetMsg{assistant.Reset(root)} }
	case "enter":
		question := strings.TrimSpace(m.askInput.Value())
		if question == "" || m.askBusy {
			return m, nil
		}
		m.askInput.Reset()
		m.askLog = append(m.askLog, askEntry{question: question})
		m.askBusy = true
		m.syncAsk()
		root, exe := m.eng.Root, m.eng.Exe()
		return m, func() tea.Msg {
			answer, err := askAssistant(context.Background(), root, exe, question, false)
			return askDoneMsg{answer, err}
		}
	}
	var cmd tea.Cmd
	m.askInput, cmd = m.askInput.Update(msg)
	return m, cmd
}

// finishReset starts a new conversation once the old one is forgotten.
func (m *Model) finishReset(msg askResetMsg) {
	m.askBusy = false
	if msg.err != nil {
		m.askLog = append(m.askLog, askEntry{question: "new conversation", err: msg.err})
	} else {
		m.askLog = nil
	}
	m.syncAsk()
}

// finishAsk files the answer under the question that is waiting for it.
func (m *Model) finishAsk(msg askDoneMsg) {
	m.askBusy = false
	if n := len(m.askLog); n > 0 {
		m.askLog[n-1].answer, m.askLog[n-1].err = msg.answer, msg.err
	}
	m.syncAsk()
	switch {
	case m.mode == modeAsk:
	case msg.err != nil:
		m.setFlash("The assistant failed; press " + askKey + " to see why")
	default:
		m.setFlash("The assistant answered; press " + askKey + " to read it")
	}
}

// syncAsk redraws the conversation and scrolls to its end.
func (m *Model) syncAsk() {
	m.askView.Width, m.askView.Height = max(0, m.width-4), max(0, m.height-4)
	m.askView.SetContent(m.renderAskLog())
	m.askView.GotoBottom()
}

func (m Model) renderAskLog() string {
	if len(m.askLog) == 0 {
		return muted.Render("Ask where cards are, what needs you, or tell the assistant to move them.\nctrl+n starts a new conversation.")
	}
	var b strings.Builder
	for i, e := range m.askLog {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(bold.Render("› " + e.question))
		b.WriteString("\n")
		b.WriteString(e.answer)
		if e.err != nil {
			if e.answer != "" {
				b.WriteString("\n")
			}
			b.WriteString(alert.Render(e.err.Error()))
		}
		if e.answer == "" && e.err == nil && i == len(m.askLog)-1 && m.askBusy {
			b.WriteString(dim.Render("thinking…"))
		}
	}
	return lipgloss.NewStyle().Width(max(10, m.width-6)).Render(b.String())
}

func (m Model) renderAsk(height int) string {
	return panelStyle.Width(m.width - 2).Height(height - 2).Render(m.askView.View())
}

func (m Model) renderAskFooter() string {
	prompt := "Ask ›"
	if m.askBusy {
		prompt = spinner[m.frame%len(spinner)] + " Ask ›"
	}
	return fmt.Sprintf(" %s %s", bold.Render(prompt), m.askInput.View())
}
