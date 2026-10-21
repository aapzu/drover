// Package engine owns the board state and implements the transition
// rules: moving a card creates its worktree, starts or resumes its agent, and
// runs the column's automations.
//
// Locking: mu guards state and board and is only held briefly. Each task also
// has its own lock, held for a whole transition, so slow work (git, tmux,
// scripts) on one task never blocks the UI or other tasks.
package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/gitx"
	"github.com/aapzu/drover/internal/linear"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

// ErrDirty is returned when a move to Done would delete uncommitted work.
var ErrDirty = errors.New("worktree has uncommitted changes")

type Engine struct {
	Root                 string
	ProjectName          string
	PRPollInterval       time.Duration
	ReleaseFetchInterval time.Duration
	exe                  string
	isRepo               bool
	linear               *linear.Client
	lastPoll             time.Time

	lastPRPoll       time.Time
	lastReleaseFetch time.Time
	prPolling        atomic.Bool
	prScanErrs       map[string]string
	meMu             sync.Mutex
	me               string
	meErr            error
	meFailedAt       time.Time

	mu         sync.Mutex
	state      *store.State
	board      *config.Board
	boardFound bool
	boardStamp string

	taskLocks sync.Map
	typing    sync.Map // tmux session name -> *sync.Mutex
	launching sync.Map // tmux session names dequeued but not yet started
	busy      sync.Map // task id -> what a move is doing, shown on the card
	notices   chan string
	work      sync.WaitGroup
	stopping  context.Context
	stop      context.CancelFunc

	focused      string
	alerted      map[string]time.Time
	idleStopping sync.Map
}

func New(root string) (*Engine, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	board, found, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	state, err := store.Load(root)
	if err != nil {
		return nil, fmt.Errorf("load task state: %w", err)
	}
	e := &Engine{
		Root:                 root,
		ProjectName:          filepath.Base(root),
		PRPollInterval:       time.Minute,
		ReleaseFetchInterval: 5 * time.Minute,
		exe:                  exe,
		isRepo:               gitx.HasCommits(root),
		state:                state,
		board:                board,
		boardFound:           found,
		notices:              make(chan string, 64),
		prScanErrs:           map[string]string{},
		alerted:              map[string]time.Time{},
	}
	e.stopping, e.stop = context.WithCancel(context.Background())
	e.boardStamp = config.Stamp(root)
	if client, ok := linear.FromEnv(); ok {
		e.linear = client
	}
	if e.isRepo {
		gitx.EnsureExcluded(root, board.WorktreesPath(root))
	}
	e.reconcile()
	return e, nil
}

// Notices delivers short messages the UI should flash.
func (e *Engine) Notices() <-chan string { return e.notices }

func (e *Engine) notify(format string, args ...any) {
	select {
	case e.notices <- fmt.Sprintf(format, args...):
	default:
	}
}

type Snapshot struct {
	Board      *config.Board
	BoardFound bool
	Tasks      []*store.Task
	// Busy maps task ids to the step a move is on, such as "creating worktree".
	Busy map[string]string
	// Running agents count against MaxConcurrent; the rest queue.
	Running, MaxConcurrent int
	// Live maps task ids to the tmux session of their running agent, so the
	// UI can look one up without taking the engine's lock.
	Live map[string]string
	// At is when the state was read, under the engine's lock. A snapshot with
	// an earlier At is older.
	At time.Time
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	busy := map[string]string{}
	e.busy.Range(func(id, what any) bool {
		busy[id.(string)] = what.(string)
		return true
	})
	tasks := make([]*store.Task, len(e.state.Tasks))
	live := map[string]string{}
	for i, t := range e.state.Tasks {
		tasks[i] = t.Clone()
		if name, ok := e.liveSession(t); ok {
			live[t.ID] = name
		}
	}
	return Snapshot{Board: e.board, BoardFound: e.boardFound, Tasks: tasks, Busy: busy, Live: live, Running: e.runningCount(), MaxConcurrent: e.limit(), At: time.Now()}
}

func (e *Engine) setBusy(id, what string) {
	if what == "" {
		e.busy.Delete(id)
		return
	}
	e.busy.Store(id, what)
}

// with runs fn under mu and persists the state afterwards.
func (e *Engine) with(fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn()
	if err := e.state.Save(e.Root); err != nil {
		e.notify("save failed: %v", err)
	}
}

func (e *Engine) find(id string) *store.Task {
	for _, t := range e.state.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (e *Engine) lockTask(id string) func() {
	l, _ := e.taskLocks.LoadOrStore(id, &sync.Mutex{})
	mu := l.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (e *Engine) logf(t *store.Task, format string, args ...any) {
	e.with(func() { t.AddLog(fmt.Sprintf(format, args...)) })
}

// WriteDefaultBoard writes the built-in board to drover.json, without your
// drover.local.json settings.
func (e *Engine) WriteDefaultBoard() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.boardFound {
		return nil
	}
	if err := config.Default().Save(e.Root); err != nil {
		return err
	}
	e.boardFound = true
	return nil
}

func (e *Engine) worktreesDir() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.board.WorktreesPath(e.Root)
}

// worktreeFolderPrefix marks the folders the board creates, so a shared
// directory such as .claude/worktrees can also hold other tools' worktrees.
const worktreeFolderPrefix = "drover-"

// ownsWorktree reports whether path is under the default worktrees directory
// or a drover-<N> folder of the configured one.
func (e *Engine) ownsWorktree(path string) bool {
	if claude.Within(filepath.Join(e.Root, config.DefaultWorktreesDir), path) {
		return true
	}
	rel, err := filepath.Rel(e.worktreesDir(), path)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	folder, _, _ := strings.Cut(rel, string(filepath.Separator))
	number, ok := strings.CutPrefix(folder, worktreeFolderPrefix)
	if !ok {
		return false
	}
	_, err = strconv.ParseUint(number, 10, 64)
	return err == nil
}

