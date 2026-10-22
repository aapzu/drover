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
		out, history, mouse, err = tmux.Capture(name, scrollback)
		if err != nil && !tmux.Has(name) {
			err = errSessionEnded
		}
		return out, history, mouse, err
	}
)

var errSessionEnded = errors.New("its tmux session ended, so claude exited or failed to start")

// assistantPane is the side panel that mirrors the assistant's Claude session.
type assistantPane struct {
	open    bool
	session sessionState
	// owns is whether this board created the session, so it stops it on quit.
	owns            bool
	epoch           int
	lastErr         error
	txt             string
	at              time.Time
	history, scroll int
	mouse           bool
	tickGen         int
}

type sessionState uint8

const (
	sessionStopped sessionState = iota
	sessionStarting
	sessionRunning
)

func (p assistantPane) starting() bool { return p.session == sessionStarting }
func (p assistantPane) running() bool  { return p.session == sessionRunning }

type assistantStartedMsg struct {
	epoch   int
	created bool
	err     error
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

// StopAssistant ends the assistant's session if this board created it, or is
// still starting it, so that quitting a board that merely attached to another
// board's session leaves it running.
func (m Model) StopAssistant() error {
	if !m.assist.owns && !m.assist.starting() {
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

// openAssistant shows the panel, focuses it and starts its session unless it is
// running or starting. fresh starts a new conversation, but not while a start is
// in progress.
func (m *Model) openAssistant(fresh bool) tea.Cmd {
	if m.assistantFits() == 0 {
		m.setFlash("The terminal is too small for the assistant panel")
		return nil
	}
	m.assist.open = true
	cmds := []tea.Cmd{m.focusAssistant()}
	switch {
	case m.assist.starting():
		if fresh {
			m.setFlash("The assistant is still starting; press " + newAskKey + " again once it has")
		}
	case fresh:
		cmds = append(cmds, m.startAssistantSession(true))
	case !m.assist.running():
		cmds = append(cmds, m.startAssistantSession(false))
	}
	return tea.Batch(cmds...)
}

// focusAssistant sends the keys to the panel and starts the loop that captures
// it while it has focus; a loop from an earlier focus stops.
func (m *Model) focusAssistant() tea.Cmd {
	m.mode = modeAssistant
	m.assist.tickGen++
	return assistantTick(m.assist.tickGen)
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
	m.assist.session = sessionStarting
	m.assist.txt, m.assist.history, m.assist.scroll, m.assist.mouse = "", 0, 0, false
	m.assist.epoch++
	epoch := m.assist.epoch
	root, exe, board := m.eng.Root, m.eng.Exe(), m.snap.Board
	if board == nil {
		return func() tea.Msg {
			return assistantStartedMsg{epoch: epoch, err: errors.New("the board isn't loaded yet")}
		}
	}
	return func() tea.Msg {
		if fresh {
			err := restartAssistant(root, exe, board)
			return assistantStartedMsg{epoch, err == nil, err}
		}
		created, err := startAssistant(root, exe, board)
		return assistantStartedMsg{epoch, created, err}
	}
}

func (m *Model) finishAssistantStart(msg assistantStartedMsg) tea.Cmd {
	if msg.epoch != m.assist.epoch {
		return nil
	}
	if msg.err != nil {
		m.assist.session, m.assist.lastErr = sessionStopped, msg.err
		m.setFlash("Starting the assistant failed: " + msg.err.Error())
		return nil
	}
	m.assist.session, m.assist.lastErr = sessionRunning, nil
	m.assist.owns = m.assist.owns || msg.created
	return m.captureAssistantCmd()
}

// assistantCapturing is whether the panel is shown and its session is running,
// so there is something to poll.
func (m Model) assistantCapturing() bool {
	return m.assistantWidth() > 0 && m.assist.running()
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
	if msg.epoch != m.assist.epoch || msg.at.Before(m.assist.at) || !m.assist.running() {
		return
	}
	m.assist.at = msg.at
	if msg.err != nil {
		m.assist.session, m.assist.lastErr = sessionStopped, msg.err
		return
	}
	if m.assist.scroll > 0 {
		m.assist.scroll += msg.history - m.assist.history
	}
	m.assist.scroll = clamp(m.assist.scroll, 0, msg.history)
	m.assist.txt, m.assist.history, m.assist.mouse = msg.txt, msg.history, msg.mouse
}

func (m Model) updateAssistant(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == liveExitKey {
		m.mode = modeBoard
		return m, nil
	}
	if !m.assist.running() {
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
	if !m.assist.running() {
		return m, nil
	}
	if m.assist.mouse && m.assist.history == 0 {
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
	case !m.assist.running():
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
