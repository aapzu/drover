package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

// Run polls hook events and tmux until ctx is done. Automations only run
// while the TUI is open; agents keep working in tmux when it is closed.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
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
				}
			}
		}
		followUps = append(followUps, e.dequeue()...)
		if time.Since(e.lastPoll) > 1500*time.Millisecond {
			e.lastPoll = time.Now()
			followUps = append(followUps, e.pollExternal()...)
		}
		followUps = append(followUps, e.pollPRs()...)
	})

	for _, fn := range followUps {
		go fn()
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
	taskID, visit, number := t.ID, t.Visit, t.Number
	var out []func()
	for _, ev := range events {
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
			if ev.TranscriptPath != "" {
				s.TranscriptPath = ev.TranscriptPath
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
				out = append(out, func() { e.flushPending(e.taskByID(taskID), key) })
			}
		case "UserPromptSubmit":
			s.Forked = false
			s.Activity = store.ActivityThinking
			s.Started = true
			s.TurnInVisit = true
			if prompt := strings.TrimSpace(ev.Prompt); t.Title == "" && prompt != "" && !strings.HasPrefix(prompt, "/") {
				t.Title, t.Prompt = claude.FirstLine(prompt), prompt
			}
			if f := e.board.PRFlow; f != nil && f.Requested != "" && claude.AsksForPR(ev.Prompt) {
				out = append(out, func() { e.prRequested(taskID, visit) })
			}
		case "PreToolUse":
			s.Activity = store.ActivityThinking
		case "PostToolUse":
			s.Activity = store.ActivityThinking
			if ev.ToolName == "ExitPlanMode" {
				out = append(out, func() { e.planExit(taskID, visit) })
			}
			if urls := ev.CreatedPRs(); len(urls) > 0 {
				e.addPRs(t, urls)
			}
		case "PermissionRequest":
			s.Activity = store.ActivityPermission
			e.notify("#%d needs you: permission requested", number)
		case "Notification":
			if ev.NotificationType == "permission_prompt" {
				s.Activity = store.ActivityPermission
			}
		case "Stop":
			s.Activity = store.ActivityIdle
			s.Started = true
			if s.RespawnPending {
				out = append(out, func() { e.respawnPending(taskID, key) })
			}
			reply := ev.Reply()
			s.LastReply = reply
			if s.TurnInVisit {
				s.TurnInVisit = false
				out = append(out, func() { e.runTurnEnd(taskID, key, visit, reply) })
			} else {
				out = append(out, func() { e.flushPending(e.taskByID(taskID), key) })
			}
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

// planExit moves the card the agent works for out of a plan-mode column once
// the agent leaves plan mode, to the column's planExitTarget.
func (e *Engine) planExit(taskID string, visit int) {
	unlock := e.lockTask(taskID)
	defer unlock()
	e.mu.Lock()
	t := e.find(taskID)
	if t == nil || t.Visit != visit {
		e.mu.Unlock()
		return
	}
	card := store.Driver(e.state.Tasks, t)
	col, _, _ := e.board.Column(card.ColumnID)
	target, ok := e.board.Find(col.PlanExitTarget)
	planning := e.board.PermissionMode(col) == "plan"
	e.mu.Unlock()
	if !planning || col.PlanExitTarget == "" || !ok {
		return
	}
	e.logf(card, "Plan approved, moving to %s", target.Name)
	if err := e.moveLocked(card.ID, target.ID, MoveOptions{}); err != nil {
		e.logf(card, "Plan-exit move failed: %v", err)
		return
	}
	// The agent keeps working in the same turn after leaving plan mode, now
	// with the target's permissions, so that turn belongs to the new visit.
	e.with(func() {
		if s := t.Sessions[sessionKey(target)]; s != nil && s.Status == store.StatusRunning {
			s.TurnInVisit = true
			s.PermissionMode = e.board.PermissionMode(target)
			s.RespawnPending = false
		}
	})
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
	// A turn that ended during the move had its turn end dropped as
	// belonging to the previous visit; run it for this one.
	e.with(func() {
		s := t.Sessions[sessionKey(target)]
		if s == nil || s.Status != store.StatusRunning {
			return
		}
		if s.Activity == store.ActivityIdle {
			go e.runTurnEnd(taskID, sessionKey(target), t.Visit, s.LastReply)
		} else {
			s.TurnInVisit = true
		}
	})
}

// respawnPending restarts an agent whose card moved mid-turn into a column
// with another permission mode, resuming its conversation.
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
	due := s != nil && s.RespawnPending && s.Status == store.StatusRunning &&
		sessionKey(col) == key && s.PermissionMode != mode
	if s != nil {
		s.RespawnPending = false
	}
	worktree := ""
	if t != nil {
		worktree = t.WorktreePath
	}
	e.mu.Unlock()
	if !due {
		return
	}
	e.logf(t, "Turn ended, respawning agent in %s mode", mode)
	tmux.Stop(s.Tmux)
	if err := e.spawn(t, col, key, worktree, false, "", false); err != nil {
		e.logf(t, "Respawn failed: %v", err)
	}
}

// dequeue starts queued sessions while there are free slots. Called with mu held.
func (e *Engine) dequeue() []func() {
	free := e.MaxConcurrent - e.runningCount()
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
	if s.Status != store.StatusRunning || s.Activity != store.ActivityStarting || sessionKey(col) != key {
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
	scratch := s.Scratch
	e.mu.Unlock()
	worktree := e.Root
	if !scratch {
		var err error
		if worktree, err = e.ensureWorktree(t, t.ID); err != nil {
			e.with(func() {
				s.Status = store.StatusExited
				s.Activity = store.ActivityIdle
				t.AddLog(fmt.Sprintf("Worktree failed: %v", err))
			})
			return
		}
	}
	e.with(func() { s.UpdatedAt = time.Now() })
	e.startTmux(t, key, worktree, launch)
}