// reloadBoard picks up edits to drover.json and drover.local.json made while
// the TUI runs.
func (e *Engine) reloadBoard() {
	stamp := config.Stamp(e.Root)
	e.mu.Lock()
	unchanged := stamp == e.boardStamp
	e.mu.Unlock()
	if unchanged {
		return
	}
	board, found, err := config.Load(e.Root)
	e.mu.Lock()
	e.boardStamp = stamp
	if err == nil {
		e.board, e.boardFound = board, found
	}
	e.mu.Unlock()
	if err != nil {
		e.notify("board config: %v", err)
		return
	}
	if e.isRepo {
		gitx.EnsureExcluded(e.Root, board.WorktreesPath(e.Root))
	}
	e.notify("Reloaded drover.json")
}

// reconcile marks sessions whose tmux session vanished while the TUI was
// closed (reboot, manual kill) as exited, so they can be resumed.
func (e *Engine) reconcile() {
	live := tmux.List()
	e.with(func() {
		for _, t := range e.state.Tasks {
			for _, s := range t.Sessions {
				if s.Status == store.StatusRunning && !live[s.Tmux] {
					s.Status = store.StatusExited
					s.Activity = store.ActivityIdle
				}
			}
		}
	})
}

func (e *Engine) CreateTask(title, prompt string) *store.Task {
	var task *store.Task
	e.with(func() {
		now := time.Now()
		task = &store.Task{
			ID:        uuid.NewString(),
			Number:    e.state.NextNumber,
			Title:     title,
			Prompt:    prompt,
			ColumnID:  e.board.TodoColumn().ID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		e.state.NextNumber++
		task.AddLog("Created")
		e.state.Tasks = append(e.state.Tasks, task)
	})
	return task
}

// NewSession adds a card whose agent starts right away in the main checkout.
func (e *Engine) NewSession() (*store.Task, error) {
	task := e.CreateTask("", "")
	_, _, unlock := e.lockGroup(task.ID)
	err := e.startScratch(task, true)
	unlock()
	if err != nil {
		e.DeleteTask(task.ID)
		return task, fmt.Errorf("starting session: %w", err)
	}
	return task, nil
}

// startScratch runs t's conversation in the main checkout. The caller holds
// the task lock.
func (e *Engine) startScratch(t *store.Task, fresh bool) error {
	e.mu.Lock()
	col := e.board.TodoColumn()
	e.mu.Unlock()
	err := e.spawn(t, col, store.MainSession, e.Root, fresh, "", true)
	e.with(func() {
		if s := t.Sessions[store.MainSession]; s != nil {
			s.Scratch = true
		}
	})
	return err
}

func (e *Engine) EditTask(id, title, prompt string) {
	e.with(func() {
		if t := e.find(id); t != nil {
			t.Title, t.Prompt = title, prompt
		}
	})
}

// DeleteTask stops the agent and removes the worktree. The branch is kept so
// no committed work is lost. A card that shares its session with other cards
// is removed alone, and the agent keeps working for the rest.
func (e *Engine) DeleteTask(id string) error {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return nil
	}
	e.mu.Lock()
	members := store.Members(e.state.Tasks, owner)
	e.mu.Unlock()
	if len(members) == 1 {
		e.stopSessions(t, "", store.StatusExited)
		if err := e.removeWorktree(t); err != nil {
			return err
		}
	}
	e.with(func() {
		if len(members) > 1 && t == owner {
			e.handOver(owner, members[1])
		}
		for i, x := range e.state.Tasks {
			if x.ID == id {
				e.state.Tasks = append(e.state.Tasks[:i], e.state.Tasks[i+1:]...)
				break
			}
		}
	})
	return nil
}

// ArchiveTask hides a card in Done from the board, or shows it again.
func (e *Engine) ArchiveTask(id string, archived bool) error {
	var err error
	e.with(func() {
		t := e.find(id)
		if t == nil {
			err = errors.New("task not found")
			return
		}
		if col, _, _ := e.board.Column(t.ColumnID); !col.IsDone() {
			err = fmt.Errorf("#%d is not in Done", t.Number)
			return
		}
		t.Archived = archived
		if archived {
			t.AddLog("Archived")
		} else {
			t.UpdatedAt = time.Now()
			t.AddLog("Restored from the archive")
		}
	})
	return err
}

// ArchiveColumn archives every card in a Done column and returns how many
// were.
func (e *Engine) ArchiveColumn(colID string) int {
	n := 0
	e.with(func() {
		if col, _, _ := e.board.Column(colID); !col.IsDone() {
			return
		}
		for _, t := range e.state.Tasks {
			if t.ColumnID == colID && !t.Archived {
				t.Archived = true
				t.AddLog("Archived")
				n++
			}
		}
	})
	return n
}

// archiveStale archives cards that have sat in Done for archiveDoneAfter and
// tells you, as they vanish from the board.
func (e *Engine) archiveStale(now time.Time) {
	after := e.board.ArchiveAfter()
	if after == 0 {
		return
	}
	n := 0
	for _, t := range e.state.Tasks {
		if t.Archived {
			continue
		}
		if col, _, _ := e.board.Column(t.ColumnID); !col.IsDone() {
			continue
		}
		if now.Sub(t.UpdatedAt) > after {
			t.Archived = true
			t.AddLog("Archived after " + e.board.ArchiveDoneAfter + " in Done")
			n++
		}
	}
	if n > 0 {
		e.notify("Archived %d cards after %s in Done. v shows them.", n, e.board.ArchiveDoneAfter)
	}
}

