// Package ui is the Bubble Tea board: columns of task cards, a live preview
// of the selected agent, and keys to move cards and talk to agents.
package ui

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/gitx"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

type mode int

const (
	modeBoard mode = iota
	modeForm
	modeSend
	modeMove
	modeConfirm
	modeDetail
	modeHelp
	modeImport
	modeLive
)

type tickMsg time.Time
type previewMsg struct {
	taskID, content string
	history         int
	at              time.Time
}
type noticeMsg string
type actionDoneMsg struct {
	err       error
	retryDone func() tea.Cmd
}
type attachDoneMsg struct{ err error }
type suspendAllMsg struct{}

type Model struct {
	eng    *engine.Engine
	snap   engine.Snapshot
	branch string

	width, height int
	mode          mode
	frame         int

	col        int
	selected   map[string]string // column id -> selected task id
	previewFor string
	previewTxt string
	previewAt  time.Time
	// previewScroll is how many lines above the bottom the preview shows.
	previewScroll  int
	previewHistory int

	prefs   prefs
	drag    dragState
	keys    *keySender
	liveGen int

	flash   string
	flashAt time.Time

	// form
	editingID   string
	titleInput  textinput.Model
	promptInput textarea.Model
	formFocus   int

	sendInput textinput.Model
	// modalTask is the task the move picker or send prompt was opened for.
	modalTask string

	confirmText   string
	confirmAction func() tea.Cmd

	suspendAll bool

	detail viewport.Model

	imp importState
}

func New(eng *engine.Engine) Model {
	ti := textinput.New()
	ti.Placeholder = "Short title"
	ti.CharLimit = 200
	ta := textarea.New()
	ta.Placeholder = "What should the agent do? (optional)"
	ta.ShowLineNumbers = false
	ta.SetHeight(8)
	si := textinput.New()
	si.Placeholder = "Message for the agent"
	m := Model{
		eng:         eng,
		snap:        eng.Snapshot(),
		branch:      gitx.CurrentBranch(eng.Root),
		selected:    map[string]string{},
		prefs:       loadPrefs(eng.Root),
		keys:        newKeySender(),
		titleInput:  ti,
		promptInput: ta,
		sendInput:   si,
		detail:      viewport.New(80, 20),
		imp:         newImportState(),
	}
	if !m.snap.BoardFound {
		m.setFlash("No kangentic.json found, using the default board (press W to write it)")
	}
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(), m.waitNotice()}
	if !m.prefs.NoMouse {
		cmds = append(cmds, tea.EnableMouseCellMotion)
	}
	return tea.Batch(cmds...)
}

func (m *Model) savePrefs() {
	if err := m.prefs.save(m.eng.Root); err != nil {
		m.setFlash("Could not save layout: " + err.Error())
	}
}

func tick() tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) waitNotice() tea.Cmd {
	ch := m.eng.Notices()
	return func() tea.Msg { return noticeMsg(<-ch) }
}

func (m *Model) setFlash(s string) {
	m.flash = s
	m.flashAt = time.Now()
}

func (m Model) columns() []config.Column { return m.snap.Board.Columns }

