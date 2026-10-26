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

// ErrDirty is returned when archiving a card would delete uncommitted work.
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

	focused            string
	alerted            map[string]time.Time
	idleStopping       sync.Map
	backgroundScanning sync.Map
}

// Executable is the path of the running drover binary, symlinks resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func New(root string) (*Engine, error) {
	exe, err := Executable()
	if err != nil {
		return nil, err
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
	e.migrateTodo()
	return e, nil
}

// Exe is the drover binary that agents and the assistant call back into.
func (e *Engine) Exe() string { return e.exe }

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
	e.migrateTodo()
	e.notify("Reloaded drover.json")
}

// migrateTodo moves the cards still in a removed To Do column to the first
// column. Its conversation is resumed from the first column.
func (e *Engine) migrateTodo() {
	e.mu.Lock()
	legacy := e.board.LegacyTodo
	first := e.board.FirstColumn()
	var ids []string
	for _, t := range e.state.Tasks {
		if slices.Contains(legacy, t.ColumnID) {
			ids = append(ids, t.ID)
		}
	}
	e.mu.Unlock()
	moved := 0
	for _, id := range ids {
		t, owner, unlock := e.lockGroup(id)
		e.mu.Lock()
		stale := t != nil && slices.Contains(legacy, t.ColumnID)
		drives := stale && t == owner && store.Driver(e.state.Tasks, owner) == t
		e.mu.Unlock()
		if stale {
			moved++
			drop := false
			if drives {
				_, leftRunning := e.stopSessions(owner, "", store.StatusSuspended)
				drop = !leftRunning
			}
			e.with(func() {
				if drop {
					e.dropUnstarted(t)
				}
				t.AddLog(fmt.Sprintf("Moved to %s: the To Do column was removed", first.Name))
				t.ColumnID = first.ID
			})
		}
		unlock()
	}
	if moved > 0 {
		e.notify("The To Do column was removed: moved %d cards to %s", moved, first.Name)
	}
}

// dropUnstarted forgets t's sessions that never recorded a conversation, so
// the card reads as not started. Called with mu held.
func (e *Engine) dropUnstarted(t *store.Task) {
	for key, s := range t.Sessions {
		if s.Status != store.StatusExternal && !e.talkedTo(t, key) {
			delete(t.Sessions, key)
		}
	}
}

// talkedTo reports whether s got a prompt, counting the one whose hook event
// the board has not read yet. An unreadable event log counts as talked to, so
// nothing is thrown away on a guess. Called with mu held.
func (e *Engine) talkedTo(t *store.Task, key string) bool {
	s := t.Sessions[key]
	if s.Started {
		return true
	}
	events, _, err := claude.ReadEvents(filepath.Join(e.sessionDir(t, key), "events.jsonl"), s.EventsOffset)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(events, func(ev claude.Event) bool { return ev.Name == "UserPromptSubmit" })
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
			ColumnID:  e.board.FirstColumn().ID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		e.state.NextNumber++
		task.AddLog("Created")
		e.state.Tasks = append(e.state.Tasks, task)
	})
	return task
}

// NewSession adds a card in the first column and starts its agent right away.
func (e *Engine) NewSession() (*store.Task, error) {
	col := e.firstColumn()
	if !col.IsAgent() {
		return nil, fmt.Errorf("%s, the first column, runs no agent; make it an agent column in drover.json to start sessions with n", col.Name)
	}
	task := e.CreateTask("", "")
	_, owner, unlock := e.lockGroup(task.ID)
	defer unlock()
	if err := e.activate(task, col, col, "", true, true); err != nil {
		return task, errors.Join(fmt.Errorf("starting session: %w", err), e.discardCard(task, owner))
	}
	return task, nil
}

// discardCard deletes a card that was never used, with its worktree and its
// branch, and tells you when the branch stays. A worktree whose setup failed
// before it was recorded on the card is removed too. The caller holds the
// group lock.
func (e *Engine) discardCard(t, owner *store.Task) error {
	e.mu.Lock()
	branch, unrecorded := t.Branch, ""
	if t.WorktreePath == "" && e.isRepo {
		folder := filepath.Join(e.board.WorktreesPath(e.Root), fmt.Sprintf("%s%d", worktreeFolderPrefix, t.Number))
		if _, err := os.Stat(filepath.Join(folder, ".git")); err == nil {
			unrecorded, branch = folder, gitx.BranchName(t.Title, t.ID)
		}
	}
	e.mu.Unlock()
	if err := e.deleteCard(t, owner); err != nil {
		return err
	}
	if unrecorded != "" {
		gitx.RemoveWorktree(e.Root, unrecorded)
	}
	if branch != "" {
		if err := gitx.DeleteBranch(e.Root, branch); err != nil {
			e.notify("couldn't delete the unused branch %s: %v", branch, err)
		}
	}
	return nil
}

