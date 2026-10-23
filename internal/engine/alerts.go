package engine

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

type alertKind int

const (
	alertTurnDone alertKind = iota
	alertNeedsYou
	alertPlanReady
	alertExited
)

func enabled(n config.Notifications, kind alertKind) bool {
	switch kind {
	case alertTurnDone:
		return n.TurnDone
	case alertNeedsYou:
		return n.NeedsYou
	case alertPlanReady:
		return n.PlanReady
	default:
		return n.Exited
	}
}

// SetFocused names the task you're attached to or typing into, so its agent
// neither notifies you nor times out. An empty id clears it.
func (e *Engine) SetFocused(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.focused = ""
	if t := e.find(id); t != nil {
		e.focused = e.owner(t).ID
	}
}

// alert tells you about t's agent on the board and on the desktop, at most
// once per cooldown for each kind. Called with mu held.
func (e *Engine) alert(t *store.Task, kind alertKind, body string) {
	n := e.board.Notifications
	owner := e.owner(t)
	if !enabled(n, kind) || owner.ID == e.focused {
		return
	}
	key := fmt.Sprintf("%s/%d", owner.ID, kind)
	if last, ok := e.alerted[key]; ok && time.Since(last) < time.Duration(n.CooldownSeconds)*time.Second {
		return
	}
	e.alerted[key] = time.Now()
	e.notify("#%d %s", t.Number, body)
	desktopNotify(fmt.Sprintf("#%d %s", t.Number, t.Title), body)
}

// alertTurnDone runs after the turn-end automations and queued messages, and
// notifies that the agent finished unless they started another turn.
func (e *Engine) alertTurnDone(taskID, key, reply string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.find(taskID)
	if t == nil {
		return
	}
	if s := t.Sessions[key]; s != nil && idle(s) {
		e.alert(t, alertTurnDone, "finished: "+claude.FirstLine(reply))
	}
}

// idleFor reports whether s has been idle for at least d.
func idleFor(s *store.Session, d time.Duration) bool {
	return s != nil && idle(s) && time.Since(s.UpdatedAt) >= d
}

// idle reports whether s is a started agent waiting for you with nothing
// queued.
func idle(s *store.Session) bool {
	return s.Status == store.StatusRunning && s.Activity == store.ActivityIdle && s.Started &&
		len(s.Pending) == 0 && !s.RespawnPending && !s.HasBackground()
}

// idleTimeouts returns follow-ups suspending agents idle for longer than the
// board's idleTimeoutMinutes. Called with mu held.
func (e *Engine) idleTimeouts() []func() {
	minutes := e.board.IdleTimeoutMinutes
	if minutes <= 0 {
		return nil
	}
	var out []func()
	for _, t := range e.state.Tasks {
		if t.ID == e.focused || !idleFor(t.Sessions[store.MainSession], time.Duration(minutes)*time.Minute) {
			continue
		}
		if _, stopping := e.idleStopping.LoadOrStore(t.ID, true); stopping {
			continue
		}
		id := t.ID
		out = append(out, func() {
			defer e.idleStopping.Delete(id)
			e.suspendIdle(id, minutes)
		})
	}
	return out
}

func (e *Engine) suspendIdle(id string, minutes int) {
	t, owner, unlock := e.lockGroup(id)
	defer unlock()
	if t == nil {
		return
	}
	e.mu.Lock()
	due := owner.ID != e.focused && idleFor(owner.Sessions[store.MainSession], time.Duration(minutes)*time.Minute)
	e.mu.Unlock()
	if !due {
		return
	}
	if stopped, _ := e.stopSessions(owner, "", store.StatusSuspended); stopped > 0 {
		e.logf(t, "Agent suspended after %d min idle", minutes)
		e.notify("#%d suspended after %d min idle", t.Number, minutes)
	}
}

var desktopNotify = func(title, body string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("osascript", "-e", "on run argv", "-e",
			"display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body)
	case "linux":
		cmd = exec.Command("notify-send", title, body)
	default:
		return
	}
	go cmd.Run()
}
