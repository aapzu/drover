package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/inbox"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

// Run polls hook events and tmux, and applies the CLI's requests, until ctx is
// done, then waits for the work it started. Automations only run while the
// TUI is open; agents keep working in tmux when it is closed.
func (e *Engine) Run(ctx context.Context) {
	if release, ok := inbox.Hold(e.Root); ok {
		defer release()
		inbox.Recover(e.Root)
		go e.serveInbox(ctx)
	} else {
		e.notify("Another drover is open for this project and applies the CLI's changes")
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.stop()
			e.work.Wait()
			return
		case <-ticker.C:
			e.Tick()
		}
	}
}

func (e *Engine) Tick() {
	e.reloadBoard()
	live := tmux.List()
	var followUps []func()

	e.with(func() {
		for _, t := range e.state.Tasks {
			if t.PRMovePending && !agentRunning(t) {
				followUps = append(followUps, func() { e.shipTask(t) })
			}
			for key, s := range t.Sessions {
				if s.Status != store.StatusRunning {
					continue
				}
				followUps = append(followUps, e.consumeEvents(t, key, s)...)
				if _, launching := e.launching.Load(s.Tmux); launching {
					continue
				}
				if !live[s.Tmux] && time.Since(s.UpdatedAt) > 3*time.Second && !tmux.Has(s.Tmux) {
					s.Status = store.StatusExited
					s.Activity = store.ActivityIdle
					t.AddLog("Agent exited")
					e.alert(t, alertExited, "agent exited")
				}
			}
		}
		followUps = append(followUps, e.idleTimeouts()...)
		followUps = append(followUps, e.checkInterrupted()...)
		followUps = append(followUps, e.backgroundScans()...)
		followUps = append(followUps, e.dequeue()...)
		if time.Since(e.lastPoll) > 1500*time.Millisecond {
			e.lastPoll = time.Now()
			followUps = append(followUps, e.pollExternal()...)
		}
		followUps = append(followUps, e.pollPRs()...)
		if fn := e.archiveStale(time.Now()); fn != nil {
			followUps = append(followUps, fn)
		}
	})

	for _, fn := range followUps {
		e.work.Go(fn)
	}
}