// DiscardSilent deletes a new card whose agent never got a message, and stops
// the agent, its worktree and its unused branch. A card that is titled, has a
// prompt or an issue, has left the first column, or has a started conversation
// is kept.
func (e *Engine) DiscardSilent(id string) error {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return nil
	}
	e.mu.Lock()
	s := t.Sessions[store.MainSession]
	silent := s != nil && t.Title == "" && t.Prompt == "" && t.Issue == nil && t.ColumnID == e.board.FirstColumn().ID && !e.talkedTo(t, store.MainSession)
	e.mu.Unlock()
	if !silent {
		return nil
	}
	return e.discardCard(t, owner)
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
	return e.deleteCard(t, owner)
}

// deleteCard is DeleteTask once the card's group is locked.
func (e *Engine) deleteCard(t, owner *store.Task) error {
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
			if x.ID == t.ID {
				e.state.Tasks = append(e.state.Tasks[:i], e.state.Tasks[i+1:]...)
				break
			}
		}
	})
	if len(members) > 1 {
		heir := owner
		if t == owner {
			heir = members[1]
		}
		return e.releaseIdle(heir)
	}
	return nil
}

// ArchiveTask hides a card in Done from the board, or shows it again.
// Archiving removes the worktree once every card sharing it is archived; a
// dirty one is kept and ErrDirty returned unless force is set.
func (e *Engine) ArchiveTask(id string, archived, force bool) error {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return errors.New("task not found")
	}
	e.mu.Lock()
	col, _, _ := e.board.Column(t.ColumnID)
	e.mu.Unlock()
	if !col.IsDone() {
		return fmt.Errorf("#%d is not in Done", t.Number)
	}
	if archived {
		if err := e.freeWorktree(owner, t, force); err != nil {
			return err
		}
	}
	e.with(func() {
		t.Archived = archived
		if archived {
			t.AddLog("Archived")
		} else {
			t.UpdatedAt = time.Now()
			t.AddLog("Restored from the archive")
		}
	})
	return nil
}

// ArchiveColumn archives every card in a Done column and returns how many
// were. Worktrees with uncommitted changes are kept.
func (e *Engine) ArchiveColumn(colID string) int {
	n := 0
	var owners []string
	e.with(func() {
		if col, _, _ := e.board.Column(colID); !col.IsDone() {
			return
		}
		for _, t := range e.state.Tasks {
			if t.ColumnID == colID && !t.Archived {
				t.Archived = true
				t.AddLog("Archived")
				owners = append(owners, e.owner(t).ID)
				n++
			}
		}
	})
	e.releaseArchived(owners)
	return n
}