func (e *Engine) WorktreeDirty(id string) bool {
	e.mu.Lock()
	t := e.find(id)
	path := ""
	if t != nil {
		path = e.owner(t).WorktreePath
	}
	ignore := e.board.CopyFiles
	e.mu.Unlock()
	return gitx.Dirty(path, ignore...)
}

type MoveOptions struct {
	// Message is sent to the agent after the move (move_to_column rows).
	Message string
	// Force removes a dirty worktree when moving to Done.
	Force bool
	// evict marks a move you made, which may suspend an idle agent to free a
	// slot; automation moves queue instead.
	evict bool
	// skipEnter leaves out the target's enter group.
	skipEnter bool
}

// Move transitions a task into the column with id targetID.
func (e *Engine) Move(id, targetID string, opts MoveOptions) error {
	_, _, unlock := e.lockGroup(id)
	defer unlock()
	opts.evict = true
	return e.moveLocked(id, targetID, opts)
}

// moveLocked moves one card. The caller holds its group's lock. A card that
// shares its session with other cards only stops the agent, removes the
// worktree or resets the conversation once no card in the group needs them.
func (e *Engine) moveLocked(id, targetID string, opts MoveOptions) error {
	e.mu.Lock()
	t := e.find(id)
	board := e.board
	var owner *store.Task
	var from config.Column
	othersActive, othersEnded, othersTodo := false, true, true
	var nextDriver string
	if t != nil {
		owner = e.owner(t)
		from, _, _ = board.Column(t.ColumnID)
		for _, m := range store.Members(e.state.Tasks, owner) {
			if m == t {
				continue
			}
			col, _, _ := board.Column(m.ColumnID)
			if isAgentColumn(col) {
				othersActive = true
				nextDriver = m.ID
			}
			othersEnded = othersEnded && (col.IsTodo() || col.IsDone())
			othersTodo = othersTodo && col.IsTodo()
		}
	}
	e.mu.Unlock()
	if t == nil {
		return errors.New("task not found")
	}
	target, _, ok := board.Column(targetID)
	if !ok {
		return fmt.Errorf("unknown column %s", targetID)
	}
	if from.ID == target.ID {
		return nil
	}
	ending := (target.IsTodo() || target.IsDone()) && othersEnded
	external := e.externalCheck(owner)
	if external != nil && ending {
		return external
	}
	e.mu.Lock()
	worktree := owner.WorktreePath
	drives := isAgentColumn(target) || store.Driver(e.state.Tasks, owner) == t
	copied := e.board.CopyFiles
	e.mu.Unlock()
	if target.IsDone() && ending && !opts.Force && gitx.Dirty(worktree, copied...) {
		return ErrDirty
	}

	e.with(func() {
		t.ColumnID = target.ID
		t.UpdatedAt = time.Now()
		if opts.evict {
			t.PRMovePending = false
		}
		if drives {
			// Turn-end automations of the previous visit no longer apply.
			owner.Visit++
			for _, s := range owner.Sessions {
				s.TurnInVisit = false
			}
		}
		t.Archived = false
		t.AddLog(fmt.Sprintf("Moved %s → %s", from.Name, target.Name))
	})

	if !isAgentColumn(target) && !target.IsTodo() && !target.IsDone() && !opts.skipEnter {
		defer e.runEnterGroup(t, e.withoutAgent(t, target.Automations.OnEnter), groupContext{from: from, to: target})
	}
	switch {
	case !isAgentColumn(target) && othersActive:
		if drives {
			e.with(func() { owner.ActiveCard = nextDriver })
		}
		return nil
	case target.IsTodo() && othersTodo:
		e.stopSessions(owner, "", store.StatusExited)
		e.with(func() { owner.Sessions = nil })
		return e.removeWorktree(owner)
	case ending:
		e.stopSessions(owner, "", store.StatusSuspended)
		return e.removeWorktree(owner)
	case !isAgentColumn(target):
		e.stopSessions(owner, "", store.StatusSuspended)
		return nil
	case external != nil:
		e.logf(t, "Not starting an agent: its conversation is still running in another terminal")
		return nil
	default:
		return e.activate(t, target, from, opts.Message, !opts.skipEnter, opts.evict)
	}
}

// Start resumes or starts the agent for a task in place, without re-running
// the column's enter automations.
func (e *Engine) Start(id string) error {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return errors.New("task not found")
	}
	if err := e.externalCheck(owner); err != nil {
		return err
	}
	col, err := e.startColumn(t, owner)
	if err != nil {
		return err
	}
	return e.activate(t, col, col, "", false, true)
}

// TakeOver kills the process running the task's conversation in another
// terminal and resumes the conversation on the board.
func (e *Engine) TakeOver(id string) error {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return errors.New("task not found")
	}
	col, err := e.startColumn(t, owner)
	if err != nil {
		return err
	}
	for range 3 {
		if e.externalCheck(owner) == nil {
			return e.activate(t, col, col, "", false, true)
		}
		l, _ := e.externalLive(owner)
		pid := l.PID
		if pid != 0 {
			if err := claude.Terminate(pid); err != nil {
				e.logf(t, "Stopping agent in another terminal (pid %d) failed: %v", pid, err)
				return err
			}
		}
		e.with(func() {
			if s := externalSession(owner); s != nil {
				s.Status, s.Activity, s.Pending = store.StatusSuspended, store.ActivityIdle, nil
			}
			if pid != 0 {
				t.AddLog(fmt.Sprintf("Stopped agent in another terminal (pid %d) to resume it here", pid))
			}
		})
	}
	return fmt.Errorf("#%d keeps restarting in another terminal; exit it there", t.Number)
}