// consumeEvents applies new hook events to the session's activity and returns
// work to run outside the state lock. Called with mu held.
func (e *Engine) consumeEvents(t *store.Task, key string, s *store.Session) []func() {
	events, offset, err := claude.ReadEvents(filepath.Join(e.sessionDir(t, key), "events.jsonl"), s.EventsOffset)
	if err != nil || len(events) == 0 {
		return nil
	}
	s.EventsOffset = offset
	s.UpdatedAt = time.Now()
	taskID, visit := t.ID, t.Visit
	var out []func()
	for _, ev := range events {
		if ev.PermissionMode != "" {
			s.PermissionMode = ev.PermissionMode
		}
		switch ev.Name {
		case "SessionStart":
			if ev.Source == "startup" && s.Started && ev.SessionID != s.AgentSessionID {
				// A blank conversation opened in the pane after the agent was
				// sent to the background; follow the background session.
				if s.Forked {
					return append(out, e.followBackground(t, s))
				}
				continue
			}
			s.Forked = ev.Source == "fork"
			if ev.Source == "startup" || ev.Source == "resume" {
				s.Background = nil
			}
			// /clear, /compact and forks continue under a new id; resume that one.
			if ev.SessionID != "" && ev.SessionID != s.AgentSessionID {
				s.AgentSessionID = ev.SessionID
				s.PRScan = 0
			}
			// /clear starts a new conversation without a Stop event.
			if s.Activity == store.ActivityStarting || ev.Source == "clear" {
				s.Activity = store.ActivityIdle
				s.TurnInVisit = false
				s.TurnEndRow = ""
				out = append(out, func() { e.flushPending(e.taskByID(taskID), key) })
			}
		case "UserPromptSubmit":
			s.Forked = false
			s.Activity = store.ActivityThinking
			s.Started = true
			s.TurnInVisit = true
			prompt := strings.TrimSpace(ev.Prompt)
			typed := prompt != "" && !strings.HasPrefix(prompt, "/")
			if t.Title == "" && typed {
				t.Title, t.Prompt = claude.FirstLine(prompt), prompt
				if e.board.AutoName {
					placeholder := t.Title
					out = append(out, func() { e.autoName(taskID, prompt, placeholder) })
				}
			}
			if f := e.board.PRFlow; f != nil && f.Requested != "" && claude.AsksForPR(ev.Prompt) {
				out = append(out, func() { e.prRequested(taskID, visit) })
			}
			if urls := claude.ReviewedPRs(ev.Prompt); len(urls) > 0 {
				e.addPRs(t, urls, true)
			}
		case "PreToolUse":
			s.Activity = store.ActivityThinking
		case "PostToolUse":
			s.Activity = store.ActivityThinking
			if id, ok := ev.BackgroundAgent(); ok {
				s.Background = append(s.Background, id)
			}
			if ev.ToolName == "ExitPlanMode" {
				out = append(out, func() { e.planExit(taskID, visit) })
			}
			if urls := ev.CreatedPRs(); len(urls) > 0 {
				e.addPRs(t, urls, false)
			}
		case "PermissionRequest":
			s.Activity = store.ActivityPermission
			if ev.ToolName == "ExitPlanMode" {
				e.alert(t, alertPlanReady, "has a plan ready for review")
				out = append(out, func() { e.prebuildWorktree(taskID, visit) })
			} else {
				e.alert(t, alertNeedsYou, "needs you: permission requested")
			}
		case "Notification":
			if ev.NotificationType == "permission_prompt" {
				s.Activity = store.ActivityPermission
			}
		case "Stop":
			s.Activity = store.ActivityIdle
			s.Started = true
			if ids, ok := ev.RunningBackgrounds(); ok {
				s.Background = ids
			}
			reply := ev.Reply()
			s.LastReply = reply
			s.Summary = ""
			out = append(out, func() { e.scanUsage(taskID, key) })
			out = append(out, func() { e.summarize(taskID, key, reply) })
			next := func() {
				if owner := e.taskByID(taskID); !e.shipTask(owner) {
					e.flushPending(owner, key)
				}
			}
			if s.TurnInVisit {
				s.TurnInVisit = false
				next = func() { e.runTurnEnd(taskID, key, visit, reply) }
			}
			if s.RespawnPending {
				then := next
				next = func() { e.respawnPending(taskID, key); then() }
			}
			out = append(out, func() { next(); e.alertTurnDone(taskID, key, reply) })
		}
		// Claude moves the transcript when the agent enters a worktree, so follow
		// the path of the latest event.
		if ev.TranscriptPath != "" {
			s.TranscriptPath = ev.TranscriptPath
		}
	}
	return out
}

// backgroundScans returns follow-ups that read the transcripts of sessions with
// background tasks for the completion notices of those tasks. Called with mu
// held.
func (e *Engine) backgroundScans() []func() {
	var out []func()
	for _, t := range e.state.Tasks {
		for key, s := range t.Sessions {
			if s.Status != store.StatusRunning || !s.HasBackground() {
				continue
			}
			path := e.transcriptOf(t, s)
			if path == "" {
				continue
			}
			if _, scanning := e.backgroundScanning.LoadOrStore(s, true); scanning {
				continue
			}
			taskID, offset := t.ID, s.BackgroundScan
			if s.BackgroundScanPath != path {
				offset = 0
			}
			out = append(out, func() {
				defer e.backgroundScanning.Delete(s)
				done, next, err := claude.BackgroundDone(path, offset)
				if err != nil {
					return
				}
				e.with(func() {
					if task := e.find(taskID); task != nil && task.Sessions[key] == s {
						s.Background = slices.DeleteFunc(s.Background, func(id string) bool { return slices.Contains(done, id) })
						s.BackgroundScanPath, s.BackgroundScan = path, next
					}
				})
			})
		}
	}
	return out
}

// checkInterrupted returns follow-ups that find working sessions whose turn
// you interrupted.
func (e *Engine) checkInterrupted() []func() {
	var out []func()
	for _, t := range e.state.Tasks {
		for key, s := range t.Sessions {
			working := s.Activity == store.ActivityThinking || s.Activity == store.ActivityPermission
			if s.Status != store.StatusRunning || !working {
				continue
			}
			path := e.transcriptOf(t, s)
			if path == "" {
				continue
			}
			taskID, name, offset, since := t.ID, s.Tmux, s.EventsOffset, s.UpdatedAt
			out = append(out, func() {
				if !claude.Interrupted(path, since) {
					return
				}
				var task *store.Task
				e.with(func() {
					task = e.find(taskID)
					if task == nil || task.Sessions[key] != s || s.Status != store.StatusRunning || s.EventsOffset != offset {
						task = nil
						return
					}
					if typing, ok := e.typing.Load(name); ok {
						if !typing.(*sync.Mutex).TryLock() {
							task = nil
							return
						}
						typing.(*sync.Mutex).Unlock()
					}
					s.Activity = store.ActivityIdle
					s.TurnInVisit = false
					s.TurnEndRow = ""
					task.AddLog("Turn interrupted")
				})
				if task != nil {
					e.respawnPending(taskID, key)
					e.flushPending(task, key)
				}
			})
		}
	}
	return out
}

