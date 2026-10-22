// Package ui is the Bubble Tea board: columns of task cards, a live preview
// of the selected agent, and keys to move cards and talk to agents.
package ui

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
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
	modeFilter
)

type tickMsg time.Time
type previewMsg struct {
	taskID, content string
	history         int
	mouse           bool
	at              time.Time
}
type noticeMsg string
type snapMsg engine.Snapshot
type actionDoneMsg struct {
	id          string
	at          time.Time
	err         error
	done        string
	snap        engine.Snapshot
	confirmText string
	confirm     func() tea.Cmd
	attach      bool
}
type attachDoneMsg struct {
	id       string
	paneGone bool
	err      error
}
type takeOverDoneMsg struct {
	taskID string
	err    error
	snap   engine.Snapshot
}
type externalAttachMsg struct {
	cmd         *exec.Cmd
	id          string
	number, pid int
}
type suspendAllMsg struct{}
type sessionStartedMsg struct {
	task *store.Task
	err  error
	snap engine.Snapshot
}

type Model struct {
	eng      *engine.Engine
	snap     engine.Snapshot
	focused  string
	attached string
	branch   string
	snapBusy bool

	// busy holds the actions the board started and is waiting on, one per
	// card, unlike snap.Busy, which holds the engine's own steps. The map is
	// shared by every copy of the Model and only written in Update.
	busy map[string]op

	width, height int
	mode          mode
	frame         int

	col         int
	selected    map[string]string // column id -> selected task id
	previewFor  string
	previewTxt  string
	previewAt   time.Time
	follow      string
	followUntil time.Time
	// previewScroll is how many lines above the bottom the preview shows.
	previewScroll  int
	previewHistory int
	previewMouse   bool

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

	sendInput   textinput.Model
	filter      string
	filterInput textinput.Model
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
	fi := textinput.New()
	fi.Placeholder = "Words to match"
	m := Model{
		eng:         eng,
		snap:        eng.Snapshot(),
		branch:      gitx.CurrentBranch(eng.Root),
		selected:    map[string]string{},
		busy:        map[string]op{},
		prefs:       loadPrefs(eng.Root),
		keys:        newKeySender(),
		titleInput:  ti,
		promptInput: ta,
		sendInput:   si,
		filterInput: fi,
		detail:      viewport.New(80, 20),
		imp:         newImportState(),
	}
	if !m.snap.BoardFound {
		m.setFlash("No drover.json found, using the default board (press W to write it)")
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
	hides := m.hidesArchived(colID)
	for _, t := range m.snap.Tasks {
		if m.columnOf(t) == colID && !(hides && t.Archived) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return m.updatedAt(out[i]).Before(m.updatedAt(out[j])) })
	return out
}

func pluralCards(n int) string {
	if n == 1 {
		return "the card"
	}
	return fmt.Sprintf("all %d cards", n)
}

// hidesArchived is whether a column hides its archived cards: only Done
// columns do, so a card is never lost if its column stops being Done.
func (m Model) hidesArchived(colID string) bool {
	col, _, _ := m.snap.Board.Column(colID)
	return col.IsDone() && !m.prefs.ShowArchived
}

// archivedIn counts the archived cards the board hides in a column.
func (m Model) archivedIn(colID string) int {
	if !m.hidesArchived(colID) {
		return 0
	}
	n := 0
	for _, t := range m.snap.Tasks {
		if t.Archived && m.columnOf(t) == colID {
			n++
		}
	}
	return n
}

// op is an action running on a card. A move also sets column, so the card
// jumps there at once instead of waiting for the engine.
type op struct {
	label, column string
	at            time.Time
}

// columnOf is the column the card is shown in: the one it is being moved to,
// else the one it is in.
func (m Model) columnOf(t *store.Task) string {
	if o := m.busy[t.ID]; o.column != "" {
		return o.column
	}
	return t.ColumnID
}

// updatedAt puts a card being moved at the end of its new column, where the
// engine puts it.
func (m Model) updatedAt(t *store.Task) time.Time {
	if o := m.busy[t.ID]; o.column != "" {
		return o.at
	}
	return t.UpdatedAt
}

