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

	tooSmallForAssistant = "The terminal is too small for the assistant panel"
)

var (
	stopAssistant    = assistant.Stop
	startAssistant   = assistant.Start
	restartAssistant = assistant.Restart
	captureAssistant = func(name string, width, height, scrollback int) (out string, history int, mouse bool, err error) {
		tmux.Resize(name, width, height)
		out, history, mouse, err = tmux.Capture(name, scrollback)
		if err != nil && !tmux.Has(name) {
			err = fmt.Errorf("%w (%w)", errSessionEnded, err)
		}
		return out, history, mouse, err
	}
)

var errSessionEnded = errors.New("its tmux session ended: claude exited or failed to start, or the session was restarted or killed from outside this board")

type assistantPane struct {
	open            bool
	session         sessionState
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

func (p *assistantPane) clearFrame() { p.txt, p.history, p.scroll, p.mouse = "", 0, 0, false }

func (p *assistantPane) fail(err error) {
	p.session, p.lastErr = sessionStopped, err
	p.clearFrame()
}

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
// still starting it.
func (m Model) StopAssistant() error {
	if !m.assist.owns && !m.assist.starting() {
		return nil
	}
	return stopAssistant(m.eng.Root)
}

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

func (m Model) boardWidth() int { return m.width - m.assistantWidth() }

func (m *Model) openAssistant(fresh bool) tea.Cmd {
	if m.assistantFits() == 0 {
		m.setFlash(tooSmallForAssistant)
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
	default:
		cmds = append(cmds, m.captureAssistantCmd())
	}
	return tea.Batch(cmds...)
}

func (m *Model) focusAssistant() tea.Cmd {
	m.mode = modeAssistant
	m.assist.tickGen++
	return assistantTick(m.assist.tickGen)
}

func (m *Model) startAssistantSession(fresh bool) tea.Cmd {
	board := m.snap.Board
	if board == nil {
		return m.finishAssistantStart(assistantStartedMsg{epoch: m.assist.epoch, err: errors.New("the board isn't loaded yet")})
	}
	m.assist.session, m.assist.lastErr = sessionStarting, nil
	m.assist.clearFrame()
	m.assist.epoch++
	epoch := m.assist.epoch
	root, exe := m.eng.Root, m.eng.Exe()
	return func() tea.Msg {
		if fresh {
			err := restartAssistant(root, exe, board)
			return assistantStartedMsg{epoch: epoch, created: err == nil, err: err}
		}
		created, err := startAssistant(root, exe, board)
		return assistantStartedMsg{epoch: epoch, created: created, err: err}
	}
}

func (m *Model) finishAssistantStart(msg assistantStartedMsg) tea.Cmd {
	if msg.created {
		m.assist.owns = true
	}
	if msg.epoch != m.assist.epoch {
		return nil
	}
	if msg.err != nil {
		m.assist.fail(msg.err)
		m.setFlash("Starting the assistant failed: " + msg.err.Error())
		return nil
	}
	m.assist.session = sessionRunning
	return m.captureAssistantCmd()
}

func (m Model) assistantCapturing() bool {
	return m.assistantWidth() > 0 && m.assist.running()
}

func (m Model) captureAssistantCmd() tea.Cmd {
	name, epoch := assistant.SessionName(m.eng.Root), m.assist.epoch
	w, h := m.assistantWidth()-2, m.bodyHeight()-2
	if w < 1 || h < 1 {
		return nil
	}
	scrollback := scrollbackFor(m.assist.scroll, h)
	return func() tea.Msg {
		at := time.Now()
		out, history, mouse, err := captureAssistant(name, w, h, scrollback)
		return assistantFrameMsg{epoch: epoch, txt: out, history: history, mouse: mouse, err: err, at: at}
	}
}

func (m *Model) applyAssistantFrame(msg assistantFrameMsg) {
	if msg.epoch != m.assist.epoch || msg.at.Before(m.assist.at) || !m.assist.running() {
		return
	}
	m.assist.at = msg.at
	if msg.err != nil {
		if errors.Is(msg.err, errSessionEnded) {
			m.assist.fail(msg.err)
			m.assist.owns = false
		} else if text := "Reading the assistant failed: " + msg.err.Error(); m.flash != text {
			m.setFlash(text)
		}
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
	switch {
	case m.assist.running():
	case m.assist.starting():
		return m, nil
	case msg.Type == tea.KeyEnter:
		cmd := m.startAssistantSession(false)
		return m, cmd
	default:
		m.setFlash("The assistant isn't running. Press enter to start it, ctrl+q to return.")
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

func (m Model) wheelAssistant(msg tea.MouseMsg, l layout) (tea.Model, tea.Cmd) {
	if !m.assist.running() {
		return m, nil
	}
	up := msg.Button == tea.MouseButtonWheelUp
	if m.assist.mouse && m.assist.history == 0 {
		x, y := clamp(msg.X-l.boardW, 1, l.assistantW-2), clamp(msg.Y-l.bodyY, 1, l.bodyH-2)
		m.keys.send(wheelJob(assistant.SessionName(m.eng.Root), up, x, y))
		return m, nil
	}
	lines := -3
	if up {
		lines = 3
	}
	scroll := clamp(m.assist.scroll+lines, 0, m.assist.history)
	if scroll == m.assist.scroll {
		return m, nil
	}
	m.assist.scroll = scroll
	return m, m.captureAssistantCmd()
}

func (m Model) renderAssistant(width, height int) string {
	focused := m.mode == modeAssistant
	var body string
	switch {
	case m.assist.starting():
		body = dim.Render("Starting the assistant…")
	case !m.assist.running():
		hint := "Press " + askKey + " to start it again."
		if focused {
			hint = "Press enter to start it again."
		}
		what, reason := "The assistant isn't running", ""
		if errors.Is(m.assist.lastErr, errSessionEnded) {
			what = "The assistant stopped"
		}
		if m.assist.lastErr != nil {
			reason = ": " + m.assist.lastErr.Error()
		}
		body = dim.Render(what + reason + ".\n" + hint)
	default:
		body = previewWindow(m.assist.txt, height-2, m.assist.scroll)
	}

	title, hint := " assistant ", "· "+askKey+" to type here "
	style := panelStyle
	if focused {
		style = style.BorderForeground(accent)
		title, hint = lipgloss.NewStyle().Foreground(accent).Bold(true).Render(" ● ASSISTANT "), "· ctrl+q back to board "
	}
	if m.assist.scroll > 0 {
		title += thinking.Render(fmt.Sprintf("· ↑ %d lines up ", m.assist.scroll))
	} else {
		title += dim.Render(hint)
	}

	return titledBox(style, title, body, width, height)
}