func (m Model) tasksIn(colID string) []*store.Task {
	var out []*store.Task
	for _, t := range m.snap.Tasks {
		if t.ColumnID == colID {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out
}

// orphans are tasks whose column no longer exists in kangentic.json; they are
// shown in the first column so they can be moved somewhere valid.
func (m Model) tasksForColumn(i int) []*store.Task {
	col := m.columns()[i]
	tasks := m.tasksIn(col.ID)
	if i == 0 {
		for _, t := range m.snap.Tasks {
			if _, _, ok := m.snap.Board.Column(t.ColumnID); !ok {
				tasks = append(tasks, t)
			}
		}
	}
	return tasks
}

func (m Model) current() *store.Task {
	if m.col >= len(m.columns()) {
		return nil
	}
	tasks := m.tasksForColumn(m.col)
	if len(tasks) == 0 {
		return nil
	}
	id := m.selected[m.columns()[m.col].ID]
	for _, t := range tasks {
		if t.ID == id {
			return t
		}
	}
	return tasks[0]
}

func (m Model) task(id string) *store.Task {
	for _, t := range m.snap.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (m Model) selectedIndex() int {
	cur := m.current()
	for i, t := range m.tasksForColumn(m.col) {
		if cur != nil && t.ID == cur.ID {
			return i
		}
	}
	return 0
}

func (m *Model) selectTask(t *store.Task) {
	for i, c := range m.columns() {
		if c.ID == t.ColumnID {
			m.col = i
			m.selected[c.ID] = t.ID
		}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.promptInput.SetWidth(min(100, m.width-8))
		m.titleInput.Width = min(100, m.width-8)
		m.sendInput.Width = max(20, m.width-14)
		m.detail.Width, m.detail.Height = m.width-4, m.height-4
		return m, nil
	case tickMsg:
		m.frame++
		m.snap = m.eng.Snapshot()
		if m.col >= len(m.columns()) {
			m.col = len(m.columns()) - 1
		}
		if (m.mode == modeMove || m.mode == modeSend) && m.task(m.modalTask) == nil {
			m.mode = modeBoard
		}
		if m.flash != "" && time.Since(m.flashAt) > 6*time.Second {
			m.flash = ""
		}
		select {
		case err := <-m.keys.errs:
			m.setFlash("Typing into the agent failed: " + err.Error())
		default:
		}
		cmds := []tea.Cmd{tick()}
		if !m.prefs.HidePreview && m.mode != modeLive && m.frame%2 == 0 {
			cmds = append(cmds, m.capturePreview())
		}
		return m, tea.Batch(cmds...)
	case previewMsg:
		if msg.at.Before(m.previewAt) {
			return m, nil
		}
		switch {
		case msg.taskID != m.previewFor:
			m.previewScroll = 0
		case m.previewScroll > 0:
			// Keep the scrolled view on the same lines as output arrives.
			m.previewScroll += msg.history - m.previewHistory
		}
		m.previewScroll = clamp(m.previewScroll, 0, msg.history)
		m.previewFor, m.previewTxt, m.previewAt, m.previewHistory = msg.taskID, msg.content, msg.at, msg.history
		return m, nil
	case liveTickMsg:
		if m.mode != modeLive || msg.gen != m.liveGen {
			return m, nil
		}
		if t := m.current(); t == nil || !m.hasLiveSession(t.ID) {
			m.mode = modeBoard
			m.setFlash("The agent is no longer running; back on the board.")
			return m, nil
		}
		return m, tea.Batch(liveTick(m.liveGen), m.capturePreview())
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case sessionsLoadedMsg, issuesLoadedMsg:
		return m.updateImportLoaded(msg), nil
	case noticeMsg:
		m.setFlash(string(msg))
		return m, m.waitNotice()
	case actionDoneMsg:
		m.snap = m.eng.Snapshot()
		if errors.Is(msg.err, engine.ErrDirty) && msg.retryDone != nil {
			m.mode = modeConfirm
			m.confirmText = "The worktree has uncommitted changes. Move to Done and delete the worktree anyway? (y/N)"
			m.confirmAction = msg.retryDone
			return m, nil
		}
		if msg.err != nil {
			m.setFlash("Error: " + msg.err.Error())
		}
		return m, nil
	case suspendAllMsg:
		m.suspendAll = true
		return m, tea.Quit
	case attachDoneMsg:
		if msg.err != nil {
			m.setFlash("Attach failed: " + msg.err.Error())
		}
		m.snap = m.eng.Snapshot()
		// Bubbletea disables the mouse for ExecProcess and doesn't turn it back on.
		if !m.prefs.NoMouse {
			return m, tea.EnableMouseCellMotion
		}
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case modeForm:
			return m.updateForm(msg)
		case modeSend:
			return m.updateSend(msg)
		case modeMove:
			return m.updateMove(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		case modeDetail:
			return m.updateDetail(msg)
		case modeHelp:
			m.mode = modeBoard
			return m, nil
		case modeImport:
			return m.updateImport(msg)
		case modeLive:
			return m.updateLive(msg)
		}
		return m.updateBoard(msg)
	}
	return m, nil
}

func (m Model) capturePreview() tea.Cmd {
	t := m.current()
	if t == nil {
		return func() tea.Msg { return previewMsg{} }
	}
	id := t.ID
	name, ok := m.eng.LiveSession(id)
	l := m.layout()
	w, h := m.width-2, l.previewH-2
	// Extra history lets the anchored view grow before the next capture.
	scrollback := 0
	if m.previewScroll > 0 {
		scrollback = m.previewScroll + 2*h
	}
	return func() tea.Msg {
		msg := previewMsg{taskID: id}
		if ok {
			if l.previewH > 0 {
				tmux.Resize(name, w, h)
			}
			if out, history, err := tmux.Capture(name, scrollback); err == nil {
				msg.content, msg.history = out, history
			}
		}
		msg.at = time.Now()
		return msg
	}
}

func (m Model) scrollPreview(lines int) (tea.Model, tea.Cmd) {
	m.previewScroll = clamp(m.previewScroll+lines, 0, m.previewHistory)
	return m, m.capturePreview()
}

func (m Model) hasLiveSession(id string) bool {
	_, ok := m.eng.LiveSession(id)
	return ok
}

func (m Model) action(fn func() error) tea.Cmd {
	return func() tea.Msg { return actionDoneMsg{err: fn()} }
}

func (m *Model) moveCmd(t *store.Task, target config.Column) tea.Cmd {
	id, targetID := t.ID, target.ID
	m.setFlash(fmt.Sprintf("Moving #%d to %s…", t.Number, target.Name))
	return func() tea.Msg {
		err := m.eng.Move(id, targetID, engine.MoveOptions{})
		retry := func() tea.Cmd {
			return m.action(func() error { return m.eng.Move(id, targetID, engine.MoveOptions{Force: true}) })
		}
		return actionDoneMsg{err: err, retryDone: retry}
	}
}

func (m Model) updateBoard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cols := m.columns()
	t := m.current()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "Q":
		n := 0
		for _, task := range m.snap.Tasks {
			for _, s := range task.Sessions {
				if s.Status == store.StatusRunning || s.Status == store.StatusQueued {
					n++
				}
			}
		}
		if n == 0 {
			m.setFlash("No agents running. Press q to quit.")
			break
		}
		m.mode = modeConfirm
		m.confirmText = fmt.Sprintf("Suspend all %d agents and quit? You can continue them with claude --resume. (y/N)", n)
		m.confirmAction = func() tea.Cmd { return func() tea.Msg { return suspendAllMsg{} } }
	case "?":
		m.mode = modeHelp
	case "left", "h":
		m.col = max(0, m.col-1)
	case "right", "l":
		m.col = min(len(cols)-1, m.col+1)
	case "up", "k", "down", "j":
		tasks := m.tasksForColumn(m.col)
		if len(tasks) == 0 {
			break
		}
		i := m.selectedIndex()
		if msg.String() == "up" || msg.String() == "k" {
			i = max(0, i-1)
		} else {
			i = min(len(tasks)-1, i+1)
		}
		m.selected[cols[m.col].ID] = tasks[i].ID
	case "g":
		if tasks := m.tasksForColumn(m.col); len(tasks) > 0 {
			m.selected[cols[m.col].ID] = tasks[0].ID
		}
	case "G":
		if tasks := m.tasksForColumn(m.col); len(tasks) > 0 {
			m.selected[cols[m.col].ID] = tasks[len(tasks)-1].ID
		}
	case "H", "shift+left", "L", "shift+right":
		if t == nil {
			break
		}
		_, idx, ok := m.snap.Board.Column(t.ColumnID)
		if !ok {
			idx = 0
		}
		next := idx + 1
		if msg.String() == "H" || msg.String() == "shift+left" {
			next = idx - 1
		}
		if next < 0 || next >= len(cols) {
			break
		}
		m.col = next
		m.selected[cols[next].ID] = t.ID
		cmd := m.moveCmd(t, cols[next])
		return m, cmd
	case "m":
		if t != nil {
			m.mode = modeMove
			m.modalTask = t.ID
		}
	case "n":
		m.openForm(nil)
		return m, textinput.Blink
	case "e":
		if t != nil {
			m.openForm(t)
			return m, textinput.Blink
		}
	case "x", "D", "delete":
		if t == nil {
			break
		}
		id := t.ID
		m.mode = modeConfirm
		m.confirmText = fmt.Sprintf("Delete #%d %q? Its agent is stopped and worktree removed; the branch is kept. (y/N)", t.Number, t.Title)
		if len(store.Members(m.snap.Tasks, store.Owner(m.snap.Tasks, t))) > 1 {
			m.confirmText = fmt.Sprintf("Delete #%d %q? Only this card goes; its agent keeps working for the other cards. (y/N)", t.Number, t.Title)
		}
		m.confirmAction = func() tea.Cmd { return m.action(func() error { return m.eng.DeleteTask(id) }) }
	case "enter", "a":
		if t == nil {
			break
		}
		name, ok := m.eng.LiveSession(t.ID)
		if s := m.sessionFor(t); !ok && s != nil && s.Status == store.StatusExternal {
			m.setFlash(fmt.Sprintf("#%d runs in another terminal (pid %d). Exit it there, then press r to resume it here.", t.Number, s.PID))
			break
		}
		if !ok {
			m.setFlash("No running agent. Press r to start or resume it.")
			break
		}
		return m, tea.ExecProcess(tmux.AttachCommand(name), func(err error) tea.Msg { return attachDoneMsg{err} })
	case "s":
		if t == nil {
			break
		}
		if _, ok := m.eng.LiveSession(t.ID); !ok {
			m.setFlash("No running agent for this task")
			break
		}
		m.mode = modeSend
		m.modalTask = t.ID
		m.sendInput.Reset()
		m.sendInput.Focus()
		return m, textinput.Blink
	case "r":
		if t != nil {
			id := t.ID
			m.setFlash(fmt.Sprintf("Starting agent for #%d…", t.Number))
			return m, m.action(func() error { return m.eng.Start(id) })
		}
	case "S":
		if t != nil {
			id := t.ID
			return m, m.action(func() error { m.eng.Suspend(id); return nil })
		}
	case "p":
		m.prefs.HidePreview = !m.prefs.HidePreview
		m.savePrefs()
	case "tab":
		return m.startLive()
	case "pgup", "pgdown":
		page := max(1, m.layout().previewH-3)
		if msg.String() == "pgdown" {
			page = -page
		}
		return m.scrollPreview(page)
	case "+", "=", "-", "_":
		l := m.layout()
		if l.previewH == 0 {
			break
		}
		step := 2
		if s := msg.String(); s == "-" || s == "_" {
			step = -2
		}
		m.prefs.PreviewHeight = clamp(l.previewH+step, minPreviewHeight, l.bodyH-minBoardHeight)
		m.savePrefs()
	case "<", ">":
		for _, c := range m.layout().cols {
			if c.index != m.col {
				continue
			}
			step := 4
			if msg.String() == "<" {
				step = -4
			}
			m.prefs.ColumnWidths[cols[m.col].ID] = clamp(c.width+step, minResizedWidth, m.width)
			m.savePrefs()
		}
	case "0":
		m.prefs.PreviewHeight, m.prefs.ColumnWidths, m.prefs.Density = 0, map[string]int{}, densityNormal
		m.savePrefs()
	case "z":
		m.prefs.Density = (m.prefs.Density + 1) % 3
		m.savePrefs()
	case "M":
		m.prefs.NoMouse = !m.prefs.NoMouse
		m.savePrefs()
		if m.prefs.NoMouse {
			m.setFlash("Mouse off: the terminal selects text again. M turns it back on.")
			return m, tea.DisableMouse
		}
		m.setFlash("Mouse on: drag cards, column edges and the preview border.")
		return m, tea.EnableMouseCellMotion
	case "i":
		if t != nil {
			m.mode = modeDetail
			m.detail.SetContent(m.renderDetail(t))
			m.detail.GotoTop()
		}
	case "I":
		cmd := m.openImport(sourceClaude)
		return m, cmd
	case "W":
		if err := m.eng.WriteDefaultBoard(); err != nil {
			m.setFlash("Error: " + err.Error())
		} else {
			m.snap = m.eng.Snapshot()
			m.setFlash("Wrote kangentic.json")
		}
	}
	return m, nil
}

func (m *Model) openForm(t *store.Task) {
	m.mode = modeForm
	m.formFocus = 0
	m.editingID = ""
	m.titleInput.Reset()
	m.promptInput.Reset()
	if t != nil {
		m.editingID = t.ID
		m.titleInput.SetValue(t.Title)
		m.promptInput.SetValue(t.Prompt)
	}
	m.titleInput.Focus()
	m.promptInput.Blur()
}

func (m Model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeBoard
		return m, nil
	case "tab", "shift+tab":
		m.formFocus = 1 - m.formFocus
		if m.formFocus == 0 {
			m.promptInput.Blur()
			cmd := m.titleInput.Focus()
			return m, cmd
		}
		m.titleInput.Blur()
		cmd := m.promptInput.Focus()
		return m, cmd
	case "enter":
		if m.formFocus == 0 {
			m.formFocus = 1
			m.titleInput.Blur()
			cmd := m.promptInput.Focus()
			return m, cmd
		}
	case "ctrl+s":
		title := strings.TrimSpace(m.titleInput.Value())
		if title == "" {
			m.setFlash("A task needs a title")
			return m, nil
		}
		prompt := strings.TrimSpace(m.promptInput.Value())
		if m.editingID != "" {
			m.eng.EditTask(m.editingID, title, prompt)
		} else {
			t := m.eng.CreateTask(title, prompt)
			m.snap = m.eng.Snapshot()
			m.mode = modeBoard
			if start := m.snap.Board.StartColumn(); start.ID != t.ColumnID {
				_, m.col, _ = m.snap.Board.Column(start.ID)
				m.selected[start.ID] = t.ID
				return m, m.moveCmd(t, start)
			}
			m.selectTask(t)
			m.setFlash(fmt.Sprintf("Created #%d. Move it right (L) to start an agent.", t.Number))
		}
		m.snap = m.eng.Snapshot()
		m.mode = modeBoard
		return m, nil
	}
	var cmd tea.Cmd
	if m.formFocus == 0 {
		m.titleInput, cmd = m.titleInput.Update(msg)
	} else {
		m.promptInput, cmd = m.promptInput.Update(msg)
	}
	return m, cmd
}