// orphans are tasks whose column no longer exists in drover.json; they are
// shown in the first column so they can be moved somewhere valid.
func (m Model) tasksForColumn(i int) []*store.Task {
	col := m.columns()[i]
	tasks := m.tasksIn(col.ID)
	if i == 0 {
		for _, t := range m.snap.Tasks {
			if _, _, ok := m.snap.Board.Column(m.columnOf(t)); !ok {
				tasks = append(tasks, t)
			}
		}
	}
	if m.filter != "" {
		tasks = slices.DeleteFunc(slices.Clone(tasks), func(t *store.Task) bool { return !matches(t, m.filter) })
	}
	return tasks
}

// matches reports whether the card's number, title, branch, issue or PR
// contains every word of filter, ignoring case.
func matches(t *store.Task, filter string) bool {
	text := fmt.Sprintf("#%d %s %s", t.Number, t.Title, t.Branch)
	if t.Issue != nil {
		text += " " + t.Issue.Key
	}
	if t.PR != nil {
		text += fmt.Sprintf(" #%d %s %s", t.PR.Number, t.PR.Title, t.PR.Branch)
	}
	text = strings.ToLower(text)
	for _, word := range strings.Fields(strings.ToLower(filter)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
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
		if c.ID == m.columnOf(t) {
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
		m.filterInput.Width = max(20, m.width-14)
		m.detail.Width, m.detail.Height = m.width-4, m.height-4
		return m, nil
	case tickMsg:
		m.frame++
		m.syncFocus()
		if m.flash != "" && time.Since(m.flashAt) > 6*time.Second {
			m.flash = ""
		}
		select {
		case err := <-m.keys.errs:
			m.setFlash("Sending input to the agent failed: " + err.Error())
		default:
		}
		cmds := []tea.Cmd{tick()}
		if !m.snapBusy {
			m.snapBusy = true
			cmds = append(cmds, m.snapshot())
		}
		if !m.prefs.HidePreview && m.mode != modeLive && m.frame%2 == 0 {
			cmds = append(cmds, m.capturePreview())
		}
		switch {
		case m.follow == "":
		case m.snap.Busy[m.follow] == "":
			if cmd, ok := m.attach(m.follow); ok {
				cmds = append(cmds, cmd)
			}
			m.follow = ""
		case time.Now().After(m.followUntil):
			m.follow = ""
		}
		return m, tea.Batch(cmds...)
	case snapMsg:
		m.snapBusy = false
		m.applySnap(engine.Snapshot(msg))
		return m, nil
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
		m.previewMouse = msg.mouse
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
		if m.busy[msg.id].at.Equal(msg.at) {
			delete(m.busy, msg.id)
		}
		m.applySnap(msg.snap)
		if msg.confirm != nil {
			m.mode = modeConfirm
			m.confirmText = msg.confirmText
			m.confirmAction = msg.confirm
			return m, nil
		}
		var ext *engine.ExternalError
		if errors.As(msg.err, &ext) {
			m.confirmTakeOver(ext.ID, ext.Number, ext.PID)
			return m, nil
		}
		if msg.err != nil {
			m.setFlash("Error: " + msg.err.Error())
		} else if msg.done != "" {
			m.setFlash(msg.done)
		}
		if msg.err == nil && msg.attach && m.mode == modeBoard {
			if cmd, ok := m.attach(msg.id); ok {
				return m, cmd
			}
		}
		return m, nil
	case takeOverDoneMsg:
		m.applySnap(msg.snap)
		if msg.err != nil {
			m.setFlash("Error: " + msg.err.Error())
			return m, nil
		}
		if cmd, ok := m.attach(msg.taskID); ok {
			return m, cmd
		}
		m.setFlash("Stopped the other terminal's agent; it resumes here once a slot frees up")
		return m, nil
	case externalAttachMsg:
		if msg.cmd != nil {
			return m, attachExternal(msg.cmd)
		}
		m.confirmTakeOver(msg.id, msg.number, msg.pid)
		return m, nil
	case suspendAllMsg:
		m.suspendAll = true
		return m, tea.Quit
	case sessionStartedMsg:
		m.applySnap(msg.snap)
		m.selectTask(msg.task)
		if msg.err != nil {
			m.setFlash("Error: " + msg.err.Error())
			return m, nil
		}
		if cmd, ok := m.attach(msg.task.ID); ok {
			m.setFlash("")
			return m, cmd
		}
		return m, nil
	case attachDoneMsg:
		if msg.err != nil {
			m.setFlash("Attach failed: " + msg.err.Error())
		}
		m.attached = ""
		m.syncFocus()
		m.snap = m.eng.Snapshot()
		var cmds []tea.Cmd
		// Bubbletea disables the mouse for ExecProcess and doesn't turn it back on.
		if !m.prefs.NoMouse {
			cmds = append(cmds, tea.EnableMouseCellMotion)
		}
		if msg.paneGone && m.snap.Busy[msg.id] != "" {
			m.follow, m.followUntil = msg.id, time.Now().Add(time.Minute)
		}
		return m, tea.Batch(cmds...)
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
		case modeFilter:
			return m.updateFilter(msg)
		}
		return m.updateBoard(msg)
	}
	return m, nil
}