// externalLive is the registry entry for t's external conversation. A pid the
// board stored is not trusted: it may belong to another process by now.
func (e *Engine) externalLive(t *store.Task) (claude.Live, bool) {
	live := claude.Running()
	e.mu.Lock()
	defer e.mu.Unlock()
	if s := externalSession(t); s != nil {
		l, ok := live[s.AgentSessionID]
		return l, ok
	}
	return claude.Live{}, false
}

// startColumn returns the column t's agent would run in, or an error when the
// column runs no agent. A scratch session runs in To Do too.
func (e *Engine) startColumn(t, owner *store.Task) (config.Column, error) {
	e.mu.Lock()
	col, _, _ := e.board.Column(t.ColumnID)
	scratch := scratchSession(owner)
	e.mu.Unlock()
	if (col.IsTodo() && scratch == nil) || col.IsDone() {
		return col, fmt.Errorf("move the task out of %s to start an agent", col.Name)
	}
	return col, nil
}

// Suspend stops the task's agents but keeps their conversations to resume.
func (e *Engine) Suspend(id string) {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return
	}
	stopped, leftRunning := e.stopSessions(owner, "", store.StatusSuspended)
	if stopped > 0 {
		e.logf(t, "Agent suspended")
	} else if !leftRunning {
		e.notify("#%d has no running agent", t.Number)
	}
}

// Handoff is an agent SuspendAll stopped. SessionID is the conversation to
// continue with `claude --resume`, or empty if the agent never started one.
type Handoff struct {
	Number    int
	Title     string
	Dir       string
	SessionID string
}

// SuspendAll suspends every running or queued agent at once and returns the
// conversations to resume elsewhere, which externalCheck then guards.
func (e *Engine) SuspendAll() []Handoff {
	var owners []string
	var out []Handoff
	e.mu.Lock()
	for _, t := range e.state.Tasks {
		if e.owner(t) != t {
			continue
		}
		dir := t.WorktreePath
		if dir == "" {
			dir = e.Root
		}
		active := false
		for _, s := range t.Sessions {
			if s.Status != store.StatusRunning && s.Status != store.StatusQueued {
				continue
			}
			active = true
			h := Handoff{Number: t.Number, Title: t.Title, Dir: dir}
			if s.Started {
				h.SessionID = s.AgentSessionID
				s.HandedOff = true
			}
			out = append(out, h)
		}
		if active {
			owners = append(owners, t.ID)
		}
	}
	e.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range owners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Suspend(id)
		}()
	}
	wg.Wait()
	return out
}

var nonName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func (e *Engine) tmuxName(t *store.Task) string {
	project := nonName.ReplaceAllString(e.ProjectName, "-")
	return fmt.Sprintf("%s-%d-%s", project, t.Number, t.ID[:6])
}

func (e *Engine) sessionDir(t *store.Task, key string) string {
	if s := t.Sessions[key]; s != nil && s.Dir != "" {
		return s.Dir
	}
	return filepath.Join(store.Dir(e.Root), "sessions", t.ID, key)
}

// ensureWorktree returns the owner t's worktree, creating it if needed.
// busyID is the card that shows the progress.
func (e *Engine) ensureWorktree(t *store.Task, busyID string) (string, error) {
	e.mu.Lock()
	path, branch, base := t.WorktreePath, t.Branch, t.BaseBranch
	title, id, number := t.Title, t.ID, t.Number
	defaultBase := e.board.DefaultBaseBranch
	copyFiles, initScript := e.board.CopyFiles, e.board.InitScript
	imported := false
	for _, s := range t.Sessions {
		imported = imported || s.Imported
	}
	e.mu.Unlock()
	// An imported conversation can only be resumed from its own directory.
	if imported {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("the imported session's directory %s no longer exists", path)
		}
		return path, nil
	}
	if !e.isRepo {
		return e.Root, nil
	}
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	resolved, err := gitx.ResolveBase(e.Root, base, defaultBase)
	if err != nil {
		return "", err
	}
	if branch == "" {
		branch = gitx.BranchName(title, id)
	}
	e.setBusy(busyID, "creating worktree")
	path, err = gitx.CreateWorktree(e.Root, e.worktreesDir(), fmt.Sprintf("%s%d", worktreeFolderPrefix, number), branch, resolved)
	if err == nil {
		err = gitx.CopyFiles(e.Root, path, copyFiles)
	}
	if err == nil && initScript != "" {
		e.setBusy(busyID, "running init script")
		env := []string{"DROVER_PROJECTPATH=" + e.Root, "DROVER_WORKTREEPATH=" + path, "DROVER_BRANCHNAME=" + branch}
		if err = runShell(e.stopping, path, initScript, env, 10*time.Minute); err != nil {
			err = fmt.Errorf("init script: %w", err)
		}
	}
	e.setBusy(busyID, "starting agent")
	if err != nil {
		return "", err
	}
	tmux.Kill(e.shellName(t))
	e.with(func() {
		t.WorktreePath, t.Branch = path, branch
		if t.BaseBranch == "" {
			t.BaseBranch = resolved
		}
		t.AddLog(fmt.Sprintf("Worktree ready on %s (from %s)", branch, resolved))
	})
	return path, nil
}

// adoptScratch moves a conversation from the main checkout into worktree so
// the agent resumes it there. One that never got a message starts fresh.
func (e *Engine) adoptScratch(t *store.Task, s *store.Session, worktree string) {
	e.mu.Lock()
	path, started := s.TranscriptPath, s.Started
	if path == "" {
		path = claude.TranscriptPath(s.AgentSessionID, e.Root)
	}
	e.mu.Unlock()
	moved, err := e.moveConversation(t, path, worktree)
	switch {
	case err != nil && !moved:
		e.notify("#%d: couldn't move the conversation into the worktree, starting a new one: %v", t.Number, err)
	case !moved && started:
		e.notify("#%d: couldn't find the conversation to carry over, starting a new one", t.Number)
	}
	e.with(func() { s.Started, s.TranscriptPath, s.Scratch = moved, "", false })
}