// archiveStale archives cards that have sat in Done for archiveDoneAfter and
// tells you, as they vanish from the board. The returned follow-up removes
// their worktrees; it is nil when nothing was archived.
func (e *Engine) archiveStale(now time.Time) func() {
	after := e.board.ArchiveAfter()
	if after == 0 {
		return nil
	}
	var owners []string
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
			owners = append(owners, e.owner(t).ID)
		}
	}
	if len(owners) == 0 {
		return nil
	}
	e.notify("Archived %d cards after %s in Done. v shows them.", len(owners), e.board.ArchiveDoneAfter)
	return func() { e.releaseArchived(owners) }
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
// shares its session with other cards only stops the agent once no card in the
// group needs it.
func (e *Engine) moveLocked(id, targetID string, opts MoveOptions) error {
	e.mu.Lock()
	t := e.find(id)
	board := e.board
	var owner *store.Task
	var from config.Column
	othersActive, othersEnded := false, true
	var nextDriver string
	if t != nil {
		owner = e.owner(t)
		ownerRunning := live(owner.Sessions[store.MainSession])
		from, _, _ = board.Column(t.ColumnID)
		for _, m := range store.Members(e.state.Tasks, owner) {
			if m == t {
				continue
			}
			col, _, _ := board.Column(m.ColumnID)
			if isAgentColumn(col) || col.KeepAgent && ownerRunning {
				othersActive = true
				if nextDriver == "" || isAgentColumn(col) {
					nextDriver = m.ID
				}
			}
			othersEnded = othersEnded && col.IsDone()
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
	ending := target.IsDone() && othersEnded
	external := e.externalCheck(owner)
	if external != nil && ending {
		return external
	}
	e.mu.Lock()
	drives := isAgentColumn(target) || store.Driver(e.state.Tasks, owner) == t
	sess := owner.Sessions[store.MainSession]
	keeps := live(sess) && target.KeepAgent && drives
	e.mu.Unlock()

	e.with(func() {
		t.ColumnID = target.ID
		t.UpdatedAt = time.Now()
		if opts.evict {
			t.PRMovePending = false
		}
		if t.PR != nil {
			t.PR.MergeMovePending = false
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

	if !isAgentColumn(target) && !target.IsDone() && !opts.skipEnter {
		defer e.runEnterGroup(t, e.withoutAgent(t, target.Automations.OnEnter), groupContext{from: from, to: target})
	}
	switch {
	case !isAgentColumn(target) && othersActive:
		if drives {
			e.with(func() { owner.ActiveCard = nextDriver })
		}
		return nil
	case keeps:
		return nil
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

// Start resumes or starts the agent for a task in place. A card that never had
// an agent runs its agent column's enter automations, as entering it would;
// otherwise they are not run again.
func (e *Engine) Start(id string) error {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return errors.New("task not found")
	}
	if err := e.externalCheck(owner); err != nil {
		return err
	}
	col, err := e.startColumn(t)
	if err != nil {
		return err
	}
	e.mu.Lock()
	entering := len(owner.Sessions) == 0 && col.IsAgent()
	e.mu.Unlock()
	return e.activate(t, col, col, "", entering, true)
}

// TakeOver kills the process running the task's conversation in another
// terminal and resumes the conversation on the board.
func (e *Engine) TakeOver(id string) error {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return errors.New("task not found")
	}
	col, err := e.startColumn(t)
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
// card is in Done.
func (e *Engine) startColumn(t *store.Task) (config.Column, error) {
	e.mu.Lock()
	col, _, _ := e.board.Column(t.ColumnID)
	e.mu.Unlock()
	if col.IsDone() {
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

func (e *Engine) tmuxName(t *store.Task) string {
	project := tmux.SafeName(e.ProjectName)
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

// moveConversation moves the transcript at path into worktree's project and
// logs how that went.
func (e *Engine) moveConversation(t *store.Task, path, worktree string) {
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
}

// carryTranscript moves a conversation still recorded under the main checkout,
// such as one from before the To Do column was removed, next to worktree so
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
// can and by resuming it in a new process otherwise.
func (e *Engine) switchSettings(t *store.Task, key string, col config.Column, worktree string) error {
	if e.retune(t, key, col) {
		return nil
	}
	e.mu.Lock()
	name := t.Sessions[key].Tmux
	e.mu.Unlock()
	tmux.Stop(name)
	return e.spawn(t, col, key, worktree, false, "", false)
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

// freeWorktree removes owner's worktree once every card sharing it is
// archived; pending counts as archived. A dirty worktree is kept unless force
// is set, and ErrDirty is returned.
func (e *Engine) freeWorktree(owner, pending *store.Task, force bool) error {
	e.mu.Lock()
	idle := true
	for _, m := range store.Members(e.state.Tasks, owner) {
		idle = idle && (m.Archived || m == pending)
	}
	path, copied := owner.WorktreePath, e.board.CopyFiles
	e.mu.Unlock()
	if !idle || path == "" || !e.isRepo || !e.ownsWorktree(path) {
		return nil
	}
	if !force && gitx.Dirty(path, copied...) {
		return ErrDirty
	}
	return e.removeWorktree(owner)
}

// releaseIdle removes owner's worktree if no card sharing it needs it any
// more, as freeWorktree decides. One with uncommitted changes is left alone.
func (e *Engine) releaseIdle(owner *store.Task) error {
	err := e.freeWorktree(owner, nil, false)
	if errors.Is(err, ErrDirty) {
		e.logf(owner, "Worktree kept: it has uncommitted changes")
		return nil
	}
	return err
}

// releaseArchived frees the worktrees of the owners, by id, whose cards were
// just archived. Dirty ones are kept, and you are told how many were kept or
// could not be removed.
func (e *Engine) releaseArchived(ownerIDs []string) {
	seen := map[string]bool{}
	kept, failed := 0, 0
	for _, id := range ownerIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		owner, _, unlock := e.lockGroup(id)
		var err error
		if owner != nil {
			err = e.freeWorktree(owner, nil, false)
		}
		unlock()
		switch {
		case errors.Is(err, ErrDirty):
			kept++
			e.logf(owner, "Worktree kept: it has uncommitted changes")
		case err != nil:
			failed++
		}
	}
	if kept > 0 {
		e.notify("Kept %d worktrees with uncommitted changes. Restore the card and press A to delete one.", kept)
	}
	if failed > 0 {
		e.notify("%d worktrees of archived cards could not be removed; see the card logs.", failed)
	}
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
	e.mu.Unlock()
	worktree, err := e.ensureWorktree(t, card.ID)
	if err != nil {
		e.logf(card, "Worktree failed: %v", err)
		e.notify("#%d: %v", card.Number, err)
		return err
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
	interrupt := busy && mode == "plan" && sess.PermissionMode != "plan" && !sess.RespawnPending
	name := ""
	if busy {
		name = sess.Tmux
		sess.RespawnPending = true
	}
	e.mu.Unlock()
	if busy {
		if interrupt {
			unlock := e.lockTyping(name)
			err := tmux.SendKey(name, "Escape")
			unlock()
			if err != nil {
				e.logf(t, "Couldn't interrupt the turn to switch to %s's settings: %v", col.Name, err)
			} else {
				e.logf(t, "Interrupting the turn to switch to %s's settings", col.Name)
			}
		} else {
			e.logf(t, "Agent switches to %s's settings when its current turn ends", col.Name)
		}
	}

	enter := col.Automations.OnEnter
	if !runEnter {
		enter = nil
	}
	gc := groupContext{from: from, to: col}

	switch {
	case live && changed && !busy:
		if err := e.switchSettings(t, key, col, worktree); err != nil {
			return err
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
	if len(enter) > 0 && enter[0].Type == "send_message" && !enter[0].Conditional() {
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

func (e *Engine) runningCount() int { return e.runningCountExcept(nil) }

// runningCountExcept is runningCount without skip, the session spawn is about
// to start in its own slot. Called with mu held.
func (e *Engine) runningCountExcept(skip *store.Session) int {
	n := 0
	for _, t := range e.state.Tasks {
		for _, s := range t.Sessions {
			if s.Status == store.StatusRunning && s != skip {
				n++
			}
		}
	}
	return n
}

// freeSlot suspends the agent on another task that has been idle longest, to
// make room for a start you asked for. It returns that agent's tmux session to
// stop, or "" when no agent is idle. An agent in a keepAgent column is never
// chosen. Called with mu held.
func (e *Engine) freeSlot(t *store.Task) string {
	var victim *store.Session
	var owner *store.Task
	for _, o := range e.state.Tasks {
		if o == t || e.keepsAgent(o) {
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
			e.notifyQueued(t)
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

func (e *Engine) notifyQueued(t *store.Task) {
	e.notify("#%d queued: %d agents running (limit %d). Suspend one with S to free a slot", t.Number, e.runningCount(), e.limit())
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
			s.Summary = ""
			s.EventsOffset = 0
			os.Remove(events)
		} else if info, err := os.Stat(events); err == nil {
			// Skip events the previous process wrote while shutting down.
			s.EventsOffset = info.Size()
		}
		s.Model, s.Effort = col.ModelOverride, col.EffortOverride
		s.SetMode(e.board.PermissionMode(col))
		s.RespawnPending = false
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
		if evict && e.runningCountExcept(s) >= e.limit() {
			stop = e.freeSlot(t)
		}
		if e.runningCountExcept(s) >= e.limit() {
			queue = true
			s.Status = store.StatusQueued
			s.Activity = store.ActivityIdle
			if prompt != "" {
				// The prompt waits with the queued session and is typed in
				// once a slot frees up and the agent is ready.
				s.Pending = append([]store.Pending{{Text: prompt}}, s.Pending...)
			}
			t.AddLog("Agent queued: concurrency limit reached")
			e.notifyQueued(t)
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
	launch.SystemPrompt = claude.BoardPrompt(e.exe)
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

// sendPending is sendMessage for a queued message.
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
	s.TurnInVisit = true
	s.UpdatedAt = time.Now()
	e.mu.Unlock()

	defer e.lockTyping(name)()
	err := tmux.SendText(name, p.Text)
	e.with(func() {
		s.UpdatedAt = time.Now()
		if err != nil {
			s.Activity = store.ActivityIdle
			s.TurnInVisit = false
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