func (m Model) snapshot() tea.Cmd {
	eng := m.eng
	return func() tea.Msg { return snapMsg(eng.Snapshot()) }
}

// applySnap drops a snapshot older than the one shown: ticks and actions take
// theirs on separate goroutines, so they can arrive out of order. An empty one
// comes from an op that never ran.
func (m *Model) applySnap(snap engine.Snapshot) {
	if snap.Board == nil || snap.At.Before(m.snap.At) {
		return
	}
	m.snap = snap
	if m.col >= len(m.columns()) {
		m.col = len(m.columns()) - 1
	}
	if (m.mode == modeMove || m.mode == modeSend) && m.task(m.modalTask) == nil {
		m.mode = modeBoard
	}
}

func (m Model) capturePreview() tea.Cmd {
	t := m.current()
	if t == nil {
		return func() tea.Msg { return previewMsg{} }
	}
	id := t.ID
	name, ok := m.liveSession(id)
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
			if out, history, mouse, err := tmux.Capture(name, scrollback); err == nil {
				msg.content, msg.history, msg.mouse = out, history, mouse
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

// wheelTarget is the session that should get wheel events: its agent reads
// SGR mouse reports and tmux has no history for the preview to scroll.
// previewFor guards against flags left over from the previously selected task.
func (m Model) wheelTarget() (string, bool) {
	t := m.current()
	if t == nil || m.previewFor != t.ID || !m.previewMouse || m.previewHistory != 0 {
		return "", false
	}
	return m.liveSession(t.ID)
}

// liveSession reads the snapshot instead of the engine, which Tick can keep
// locked for long enough to freeze drawing.
func (m Model) liveSession(id string) (string, bool) {
	name, ok := m.snap.Live[id]
	return name, ok
}

func (m Model) hasLiveSession(id string) bool {
	_, ok := m.liveSession(id)
	return ok
}

type snapshotter interface {
	Snapshot() engine.Snapshot
}

type mover interface {
	snapshotter
	Move(id, targetID string, opts engine.MoveOptions) error
}

var errBusy = errors.New("that card is still busy")

// action runs fn off the UI thread and marks the card busy until it returns.
func (m Model) action(id, label string, fn func() error) tea.Cmd {
	return runOp(m.eng, m.busy, id, op{label: label}, func() actionDoneMsg {
		return actionDoneMsg{err: fn()}
	})
}

// runOp marks the card busy, runs work off the UI thread and returns its
// result with the board as it stands afterwards. A card runs one op at a time.
func runOp(eng snapshotter, busy map[string]op, id string, o op, work func() actionDoneMsg) tea.Cmd {
	if _, working := busy[id]; working {
		return func() tea.Msg { return actionDoneMsg{id: id, err: errBusy} }
	}
	o.at = time.Now()
	busy[id] = o
	return func() tea.Msg {
		msg := work()
		msg.id, msg.at, msg.snap = id, o.at, eng.Snapshot()
		return msg
	}
}

// attach opens the task's live agent terminal; Ctrl-q returns to the board.
func (m *Model) attach(id string) (tea.Cmd, bool) {
	name, ok := m.liveSession(id)
	if !ok {
		return nil, false
	}
	m.attached = id
	m.syncFocus()
	return tea.ExecProcess(tmux.AttachCommand(name), func(err error) tea.Msg {
		return attachDoneMsg{id: id, paneGone: !tmux.Has(name), err: err}
	}), true
}

// confirmTakeOver asks to stop the task's agent in its other terminal, resume
// it on the board and attach to it.
func (m *Model) confirmTakeOver(id string, number, pid int) {
	m.mode = modeConfirm
	m.confirmText = fmt.Sprintf("#%d runs in another terminal (pid %d). Stop it there and resume it here? (y/N)", number, pid)
	eng := m.eng
	m.confirmAction = func() tea.Cmd {
		return func() tea.Msg {
			err := eng.TakeOver(id)
			return takeOverDoneMsg{taskID: id, err: err, snap: eng.Snapshot()}
		}
	}
}

// attachExternal opens an agent that runs outside the board.
func attachExternal(cmd *exec.Cmd) tea.Cmd {
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return attachDoneMsg{err: err} })
}