// moveConversation moves the transcript at path into worktree's project and
// logs how that went.
func (e *Engine) moveConversation(t *store.Task, path, worktree string) (bool, error) {
	moved, err := claude.MoveTranscript(path, worktree)
	switch {
	case moved && err != nil:
		e.logf(t, "Moved the conversation into the worktree, but not its subagent output: %v", err)
	case moved:
		e.logf(t, "Moved the conversation into the worktree")
	case err != nil:
		e.logf(t, "Moving the conversation into the worktree failed: %v", err)
	default:
		e.logf(t, "No conversation found at %s", path)
	}
	return moved, err
}

// relocateLive keeps a running scratch conversation alive across the move,
// and returns the request asking it to switch into worktree itself. It
// declines, leaving the restart to the caller, when there is nothing to keep.
func (e *Engine) relocateLive(t *store.Task, s *store.Session, worktree string) (store.Pending, bool) {
	e.mu.Lock()
	ok := s.Status == store.StatusRunning && s.Started
	name, branch := s.Tmux, t.Branch
	e.mu.Unlock()
	if !ok || !tmux.Has(name) {
		return store.Pending{}, false
	}
	e.with(func() { s.Scratch = false })
	e.logf(t, "Asking the agent to move into the worktree")
	return store.Pending{Relocate: worktree, Text: fmt.Sprintf("This task now has its own git worktree at %s on branch %s. "+
		"Call the EnterWorktree tool with path %q to switch into it, and do all further work there.", worktree, branch, worktree)}, true
}

// relocateByRespawn resumes an agent that did not move into worktree when
// asked, in a new process there.
func (e *Engine) relocateByRespawn(t *store.Task, s *store.Session, col config.Column, key, worktree string) {
	e.logf(t, "The agent didn't move into the worktree, restarting it there")
	e.mu.Lock()
	name := s.Tmux
	e.mu.Unlock()
	tmux.Stop(name)
	e.adoptScratch(t, s, worktree)
	e.mu.Lock()
	fresh := !s.Started
	e.mu.Unlock()
	if err := e.spawn(t, col, key, worktree, fresh, "", false); err != nil {
		e.logf(t, "Respawn failed: %v", err)
	}
}

// carryTranscript moves a conversation that switched into worktree while
// running, and so is still recorded under the main checkout, next to it so
// `claude --resume` finds it there.
func (e *Engine) carryTranscript(t *store.Task, key, worktree string) {
	e.mu.Lock()
	id := t.Sessions[key].AgentSessionID
	e.mu.Unlock()
	if _, err := os.Stat(claude.TranscriptPath(id, worktree)); err == nil {
		return
	}
	e.moveConversation(t, claude.TranscriptPath(id, e.Root), worktree)
}

// settingsDiffer reports whether s runs with other settings than col wants.
func settingsDiffer(s *store.Session, col config.Column, mode string) bool {
	modeDiffers := s.PermissionMode != mode
	if mode == "" {
		modeDiffers = !s.ModeInherited
	}
	return modeDiffers || s.Model != col.ModelOverride || s.Effort != col.EffortOverride
}

// switchSettings moves a live, idle agent to col's settings, in place when it
// can and by resuming it in a new process otherwise, which it reports.
func (e *Engine) switchSettings(t *store.Task, key string, col config.Column, worktree string) (restarted bool, err error) {
	if e.retune(t, key, col) {
		return false, nil
	}
	e.mu.Lock()
	name := t.Sessions[key].Tmux
	e.mu.Unlock()
	tmux.Stop(name)
	return true, e.spawn(t, col, key, worktree, false, "", false)
}

// retune switches a running agent to col's permission mode as you would,
// and reports whether it did. A model or effort change needs a restart:
// /model and /effort save your pick as the default for every new session. It
// leaves Activity alone: switching modes starts no turn.
func (e *Engine) retune(t *store.Task, key string, col config.Column) bool {
	e.mu.Lock()
	s := t.Sessions[key]
	name, mode := s.Tmux, s.PermissionMode
	same := s.Model == col.ModelOverride && s.Effort == col.EffortOverride
	want := e.board.PermissionMode(col)
	e.mu.Unlock()
	if !same || want == "" {
		return false
	}

	defer e.lockTyping(name)()
	var err error
	if mode != want {
		err = switchMode(name, want)
	}
	e.with(func() {
		if err == nil {
			s.SetMode(want)
			t.AddLog(fmt.Sprintf("Switched agent to %s mode in place", want))
		} else {
			t.AddLog(fmt.Sprintf("Couldn't switch the agent to %s mode in place, restarting it: %v", want, err))
		}
	})
	return err == nil
}

// switchMode puts the agent in mode: /plan for plan mode, otherwise
// Shift+Tab until the screen shows it, as you would.
func switchMode(name, mode string) error {
	if mode == "plan" {
		if err := tmux.SendText(name, "/plan"); err != nil {
			return err
		}
	}
	seen := ""
	for range 6 {
		time.Sleep(300 * time.Millisecond)
		screen, err := tmux.Screen(name)
		if err != nil {
			return err
		}
		if seen = claude.ScreenMode(screen); seen == mode {
			return nil
		}
		if err := tmux.SendKey(name, "BTab"); err != nil {
			return err
		}
	}
	return fmt.Errorf("the screen shows %q", cmp.Or(seen, "no mode"))
}

