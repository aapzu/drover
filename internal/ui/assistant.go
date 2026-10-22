package ui

import (
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aapzu/drover/internal/assistant"
	"github.com/aapzu/drover/internal/tmux"
)

const (
	askKey            = ":"
	newAskKey         = "N"
	minBoardWidth     = 60
	minAssistantWidth = 40
)

var (
	stopAssistant    = assistant.Stop
	startAssistant   = assistant.Start
	restartAssistant = assistant.Restart
	captureAssistant = func(name string, width, height, scrollback int) (out string, history int, mouse bool, err error) {
		tmux.Resize(name, width, height)
		return tmux.Capture(name, scrollback)
	}
)

// assistantPane is the side panel that mirrors the assistant's Claude session.
type assistantPane struct {
	open            bool
	session         sessionState
	started         bool
	epoch           int
	lastErr         error
	txt             string
	at              time.Time
	history, scroll int
	mouse           bool
	gen             int
}

type sessionState uint8

const (
	sessionStopped sessionState = iota
	sessionStarting
	sessionRunning
)

func (p assistantPane) starting() bool { return p.session == sessionStarting }
func (p assistantPane) alive() bool    { return p.session == sessionRunning }

type assistantStartedMsg struct {
	epoch int
	err   error
}
type assistantTickMsg struct{ gen int }
type assistantFrameMsg struct {
	epoch   int
	txt     string
	history int
	mouse   bool
	err     error
	at      time.Time
}

func assistantTick(gen int) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return assistantTickMsg{gen} })
}

// StopAssistant ends the assistant's session when this board started it, so
// quitting a board that never opened the panel leaves another board's alone.
func (m Model) StopAssistant() error {
	if !m.assist.started {
		return nil
	}
	return stopAssistant(m.eng.Root)
}

// assistantFits is the width the panel would take, or 0 when the terminal is too
// narrow to leave the board minBoardWidth or too short to show it.
func (m Model) assistantFits() int {
	w := max(minAssistantWidth, m.width*2/5)
	if m.width-w < minBoardWidth || m.bodyHeight() < minPreviewHeight {
		return 0
	}
	return w
}

func (m Model) assistantWidth() int {
	if !m.assist.open {
		return 0
	}
	return m.assistantFits()
}

// boardWidth is the width left for the columns and the preview.
func (m Model) boardWidth() int { return m.width - m.assistantWidth() }

// openAssistant shows the panel, focuses it and starts its session unless it
// runs. fresh starts a new conversation even if it does.
func (m *Model) openAssistant(fresh bool) tea.Cmd {
	if m.assistantFits() == 0 {
		m.setFlash("The terminal is too small for the assistant panel")
		return nil
	}
	m.assist.open = true
	m.focusAssistant()
	cmds := []tea.Cmd{assistantTick(m.assist.gen)}
	switch {
	case fresh:
		m.assist.session, m.assist.txt, m.assist.scroll = sessionStopped, "", 0
		cmds = append(cmds, m.startAssistantSession(true))
	case m.assist.session == sessionStopped:
		cmds = append(cmds, m.startAssistantSession(false))
	}
	return tea.Batch(cmds...)
}

// focusAssistant sends the keys to the panel and starts one capture loop for it.
func (m *Model) focusAssistant() {
	m.mode = modeAssistant
	m.assist.gen++
}

func (m *Model) hideAssistant() {
	m.assist.open = false
	if m.mode == modeAssistant {
		m.mode = modeBoard
	}
}

// startAssistantSession starts, or with fresh restarts, the assistant's tmux
// session off the UI thread.
func (m *Model) startAssistantSession(fresh bool) tea.Cmd {
	m.assist.session, m.assist.started = sessionStarting, true
	m.assist.epoch++
	epoch := m.assist.epoch
	start := startAssistant
	if fresh {
		start = restartAssistant
	}
	root, exe, board := m.eng.Root, m.eng.Exe(), m.snap.Board
	if board == nil {
		return func() tea.Msg { return assistantStartedMsg{epoch, errors.New("the board isn't loaded yet")} }
	}
	return func() tea.Msg { return assistantStartedMsg{epoch, start(root, exe, board)} }
}

func (m *Model) finishAssistantStart(msg assistantStartedMsg) tea.Cmd {
	if msg.epoch != m.assist.epoch {
		return nil
	}
	if msg.err != nil {
		m.assist.session = sessionStopped
		m.setFlash("Starting the assistant failed: " + msg.err.Error())
		return nil
	}
	m.assist.session, m.assist.lastErr = sessionRunning, nil
	return m.captureAssistantCmd()
}

