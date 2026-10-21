package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

type groupContext struct {
	from, to config.Column
	trigger  string
	reply    string
}

func rowName(row config.Automation) string {
	if row.Name != "" {
		return row.Name
	}
	return row.Type
}

// runEnterGroup runs the column's enter group top to bottom. A failing row is
// logged and never stops the rest of the group or the move.
func (e *Engine) runEnterGroup(t *store.Task, rows []config.Automation, gc groupContext) {
	if len(rows) == 0 {
		return
	}
	gc.trigger = "enter"
	e.mu.Lock()
	gc.reply = ""
	if s := e.owner(t).Sessions[store.MainSession]; s != nil {
		gc.reply = s.LastReply
	}
	e.mu.Unlock()

	for _, row := range rows {
		if !e.shouldRun(t, row, gc.reply) {
			continue
		}
		if row.Type == "move_to_column" {
			e.logf(t, "Automation %q skipped: move_to_column only runs on turn end", rowName(row))
			continue
		}
		e.execute(t, row, gc)
	}
}

// runTurnEnd runs the column's turn-end group after the agent finishes a turn
// that started during the current visit, then delivers queued messages. The
// group is that of the card the agent works for, so a move_to_column moves
// only that card.
func (e *Engine) runTurnEnd(taskID, sessKey string, visit int, reply string) {
	unlock := e.lockTask(taskID)
	defer unlock()
	e.mu.Lock()
	owner := e.find(taskID)
	if owner == nil || owner.Visit != visit {
		e.mu.Unlock()
		return
	}
	t := store.Driver(e.state.Tasks, owner)
	col, idx, _ := e.board.Column(t.ColumnID)
	board := e.board
	e.mu.Unlock()

	if sessKey == store.MainSession {
		gc := groupContext{from: col, to: col, trigger: "turn_end", reply: reply}
		for _, row := range col.Automations.OnTurnEnd {
			if !e.shouldRun(t, row, reply) {
				continue
			}
			if row.Type != "move_to_column" {
				e.execute(t, row, gc)
				continue
			}
			target, ok := moveTarget(board, row.TargetColumn, idx)
			if !ok {
				e.logf(t, "Automation %q: no column %q", rowName(row), row.TargetColumn)
				continue
			}
			gc.to = target
			message := e.render(t, row.Message, gc, false)
			e.logf(t, "Automation %q: moving to %s", rowName(row), target.Name)
			err := e.moveLocked(t.ID, target.ID, MoveOptions{Message: message})
			if err == nil {
				return
			}
			if errors.Is(err, ErrDirty) {
				e.logf(t, "Automation %q: not moved to %s, the worktree has uncommitted changes", rowName(row), target.Name)
				e.notify("#%d needs you: uncommitted changes block the move to %s", t.Number, target.Name)
			} else {
				e.logf(t, "Automation %q: move to %s failed: %v", rowName(row), target.Name, err)
			}
			e.flushPending(owner, sessKey)
			return
		}
	}
	if e.shipOpenedPR(owner) || e.leaveScratch(t, owner, col, board, false) {
		return
	}
	e.flushPending(owner, sessKey)
}

// leaveScratchWorking moves a named card out of To Do as soon as its agent
// starts a turn.
func (e *Engine) leaveScratchWorking(taskID string) {
	unlock := e.lockTask(taskID)
	defer unlock()
	e.mu.Lock()
	owner := e.find(taskID)
	if owner == nil {
		e.mu.Unlock()
		return
	}
	t := store.Driver(e.state.Tasks, owner)
	col := e.sessionColumn(owner)
	board := e.board
	e.mu.Unlock()
	e.leaveScratch(t, owner, col, board, true)
}

// leaveScratch moves a card whose session still runs in the main checkout into
// the first agent column.
func (e *Engine) leaveScratch(t, owner *store.Task, col config.Column, board *config.Board, working bool) bool {
	if !col.IsTodo() {
		return false
	}
	e.mu.Lock()
	s := scratchSession(owner)
	idle := s != nil && s.Activity == store.ActivityIdle && len(s.Pending) == 0
	// A review waiting for gh to confirm its PR moves the card itself.
	held := t.PR.IsReview() && t.PRMovePending
	due := s != nil && t.Title != "" && !held && s.Status == store.StatusRunning && (working || idle)
	e.mu.Unlock()
	target, ok := firstAgentColumn(board)
	if !due || !ok {
		return false
	}
	why := "First turn done"
	if working {
		why = "Agent is working"
	}
	e.logf(t, "%s, moving to %s", why, target.Name)
	if err := e.moveLocked(t.ID, target.ID, MoveOptions{}); err != nil {
		e.logf(t, "Move to %s failed: %v", target.Name, err)
		e.restoreScratch(t, owner, col)
		return false
	}
	return true
}

// restoreScratch puts t back in col after a failed move, if its conversation
// is still in the main checkout.
func (e *Engine) restoreScratch(t, owner *store.Task, col config.Column) {
	e.with(func() {
		if scratchSession(owner) != nil {
			t.ColumnID = col.ID
		}
	})
}

func moveTarget(board *config.Board, ref string, currentIdx int) (config.Column, bool) {
	if ref == "" {
		if currentIdx+1 < len(board.Columns) {
			return board.Columns[currentIdx+1], true
		}
		return config.Column{}, false
	}
	return board.Find(ref)
}