// scratchSession returns t's conversation while it still runs in the main
// checkout. Called with mu held.
func scratchSession(t *store.Task) *store.Session {
	if s := t.Sessions[store.MainSession]; s != nil && s.Scratch {
		return s
	}
	return nil
}

func (e *Engine) removeWorktree(t *store.Task) error {
	tmux.Kill(e.shellName(t))
	e.mu.Lock()
	path := t.WorktreePath
	e.mu.Unlock()
	// Imported sessions may live in the main checkout or a worktree the board
	// did not create; those are never deleted.
	if path == "" || !e.isRepo || !e.ownsWorktree(path) {
		return nil
	}
	if err := gitx.RemoveWorktree(e.Root, path); err != nil {
		e.logf(t, "Removing worktree failed: %v", err)
		return err
	}
	e.with(func() {
		t.WorktreePath = ""
		t.AddLog("Worktree removed (branch kept)")
	})
	return nil
}

// stopSessions stops every session except keep and marks it status: the
// board's own panes, and conversations Claude Code moved to a background
// process whatever the session's status says. A conversation running in your
// own terminal is left alone and reported. It returns how many sessions it
// stopped and whether one was left running.
func (e *Engine) stopSessions(t *store.Task, keep string, status store.SessionStatus) (stopped int, leftRunning bool) {
	type candidate struct {
		s           *store.Session
		lastPID     int
		paneStopped bool
	}
	var names []string
	candidates := map[string]candidate{}
	markStopped := func(s *store.Session) {
		s.Status = status
		s.Activity = store.ActivityIdle
		s.Pending = nil
		stopped++
	}
	e.with(func() {
		for key, s := range t.Sessions {
			if key == keep {
				continue
			}
			c := candidate{s: s}
			switch s.Status {
			case store.StatusRunning, store.StatusQueued:
				markStopped(s)
				if s.Tmux != "" {
					names = append(names, s.Tmux)
					c.paneStopped = true
				}
			case store.StatusExternal:
				c.lastPID = s.PID
			}
			if s.AgentSessionID != "" {
				candidates[s.AgentSessionID] = c
			}
		}
	})
	for _, name := range names {
		tmux.Stop(name)
	}
	if len(candidates) == 0 {
		return stopped, false
	}

	live := claude.Running()
	for id, c := range candidates {
		l, ok := live[id]
		if !ok && claude.ProcessAlive(c.lastPID) {
			l, ok = claude.Live{PID: c.lastPID}, true
		}
		if !ok {
			continue
		}
		if !l.Background() && !c.paneStopped {
			e.logf(t, "Session %s still runs in another terminal (pid %d)", claude.ShortID(id), l.PID)
			e.notify("#%d still runs in another terminal (pid %d); exit it there", t.Number, l.PID)
			leftRunning = true
			continue
		}
		if err := claude.Terminate(l.PID); err != nil {
			e.logf(t, "Stopping session %s failed: %v", claude.ShortID(id), err)
			e.notify("#%d: stopping its agent (pid %d) failed: %v", t.Number, l.PID, err)
			leftRunning = true
			continue
		}
		if !c.paneStopped {
			e.with(func() { markStopped(c.s) })
			e.logf(t, "Stopped background session %s", claude.ShortID(id))
		}
	}
	return stopped, leftRunning
}

// activate makes sure the agent for col is running in the task's worktree:
// an existing session is kept (or respawned when the model changes), a missing
// one is resumed or started, then the enter group runs. card is the card being
// moved; the session belongs to its group's owner and now follows card. evict
// lets a start you asked for suspend the longest-idle agent when every slot is
// taken.
func (e *Engine) activate(card *store.Task, col, from config.Column, message string, runEnter, evict bool) error {
	e.setBusy(card.ID, "starting agent")
	defer e.setBusy(card.ID, "")
	e.mu.Lock()
	t := e.owner(card)
	scratch := scratchSession(t)
	e.mu.Unlock()
	worktree := e.Root
	var relocation store.Pending
	if scratch == nil || !col.IsTodo() {
		var err error
		if worktree, err = e.ensureWorktree(t, card.ID); err != nil {
			e.logf(card, "Worktree failed: %v", err)
			e.notify("#%d: %v", card.Number, err)
			return err
		}
		if scratch != nil {
			if p, ok := e.relocateLive(t, scratch, worktree); ok {
				relocation = p
			} else {
				e.stopSessions(t, "", store.StatusSuspended)
				e.adoptScratch(t, scratch, worktree)
			}
		}
	}
	e.with(func() { t.ActiveCard = card.ID })
	key := store.MainSession
	e.stopSessions(t, key, store.StatusSuspended)

	e.mu.Lock()
	sess := t.Sessions[key]
	live := sess != nil && sess.Status == store.StatusRunning && tmux.Has(sess.Tmux)
	queued := sess != nil && sess.Status == store.StatusQueued
	started := sess != nil && sess.Started
	mode := e.board.PermissionMode(col)
	changed := live && settingsDiffer(sess, col, mode)
	busy := changed && sess.Activity != store.ActivityIdle && sess.Activity != store.ActivityStarting
	e.mu.Unlock()
	if busy {
		e.with(func() { sess.RespawnPending = true })
		e.logf(t, "Agent switches to %s's settings when its current turn ends", col.Name)
	}

	enter := col.Automations.OnEnter
	if !runEnter {
		enter = nil
	}
	gc := groupContext{from: from, to: col}

	switch {
	case live && changed && !busy:
		restarted, err := e.switchSettings(t, key, col, worktree)
		if err != nil {
			return err
		}
		if restarted {
			relocation = store.Pending{}
		}
	case queued && evict:
		e.startQueuedNow(t, key)
	case live || queued:
	default:
		fresh := !started
		prompt := ""
		if fresh {
			prompt, enter = e.initialPrompt(card, enter, gc)
			if message != "" {
				prompt += "\n\n" + message
				message = ""
			}
		}
		if err := e.spawn(t, col, key, worktree, fresh, prompt, evict); err != nil {
			return err
		}
	}
	if relocation.Text != "" {
		e.sendPending(t, key, relocation)
	}
	if message != "" {
		e.sendMessage(t, key, message)
	}
	e.runEnterGroup(card, enter, gc)
	return nil
}