func (m Model) moveCmd(t *store.Task, target config.Column) tea.Cmd {
	return moveTo(m.eng, m.busy, t.ID, t.Number, target)
}

// moveTo shows the card in the target column straight away while the engine
// works. A move that fails puts the card back.
func moveTo(eng mover, busy map[string]op, id string, number int, target config.Column) tea.Cmd {
	return runOp(eng, busy, id, op{label: "moving", column: target.ID}, func() actionDoneMsg {
		err := eng.Move(id, target.ID, engine.MoveOptions{})
		msg := actionDoneMsg{err: err}
		if err == nil {
			msg.done = fmt.Sprintf("Moved #%d to %s", number, target.Name)
		}
		return msg
	})
}

// archiveCard archives or restores a card off the UI thread. A worktree with
// uncommitted changes asks first, then retries with force.
func archiveCard(eng *engine.Engine, busy map[string]op, id string, number int, archive, force bool) tea.Cmd {
	return runOp(eng, busy, id, op{label: "archiving"}, func() actionDoneMsg {
		err := eng.ArchiveTask(id, archive, force)
		msg := actionDoneMsg{err: err}
		switch {
		case err == nil && archive:
			msg.done = fmt.Sprintf("Archived #%d. v shows archived cards.", number)
		case err == nil:
			msg.done = fmt.Sprintf("Restored #%d", number)
		case errors.Is(err, engine.ErrDirty):
			msg.confirmText = fmt.Sprintf("#%d's worktree has uncommitted changes. Archive and delete the worktree anyway? (y/N)", number)
			msg.confirm = func() tea.Cmd { return archiveCard(eng, busy, id, number, archive, true) }
		}
		return msg
	})
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
		_, idx, ok := m.snap.Board.Column(m.columnOf(t))
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
		m.setFlash("Starting a new session…")
		eng := m.eng
		return m, func() tea.Msg {
			t, err := eng.NewSession()
			return sessionStartedMsg{task: t, err: err, snap: eng.Snapshot()}
		}
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
		m.confirmText = fmt.Sprintf("Delete #%d %q? Its agent is stopped and worktree removed; the branch is kept. (y/N)", t.Number, taskTitle(t))
		if len(store.Members(m.snap.Tasks, store.Owner(m.snap.Tasks, t))) > 1 {
			m.confirmText = fmt.Sprintf("Delete #%d %q? Only this card goes; its agent keeps working for the other cards. (y/N)", t.Number, taskTitle(t))
		}
		m.confirmAction = func() tea.Cmd { return m.action(id, "deleting", func() error { return m.eng.DeleteTask(id) }) }
	case "enter", "a":
		if t == nil {
			break
		}
		cmd, ok := m.attach(t.ID)
		if s := m.sessionFor(t); !ok && s != nil && s.Status == store.StatusExternal {
			id, number, pid, eng := t.ID, t.Number, s.PID, m.eng
			return m, func() tea.Msg {
				cmd, _ := eng.ExternalAttach(id)
				return externalAttachMsg{cmd: cmd, id: id, number: number, pid: pid}
			}
		}
		if !ok {
			id := t.ID
			return m, runOp(m.eng, m.busy, id, op{label: "starting agent"}, func() actionDoneMsg {
				return actionDoneMsg{err: m.eng.Start(id), attach: true}
			})
		}
		return m, cmd
	case "s":
		if t == nil {
			break
		}
		if _, ok := m.liveSession(t.ID); !ok {
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
			return m, m.action(id, "starting agent", func() error { return m.eng.Start(id) })
		}
	case "/":
		m.mode = modeFilter
		m.filterInput.SetValue(m.filter)
		m.filterInput.CursorEnd()
		m.filterInput.Focus()
		return m, textinput.Blink
	case "esc":
		m.filter = ""
	case "c":
		if t != nil {
			name, err := m.eng.Shell(t.ID)
			if err != nil {
				m.setFlash("Error: " + err.Error())
				return m, nil
			}
			id := t.ID
			return m, tea.ExecProcess(tmux.AttachCommand(name), func(err error) tea.Msg {
				return attachDoneMsg{id: id, err: err}
			})
		}
	case "u":
		if t != nil {
			id, eng := t.ID, m.eng
			return m, runOp(eng, m.busy, id, op{label: "updating from base"}, func() actionDoneMsg {
				done, err := eng.UpdateFromBase(id)
				return actionDoneMsg{err: err, done: done}
			})
		}
	case "S":
		if t != nil {
			id := t.ID
			return m, m.action(id, "suspending", func() error { m.eng.Suspend(id); return nil })
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
	case "A":
		if t == nil {
			break
		}
		return m, archiveCard(m.eng, m.busy, t.ID, t.Number, !t.Archived, false)
	case "C":
		if !cols[m.col].IsDone() {
			m.setFlash("Only the Done column can be archived")
			break
		}
		n := 0
		for _, task := range m.tasksForColumn(m.col) {
			if !task.Archived {
				n++
			}
		}
		if n == 0 {
			m.setFlash("Nothing to archive")
			break
		}
		eng, colID := m.eng, cols[m.col].ID
		m.mode = modeConfirm
		m.confirmText = fmt.Sprintf("Archive %s in %s? They are hidden and their worktrees removed unless dirty; v shows them. (y/N)", pluralCards(n), cols[m.col].Name)
		m.confirmAction = func() tea.Cmd {
			return func() tea.Msg {
				eng.ArchiveColumn(colID)
				return snapMsg(eng.Snapshot())
			}
		}
	case "v":
		m.prefs.ShowArchived = !m.prefs.ShowArchived
		m.savePrefs()
	case "W":
		if err := m.eng.WriteDefaultBoard(); err != nil {
			m.setFlash("Error: " + err.Error())
		} else {
			m.snap = m.eng.Snapshot()
			m.setFlash("Wrote drover.json")
		}
	}
	return m, nil
}

func (m *Model) openForm(t *store.Task) {
	m.mode = modeForm
	m.formFocus = 0
	m.editingID = t.ID
	m.titleInput.SetValue(t.Title)
	m.promptInput.SetValue(t.Prompt)
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
		m.eng.EditTask(m.editingID, title, strings.TrimSpace(m.promptInput.Value()))
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

// updateFilter narrows the board as you type. Up and down move through the
// matches, enter keeps the filter, esc clears it.
func (m Model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "down":
		return m.updateBoard(msg)
	case "esc":
		m.filter = ""
		m.mode = modeBoard
		return m, nil
	case "enter":
		m.mode = modeBoard
		return m, nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	m.filter = strings.TrimSpace(m.filterInput.Value())
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
		return m, m.action(id, "sending", func() error { return m.eng.Send(id, text) })
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