// shouldRun checks the row's condition, if any, against the agent's reply.
func (e *Engine) shouldRun(t *store.Task, row config.Automation, reply string) bool {
	e.mu.Lock()
	worktree := e.owner(t).WorktreePath
	e.mu.Unlock()
	if row.Condition != "" {
		dir := worktree
		if dir == "" {
			dir = e.Root
		}
		ok, err := claude.EvalCondition(context.Background(), dir, row.Condition, reply)
		if err != nil {
			e.logf(t, "Automation %q skipped: %v", rowName(row), err)
			return false
		}
		if !ok {
			e.logf(t, "Automation %q skipped: condition not met", rowName(row))
			return false
		}
	}
	return true
}

func (e *Engine) execute(t *store.Task, row config.Automation, gc groupContext) {
	var err error
	switch row.Type {
	case "send_message":
		e.mu.Lock()
		owner := e.owner(t)
		e.mu.Unlock()
		e.sendMessage(owner, store.MainSession, e.render(t, row.Message, gc, false))
	case "run_script":
		err = e.runScript(t, row, gc)
	case "notify":
		title, body := row.Title, row.Body
		if title == "" {
			title = "{{title}}"
		}
		if body == "" {
			body = "{{toColumn}}"
		}
		desktopNotify(e.render(t, title, gc, false), e.render(t, body, gc, false))
		e.notify("%s", e.render(t, title+": "+body, gc, false))
	default:
		err = fmt.Errorf("unsupported automation type %q", row.Type)
	}
	e.with(func() {
		if err != nil {
			t.AddLog(fmt.Sprintf("Automation %q failed: %v", rowName(row), err))
			return
		}
		t.AddLog(fmt.Sprintf("Automation %q ran", rowName(row)))
	})
}

func (e *Engine) flushPending(t *store.Task, key string) {
	e.mu.Lock()
	s := t.Sessions[key]
	if s == nil || s.Status != store.StatusRunning || s.Activity != store.ActivityIdle || len(s.Pending) == 0 {
		e.mu.Unlock()
		return
	}
	next := s.Pending[0]
	s.Pending = s.Pending[1:]
	e.mu.Unlock()
	e.sendPending(t, key, next)
}

func (e *Engine) vars(t *store.Task, gc groupContext) map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	owner := e.owner(t)
	worktree := owner.WorktreePath
	if worktree == "" {
		worktree = e.Root
	}
	branch := owner.Branch
	var prURL, prNumber, prTitle, prBranch string
	if t.PR != nil {
		prURL, prNumber, prTitle, prBranch = t.PR.URL, fmt.Sprint(t.PR.Number), t.PR.Title, t.PR.Branch
		if prBranch != "" {
			branch = prBranch
		}
	}
	base := owner.BaseBranch
	if base == "" {
		base = e.board.DefaultBaseBranch
	}
	if base == "" {
		base = "main"
	}
	var issueKey, issueURL string
	if t.Issue != nil {
		issueKey, issueURL = t.Issue.Key, t.Issue.URL
	}
	return map[string]string{
		"issueKey":     issueKey,
		"issueUrl":     issueURL,
		"title":        t.Title,
		"description":  t.Prompt,
		"taskId":       t.ID,
		"taskNumber":   fmt.Sprint(t.Number),
		"projectPath":  e.Root,
		"projectName":  e.ProjectName,
		"worktreePath": worktree,
		"branchName":   branch,
		"prUrl":        prURL,
		"prNumber":     prNumber,
		"prTitle":      prTitle,
		"prBranch":     prBranch,
		"baseBranch":   base,
		"column":       gc.to.Name,
		"fromColumn":   gc.from.Name,
		"toColumn":     gc.to.Name,
		"trigger":      gc.trigger,
		"agentReply":   gc.reply,
	}
}

var placeholder = regexp.MustCompile(`\{\{\s*([a-zA-Z_]+)\s*\}\}`)

// render fills {{placeholders}}; unknown ones are left as written. shell
// quotes each value for sh.
func (e *Engine) render(t *store.Task, tpl string, gc groupContext, shell bool) string {
	vars := e.vars(t, gc)
	return placeholder.ReplaceAllStringFunc(tpl, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		v, ok := vars[name]
		if !ok {
			return m
		}
		if shell {
			return claude.ShellQuote(v)
		}
		return v
	})
}

func (e *Engine) runScript(t *store.Task, row config.Automation, gc groupContext) error {
	timeout := time.Duration(max(1, min(row.TimeoutMinutes, 120))) * time.Minute
	if row.TimeoutMinutes == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	vars := e.vars(t, gc)
	cmd := exec.CommandContext(ctx, "sh", "-c", e.render(t, row.Script, gc, true))
	cmd.Dir = vars["worktreePath"]
	cmd.Env = os.Environ()
	for k, v := range vars {
		cmd.Env = append(cmd.Env, "DROVER_"+strings.ToUpper(k)+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 300 {
			tail = "…" + tail[len(tail)-300:]
		}
		return fmt.Errorf("%w: %s", err, tail)
	}
	return nil
}

func desktopNotify(title, body string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %q with title %q", body, title)
		cmd = exec.Command("osascript", "-e", script)
	case "linux":
		cmd = exec.Command("notify-send", title, body)
	default:
		return
	}
	go cmd.Run()
}