// assistantCapturing is whether the panel's session should be polled: not while
// it is stopped or being started, when there is nothing to capture.
func (m Model) assistantCapturing() bool {
	return m.assist.open && m.assist.alive()
}

func (m Model) captureAssistantCmd() tea.Cmd {
	name, epoch := assistant.SessionName(m.eng.Root), m.assist.epoch
	w, h := m.assistantWidth()-2, m.bodyHeight()-2
	if w < 1 || h < 1 {
		return nil
	}
	scrollback := 0
	if m.assist.scroll > 0 {
		scrollback = m.assist.scroll + 2*h
	}
	return func() tea.Msg {
		out, history, mouse, err := captureAssistant(name, w, h, scrollback)
		return assistantFrameMsg{epoch: epoch, txt: out, history: history, mouse: mouse, err: err, at: time.Now()}
	}
}

func (m *Model) applyAssistantFrame(msg assistantFrameMsg) {
	if msg.epoch != m.assist.epoch || msg.at.Before(m.assist.at) || m.assist.starting() {
		return
	}
	if m.assist.scroll > 0 {
		m.assist.scroll += msg.history - m.assist.history
	}
	m.assist.scroll = clamp(m.assist.scroll, 0, msg.history)
	m.assist.txt, m.assist.history, m.assist.mouse = msg.txt, msg.history, msg.mouse
	m.assist.session, m.assist.lastErr, m.assist.at = sessionRunning, msg.err, msg.at
	if msg.err != nil {
		m.assist.session = sessionStopped
	}
}

func (m Model) updateAssistant(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == liveExitKey {
		m.mode = modeBoard
		return m, nil
	}
	if !m.assist.alive() {
		switch {
		case m.assist.starting():
		case msg.Type == tea.KeyEnter:
			return m, m.startAssistantSession(false)
		default:
			m.setFlash("The assistant isn't running. Press enter to start it, ctrl+q to return.")
		}
		return m, nil
	}
	job, ok := keyJobFor(assistant.SessionName(m.eng.Root), msg)
	if !ok {
		return m, nil
	}
	m.keys.send(job)
	m.assist.scroll = 0
	return m, afterKey(m.captureAssistantCmd())
}

// wheelAssistant scrolls the panel, or hands the wheel to Claude when it tracks
// the mouse and the panel has no history to scroll.
func (m Model) wheelAssistant(msg tea.MouseMsg, l layout) (tea.Model, tea.Cmd) {
	up := msg.Button == tea.MouseButtonWheelUp
	if m.assist.alive() && m.assist.mouse && m.assist.history == 0 {
		x, y := clamp(msg.X-l.boardW, 1, l.assistantW-2), clamp(msg.Y-l.bodyY, 1, l.bodyH-2)
		m.keys.send(wheelJob(assistant.SessionName(m.eng.Root), up, x, y))
		return m, nil
	}
	lines := -3
	if up {
		lines = 3
	}
	m.assist.scroll = clamp(m.assist.scroll+lines, 0, m.assist.history)
	return m, m.captureAssistantCmd()
}

func (m Model) renderAssistant(width, height int) string {
	body := ""
	switch {
	case m.assist.starting():
		body = dim.Render("Starting the assistant…")
	case !m.assist.alive():
		hint := askKey + " to start it again."
		if m.mode == modeAssistant {
			hint = "Press enter to start it again."
		}
		reason := ""
		if m.assist.lastErr != nil {
			reason = ": " + m.assist.lastErr.Error()
		}
		body = dim.Render("The assistant stopped" + reason + ".\n" + hint)
	default:
		body = previewWindow(m.assist.txt, height-2, m.assist.scroll)
	}

	title := " assistant "
	style := panelStyle
	switch {
	case m.mode == modeAssistant:
		style = style.BorderForeground(accent)
		title = lipgloss.NewStyle().Foreground(accent).Bold(true).Render(" ● ASSISTANT ") + dim.Render("· ctrl+q back to board ")
	case m.assist.scroll > 0:
		title += thinking.Render(fmt.Sprintf("· ↑ %d lines up ", m.assist.scroll))
	default:
		title += dim.Render("· " + askKey + " to type here ")
	}

	return titledBox(style, title, body, width, height)
}