// followBackground hands a session that moved to a background Claude Code
// process over to pollExternal, which mirrors it until it exits, and returns
// the follow-up that stops the blank pane left behind. Called with mu held.
func (e *Engine) followBackground(t *store.Task, s *store.Session) func() {
	s.Forked = false
	s.Imported = true
	s.Status = store.StatusExternal
	s.Activity = store.ActivityIdle
	s.Pending = nil
	s.Background = nil
	s.PID = 0
	if l, ok := claude.Running()[s.AgentSessionID]; ok {
		s.PID = l.PID
	}
	t.AddLog(fmt.Sprintf("Conversation moved to background session %s", claude.ShortID(s.AgentSessionID)))
	name := s.Tmux
	return func() { tmux.Stop(name) }
}

func (e *Engine) taskByID(id string) *store.Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t := e.find(id); t != nil {
		return t
	}
	return &store.Task{}
}

// planTarget returns the card and the planExitTarget column of the plan-mode
// column the task's card is in, if it is in one and visit is still current.
func (e *Engine) planTarget(taskID string, visit int) (t, card *store.Task, target config.Column, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t = e.find(taskID)
	if t == nil || t.Visit != visit {
		return nil, nil, config.Column{}, false
	}
	card = store.Driver(e.state.Tasks, t)
	col, _, _ := e.board.Column(card.ColumnID)
	target, found := e.board.Find(col.PlanExitTarget)
	ok = e.board.PermissionMode(col) == "plan" && col.PlanExitTarget != "" && found
	return t, card, target, ok
}

// prebuildWorktree builds the worktree the agent moves into once the plan is
// approved while you review the plan.
func (e *Engine) prebuildWorktree(taskID string, visit int) {
	unlock := e.lockTask(taskID)
	defer unlock()
	t, card, target, ok := e.planTarget(taskID, visit)
	if !ok || target.NoWorktree || !e.isRepo {
		return
	}
	defer e.setBusy(card.ID, "")
	if _, err := e.ensureWorktree(t, target, card.ID); err != nil {
		e.logf(card, "Prebuilding the worktree failed, retrying once the plan is approved: %v", err)
	}
}

// planExit moves the card the agent works for out of a plan-mode column once
// the agent leaves plan mode, to the column's planExitTarget.
func (e *Engine) planExit(taskID string, visit int) {
	t, _, target, ok := e.planTarget(taskID, visit)
	if !ok {
		return
	}
	// The agent is already editing in the project root, so stop it before the
	// worktree is built, without waiting for a prebuild that holds the task lock.
	var name string
	e.with(func() {
		s := t.Sessions[store.MainSession]
		if s != nil && s.Status == store.StatusRunning && s.Cwd == e.Root && !target.NoWorktree && e.isRepo {
			name = s.Tmux
		}
	})
	interrupted := name != ""
	if interrupted {
		if err := e.interrupt(name); err != nil {
			e.logf(t, "Couldn't interrupt the agent before creating its worktree: %v", err)
		}
	}
	unlock := e.lockTask(taskID)
	defer unlock()
	t, card, target, ok := e.planTarget(taskID, visit)
	if !ok {
		return
	}
	e.logf(card, "Plan approved, moving to %s", target.Name)
	if err := e.moveLocked(card.ID, target.ID, MoveOptions{}); err != nil {
		e.logf(card, "Plan-exit move failed: %v", err)
		return
	}
	// The agent keeps working in the same turn after leaving plan mode, now
	// with the target's permissions, so that turn belongs to the new visit.
	var dir string
	moved := false
	e.with(func() {
		s := t.Sessions[store.MainSession]
		if s == nil || s.Status != store.StatusRunning {
			return
		}
		s.TurnInVisit = true
		s.SetMode(e.board.PermissionMode(target))
		s.RespawnPending = false
		if dir = e.dirOf(t); s.MovedFrom(dir) {
			moved, name = true, s.Tmux
		}
	})
	if moved {
		if !interrupted {
			if err := e.interrupt(name); err != nil {
				e.logf(t, "Couldn't interrupt the agent to move it into its worktree: %v", err)
			}
		}
		if err := e.switchSettings(t, store.MainSession, target, dir); err != nil {
			e.logf(t, "Moving the agent into its worktree failed: %v", err)
			e.notify("#%d: moving the agent into its worktree failed: %v", card.Number, err)
			return
		}
	}
	if moved || interrupted {
		e.sendMessage(t, store.MainSession, "The plan is approved. Continue implementing it in your new worktree.")
	}
}