func (m Model) updateSend(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeBoard
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.sendInput.Value())
		m.mode = modeBoard
		t := m.task(m.modalTask)
		if text == "" || t == nil {
			return m, nil
		}
		id := t.ID
		m.setFlash(fmt.Sprintf("Sent to #%d", t.Number))
		return m, m.action(func() error { return m.eng.Send(id, text) })
	}
	var cmd tea.Cmd
	m.sendInput, cmd = m.sendInput.Update(msg)
	return m, cmd
}

func (m Model) updateMove(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = modeBoard
	key := msg.String()
	if len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return m, nil
	}
	i := int(key[0] - '1')
	t := m.task(m.modalTask)
	if t == nil || i >= len(m.columns()) {
		return m, nil
	}
	target := m.columns()[i]
	m.col = i
	m.selected[target.ID] = t.ID
	cmd := m.moveCmd(t, target)
	return m, cmd
}

// SuspendAllRequested reports whether the board quit with Q, asking main to
// suspend every agent.
func (m Model) SuspendAllRequested() bool { return m.suspendAll }

func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = modeBoard
	if msg.String() == "y" || msg.String() == "Y" {
		return m, m.confirmAction()
	}
	return m, nil
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "i":
		m.mode = modeBoard
		return m, nil
	}
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	return m, cmd
}