// initialPrompt is the first message of a fresh session. A column whose enter
// group opens with a message (like `/code-review`) starts with that message
// instead of the task prompt; the row is consumed.
func (e *Engine) initialPrompt(t *store.Task, enter []config.Automation, gc groupContext) (string, []config.Automation) {
	if len(enter) > 0 && enter[0].Type == "send_message" && enter[0].Condition == "" {
		row := enter[0]
		e.logf(t, "Automation %q: started agent with message", rowName(row))
		return e.render(t, row.Message, gc, false), enter[1:]
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if strings.TrimSpace(t.Prompt) == "" {
		return t.Title, enter
	}
	if strings.HasPrefix(t.Prompt, t.Title) {
		return t.Prompt, enter
	}
	return t.Title + "\n\n" + t.Prompt, enter
}

// limit is how many agents may run at once. Called with mu held.
func (e *Engine) limit() int { return e.board.Limit() }

func (e *Engine) runningCount() int {
	n := 0
	for _, t := range e.state.Tasks {
		for _, s := range t.Sessions {
			if s.Status == store.StatusRunning {
				n++
			}
		}
	}
	return n
}

// freeSlot suspends the agent on another task that has been idle longest, to
// make room for a start you asked for. It returns that agent's tmux session to
// stop, or "" when no agent is idle. Called with mu held.
func (e *Engine) freeSlot(t *store.Task) string {
	var victim *store.Session
	var owner *store.Task
	for _, o := range e.state.Tasks {
		if o == t {
			continue
		}
		for _, s := range o.Sessions {
			if !idle(s) {
				continue
			}
			if victim == nil || s.UpdatedAt.Before(victim.UpdatedAt) {
				victim, owner = s, o
			}
		}
	}
	if victim == nil {
		return ""
	}
	victim.Status = store.StatusSuspended
	victim.RespawnPending = false
	owner.AddLog(fmt.Sprintf("Agent suspended to free a slot for #%d", t.Number))
	t.AddLog(fmt.Sprintf("Suspended #%d (idle longest) to free a slot", owner.Number))
	e.notify("#%d suspended to free a slot for #%d", owner.Number, t.Number)
	return victim.Tmux
}

// startQueuedNow starts a queued session right away, suspending an idle agent
// if every slot is taken. The caller holds the task lock.
func (e *Engine) startQueuedNow(t *store.Task, key string) {
	stop, name := "", ""
	start := false
	e.with(func() {
		s := t.Sessions[key]
		if s == nil || s.Status != store.StatusQueued {
			return
		}
		if e.runningCount() >= e.limit() {
			stop = e.freeSlot(t)
		}
		if e.runningCount() >= e.limit() {
			return
		}
		start = true
		s.Status = store.StatusRunning
		s.Activity = store.ActivityStarting
		s.UpdatedAt = time.Now()
		e.launching.Store(s.Tmux, true)
		name = s.Tmux
	})
	if stop != "" {
		tmux.Stop(stop)
	}
	if start {
		defer e.launching.Delete(name)
		e.launchQueued(t, key)
	}
}

// spawn starts the agent in tmux, or queues it when the concurrency limit is
// reached; with evict it first tries to suspend an idle agent. The caller
// holds the task lock.
func (e *Engine) spawn(t *store.Task, col config.Column, key, worktree string, fresh bool, prompt string, evict bool) error {
	dir := e.sessionDir(t, key)
	events := filepath.Join(dir, "events.jsonl")
	settings := filepath.Join(dir, "settings.json")
	if err := claude.WriteSettings(settings, e.exe, events); err != nil {
		return err
	}

	if !fresh && worktree != e.Root {
		e.carryTranscript(t, key, worktree)
	}

	var launch claude.Launch
	queue := false
	stop := ""
	e.with(func() {
		if t.Sessions == nil {
			t.Sessions = map[string]*store.Session{}
		}
		s := t.Sessions[key]
		if s == nil {
			s = &store.Session{Key: key, Tmux: e.tmuxName(t), Dir: dir}
			t.Sessions[key] = s
		}
		if fresh {
			s.AgentSessionID = uuid.NewString()
			s.Started = false
			s.LastReply = ""
			s.EventsOffset = 0
			os.Remove(events)
		} else if info, err := os.Stat(events); err == nil {
			// Skip events the previous process wrote while shutting down.
			s.EventsOffset = info.Size()
		}
		s.Model, s.Effort = col.ModelOverride, col.EffortOverride
		s.SetMode(e.board.PermissionMode(col))
		s.RespawnPending = false
		s.Pending = slices.DeleteFunc(s.Pending, func(p store.Pending) bool { return p.Relocate != "" })
		s.Relocate = ""
		s.UpdatedAt = time.Now()
		launch = claude.Launch{
			SettingsPath:   settings,
			SessionID:      s.AgentSessionID,
			Resume:         !fresh,
			PermissionMode: s.PermissionMode,
			Model:          col.ModelOverride,
			Effort:         col.EffortOverride,
			Prompt:         prompt,
		}
		if evict && e.runningCount() >= e.limit() {
			stop = e.freeSlot(t)
		}
		if e.runningCount() >= e.limit() {
			queue = true
			s.Status = store.StatusQueued
			s.Activity = store.ActivityIdle
			if prompt != "" {
				// The prompt waits with the queued session and is typed in
				// once a slot frees up and the agent is ready.
				s.Pending = append([]store.Pending{{Text: prompt}}, s.Pending...)
			}
			t.AddLog("Agent queued: concurrency limit reached")
			e.notify("#%d queued: %d agents running (limit %d). Suspend one with S to free a slot", t.Number, e.runningCount(), e.limit())
			return
		}
		s.Status = store.StatusRunning
		s.Activity = store.ActivityStarting
		if prompt != "" {
			s.Activity = store.ActivityThinking
			s.TurnInVisit = true
		}
	})
	if stop != "" {
		tmux.Stop(stop)
	}
	if queue {
		return nil
	}
	return e.startTmux(t, key, worktree, launch)
}

func (e *Engine) startTmux(t *store.Task, key, worktree string, launch claude.Launch) error {
	e.mu.Lock()
	s := t.Sessions[key]
	name := s.Tmux
	env := map[string]string{
		"DROVER_TASK_ID":     t.ID,
		"DROVER_TASK_NUMBER": fmt.Sprint(t.Number),
		"DROVER_TASK_TITLE":  t.Title,
	}
	e.mu.Unlock()
	tmux.Kill(name)
	if err := tmux.NewSession(name, worktree, launch.Argv(), env); err != nil {
		e.with(func() {
			s.Status = store.StatusExited
			s.Activity = store.ActivityIdle
			t.AddLog(fmt.Sprintf("Starting agent failed: %v", err))
		})
		e.notify("#%d: starting agent failed: %v", t.Number, err)
		return err
	}
	verb := "Started"
	if launch.Resume {
		verb = "Resumed"
	}
	e.logf(t, "%s agent (%s)", verb, describeLaunch(launch))
	e.syncIssueStarted(t)
	return nil
}

func describeLaunch(l claude.Launch) string {
	parts := []string{"claude"}
	for _, p := range []string{l.PermissionMode, l.Model, l.Effort} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// sendMessage delivers text to the agent now if it is idle; otherwise it is
// queued until the turn ends.
func (e *Engine) sendMessage(t *store.Task, key, text string) {
	e.sendPending(t, key, store.Pending{Text: text})
}

// sendPending is sendMessage for a queued message. Typing a relocation
// request starts watching for the move; its reply is no turn of the column's.
func (e *Engine) sendPending(t *store.Task, key string, p store.Pending) {
	e.mu.Lock()
	s := t.Sessions[key]
	if s == nil {
		e.mu.Unlock()
		return
	}
	ready := s.Status == store.StatusRunning && s.Activity == store.ActivityIdle
	name := s.Tmux
	if !ready {
		s.Pending = append(s.Pending, p)
		e.mu.Unlock()
		return
	}
	// Claim the turn before typing so a concurrent sender queues instead.
	s.Activity = store.ActivityThinking
	s.TurnInVisit = p.Relocate == ""
	if p.Relocate != "" {
		s.Relocate = p.Relocate
	}
	s.UpdatedAt = time.Now()
	e.mu.Unlock()

	defer e.lockTyping(name)()
	err := tmux.SendText(name, p.Text)
	e.with(func() {
		s.UpdatedAt = time.Now()
		if err != nil {
			s.Activity = store.ActivityIdle
			s.Relocate = ""
			t.AddLog(fmt.Sprintf("Sending message failed: %v", err))
		}
	})
}

// lockTyping serializes typing into the tmux session name.
func (e *Engine) lockTyping(name string) func() {
	typing, _ := e.typing.LoadOrStore(name, &sync.Mutex{})
	typing.(*sync.Mutex).Lock()
	return typing.(*sync.Mutex).Unlock
}

// Send is the user typing a message for the task's current agent.
func (e *Engine) Send(id, text string) error {
	card, t, unlock := e.lockGroup(id)
	defer unlock()
	e.mu.Lock()
	if card != nil {
		if s := t.Sessions[store.MainSession]; s == nil || (s.Status != store.StatusRunning && s.Status != store.StatusQueued) {
			t = nil
		}
	}
	e.mu.Unlock()
	if t == nil {
		return errors.New("no running agent for this task")
	}
	e.sendMessage(t, store.MainSession, text)
	return nil
}

// LiveSession returns the tmux session of the task's current agent, which
// every card of a group shares.
func (e *Engine) LiveSession(id string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	card := e.find(id)
	if card == nil {
		return "", false
	}
	return e.liveSession(card)
}

// liveSession is LiveSession for a card. Called with mu held.
func (e *Engine) liveSession(card *store.Task) (string, bool) {
	t := e.owner(card)
	if s := t.Sessions[store.MainSession]; s != nil && s.Status == store.StatusRunning {
		return s.Tmux, true
	}
	for _, s := range t.Sessions {
		if s.Status == store.StatusRunning {
			return s.Tmux, true
		}
	}
	return "", false
}

// ExternalAttach returns a command that opens the task's agent where it runs
// outside the board, without stopping it: a Claude Code background session,
// or a pane on your own tmux server. It reports false for an agent in a plain
// terminal, which can only be taken over.
func (e *Engine) ExternalAttach(id string) (*exec.Cmd, bool) {
	var owner *store.Task
	e.mu.Lock()
	if card := e.find(id); card != nil {
		owner = e.owner(card)
	}
	e.mu.Unlock()
	if owner == nil {
		return nil, false
	}
	l, ok := e.externalLive(owner)
	if !ok {
		return nil, false
	}
	if l.Background() {
		return exec.Command("claude", "attach", l.SessionID), true
	}
	if pane, ok := tmux.PaneOf(l.PID); ok {
		return tmux.AttachPaneCommand(pane), true
	}
	return nil, false
}