// shipTask runs shipOpenedPR under the task lock, if a move is pending.
func (e *Engine) shipTask(t *store.Task) bool {
	e.mu.Lock()
	pending := t.PRMovePending
	e.mu.Unlock()
	if !pending {
		return false
	}
	unlock := e.lockTask(t.ID)
	defer unlock()
	return e.shipOpenedPR(t)
}

// shipOpenedPR moves the card that took its session's first PR to the
// prFlow's opened column once the agent is idle, unless you moved it first.
// A PR you review goes to the reviewFlow's started column instead, once gh has
// confirmed it isn't yours. A card whose agent runs outside drover never moves.
// It reports whether it moved the card. The caller holds the task lock.
func (e *Engine) shipOpenedPR(t *store.Task) bool {
	e.mu.Lock()
	pending := t.PRMovePending
	e.mu.Unlock()
	if !pending {
		return false
	}
	var target config.Column
	reason := "PR opened"
	e.with(func() {
		col, _, _ := e.board.Column(t.ColumnID)
		review := t.PR.IsReview()
		if hasExternal(t) || !agentIdle(t) {
			return
		}
		if review && t.PR.State == "" {
			// gh hasn't said yet whether the PR is yours. A card in Done is
			// no longer polled, so it never will.
			if col.IsDone() {
				t.PRMovePending = false
			}
			return
		}
		t.PRMovePending = false
		if review {
			reason = "Review started"
		}
		target = e.flowColumn(e.startRef(review))
		switch {
		case target.ID == "" || target.ID == col.ID:
			target = config.Column{}
		case !isAgentColumn(col):
			t.AddLog(fmt.Sprintf("%s, not moving to %s: the card is in %s", reason, target.Name, col.Name))
			target = config.Column{}
		}
	})
	if target.ID == "" {
		return false
	}
	e.logf(t, "%s, moving to %s", reason, target.Name)
	if err := e.moveLocked(t.ID, target.ID, MoveOptions{}); err != nil {
		e.logf(t, "%s move failed: %v", reason, err)
		e.notify("#%d: moving to %s failed: %v", t.Number, target.Name, err)
		return false
	}
	return true
}

// prRequested moves the card the agent works for to the prFlow's requested
// column when you ask the agent to open a PR from an earlier agent column.
func (e *Engine) prRequested(taskID string, visit int) {
	unlock := e.lockTask(taskID)
	defer unlock()
	e.mu.Lock()
	t := e.find(taskID)
	if t == nil || t.Visit != visit || e.board.PRFlow == nil {
		e.mu.Unlock()
		return
	}
	card := store.Driver(e.state.Tasks, t)
	col, idx, _ := e.board.Column(card.ColumnID)
	target, ok := e.board.Find(e.board.PRFlow.Requested)
	_, targetIdx, _ := e.board.Column(target.ID)
	e.mu.Unlock()
	if !ok || !isAgentColumn(col) || idx >= targetIdx {
		return
	}
	e.logf(card, "PR requested, moving to %s", target.Name)
	if err := e.moveLocked(card.ID, target.ID, MoveOptions{skipEnter: true}); err != nil {
		e.logf(card, "PR-request move failed: %v", err)
		return
	}
	e.claimTurn(t, taskID)
}

// claimTurn makes the agent's current turn count for the visit a move just
// started. A turn that already ended had its turn end dropped as belonging to
// the previous visit, so it is run for this one; a turn still in progress is
// marked as part of the new visit. Does nothing unless the main session runs.
func (e *Engine) claimTurn(t *store.Task, taskID string) {
	e.with(func() {
		s := t.Sessions[store.MainSession]
		if s == nil || s.Status != store.StatusRunning {
			return
		}
		if s.Activity == store.ActivityIdle {
			visit, reply := t.Visit, s.LastReply
			e.work.Go(func() { e.runTurnEnd(taskID, store.MainSession, visit, reply) })
		} else {
			s.TurnInVisit = true
		}
	})
}

// respawnPending switches an agent whose card moved mid-turn into a column
// with other settings or another directory, in place or by resuming its
// conversation. A keepAgent
// column drops a pending settings switch and keeps the agent's settings.
func (e *Engine) respawnPending(taskID, key string) {
	unlock := e.lockTask(taskID)
	defer unlock()
	e.mu.Lock()
	t := e.find(taskID)
	var s *store.Session
	var col config.Column
	if t != nil {
		s = t.Sessions[key]
		col = e.sessionColumn(t)
	}
	mode := e.board.PermissionMode(col)
	worktree := e.Root
	if t != nil {
		worktree = e.dirOf(t)
	}
	due := s != nil && s.RespawnPending && s.Status == store.StatusRunning &&
		key == store.MainSession && !col.KeepAgent && settingsDiffer(s, col, mode, worktree)
	if due && waitsForBackground(s, mode) {
		e.mu.Unlock()
		return
	}
	if s != nil {
		s.RespawnPending = false
	}
	e.mu.Unlock()
	if !due {
		return
	}
	if err := e.switchSettings(t, key, col, worktree); err != nil {
		e.logf(t, "Respawn failed: %v", err)
	}
}

// waitsForBackground reports whether a due switch to mode must wait for s's
// background agents, which a restart would kill.
func waitsForBackground(s *store.Session, mode string) bool {
	return s.HasBackground() && (mode != "plan" || s.PermissionMode == "plan")
}

// dequeue starts queued sessions while there are free slots. Called with mu held.
func (e *Engine) dequeue() []func() {
	free := e.limit() - e.runningCount()
	var out []func()
	for _, t := range e.state.Tasks {
		for key, s := range t.Sessions {
			if free <= 0 {
				return out
			}
			if s.Status != store.StatusQueued {
				continue
			}
			free--
			s.Status = store.StatusRunning
			s.Activity = store.ActivityStarting
			s.UpdatedAt = time.Now()
			e.launching.Store(s.Tmux, true)
			taskID, key, name := t.ID, key, s.Tmux
			out = append(out, func() {
				defer e.launching.Delete(name)
				e.startQueued(taskID, key)
			})
		}
	}
	return out
}

func (e *Engine) startQueued(taskID, key string) {
	unlock := e.lockTask(taskID)
	defer unlock()
	e.setBusy(taskID, "starting agent")
	defer e.setBusy(taskID, "")
	e.mu.Lock()
	t := e.find(taskID)
	e.mu.Unlock()
	if t != nil {
		e.launchQueued(t, key)
	}
}

// launchQueued starts a dequeued session in tmux. The caller holds the task lock.
func (e *Engine) launchQueued(t *store.Task, key string) {
	e.mu.Lock()
	s := t.Sessions[key]
	if s == nil {
		e.mu.Unlock()
		return
	}
	col := e.sessionColumn(t)
	// The task may have moved or been stopped while waiting for its lock.
	if s.Status != store.StatusRunning || s.Activity != store.ActivityStarting || key != store.MainSession {
		e.mu.Unlock()
		return
	}
	dir := e.sessionDir(t, key)
	launch := claude.Launch{
		SettingsPath:   filepath.Join(dir, "settings.json"),
		SessionID:      s.AgentSessionID,
		Resume:         s.Started,
		PermissionMode: e.board.PermissionMode(col),
		Model:          col.ModelOverride,
		Effort:         col.EffortOverride,
	}
	s.Model, s.Effort = launch.Model, launch.Effort
	s.SetMode(launch.PermissionMode)
	e.mu.Unlock()
	worktree, err := e.ensureWorktree(t, col, t.ID)
	if err != nil {
		e.with(func() {
			s.Status = store.StatusExited
			s.Activity = store.ActivityIdle
			t.AddLog(fmt.Sprintf("Worktree failed: %v", err))
		})
		e.notify("#%d: %v", t.Number, err)
		return
	}
	if launch.Resume && worktree != e.Root {
		e.carryTranscript(t, key, worktree)
	}
	e.with(func() { s.UpdatedAt = time.Now() })
	e.startTmux(t, key, worktree, launch)
}
