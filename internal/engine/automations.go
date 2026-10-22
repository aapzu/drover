package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
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
		if row.Type == "move_to_column" {
			e.logf(t, "Automation %q skipped: move_to_column only runs on turn end", rowName(row))
			continue
		}
		if e.shouldRun(t, row, gc) {
			e.execute(t, row, gc)
		}
	}
}

// withoutAgent drops the rows that need an agent, for a column that runs none.
func (e *Engine) withoutAgent(t *store.Task, rows []config.Automation) []config.Automation {
	var kept []config.Automation
	for _, row := range rows {
		if row.Type == "send_message" {
			e.logf(t, "Automation %q skipped: no agent runs in this column", rowName(row))
			continue
		}
		kept = append(kept, row)
	}
	return kept
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
			if !e.shouldRun(t, row, gc) {
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
			e.logf(t, "Automation %q: move to %s failed: %v", rowName(row), target.Name, err)
			e.flushPending(owner, sessKey)
			return
		}
	}
	if e.shipOpenedPR(owner) || e.leaveScratch(t, owner, col, board, false, false) {
		return
	}
	e.flushPending(owner, sessKey)
}

// leaveScratchWorking moves a named card out of To Do as soon as its agent
// starts a turn.
func (e *Engine) leaveScratchWorking(taskID string, skipPlan bool) {
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
	e.leaveScratch(t, owner, col, board, true, skipPlan)
}

// routeScratch judges a new session's prompt and moves the card, past the plan
// columns if it needs no plan. If the judgement fails, the error is logged and
// the card plans.
func (e *Engine) routeScratch(taskID, prompt string) {
	needsPlan, err := claude.NeedsPlan(e.stopping, e.Root, truncatePrompt(prompt))
	if e.stopping.Err() != nil {
		return
	}
	e.with(func() {
		t := e.find(taskID)
		if t == nil {
			return
		}
		if s := t.Sessions[store.MainSession]; s != nil {
			s.Routing = false
		}
		switch {
		case err != nil:
			t.AddLog(err.Error() + ", planning")
		case !needsPlan:
			t.AddLog("Plan check: no plan needed, skipping planning")
		}
	})
	e.leaveScratchWorking(taskID, !needsPlan)
}

// truncatePrompt caps prompt at 4000 runes so the Haiku calls stay small.
func truncatePrompt(prompt string) string {
	if runes := []rune(prompt); len(runes) > 4000 {
		return string(runes[:4000])
	}
	return prompt
}

// leaveScratch moves a card whose session still runs in the main checkout into
// the first agent column, or the work column when it was routed past planning.
// It waits while the first prompt is still being judged.
func (e *Engine) leaveScratch(t, owner *store.Task, col config.Column, board *config.Board, working, skipPlan bool) bool {
	if !col.IsTodo() {
		return false
	}
	e.mu.Lock()
	s := scratchSession(owner)
	idle := s != nil && s.Activity == store.ActivityIdle && len(s.Pending) == 0
	// A review waiting for gh to confirm its PR moves the card itself.
	held := t.PR.IsReview() && t.PRMovePending
	due := s != nil && t.Title != "" && !held && !s.Routing && s.Status == store.StatusRunning && (working || idle)
	e.mu.Unlock()
	target, ok := firstAgentColumn(board)
	if skipPlan {
		if work, found := workColumn(board); found {
			target = work
		}
	}
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

// shouldRun checks the row's conditions, if any.
func (e *Engine) shouldRun(t *store.Task, row config.Automation, gc groupContext) bool {
	if row.ConditionScript != "" {
		if err := e.runShellRow(t, row, row.ConditionScript, gc); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() >= 1 && exit.ExitCode() < 126 {
				e.logf(t, "Automation %q skipped: condition script not met (exit %d)", rowName(row), exit.ExitCode())
				return false
			}
			e.skipErrored(t, row, "condition script", err)
			return false
		}
	}
	if row.Condition != "" {
		e.mu.Lock()
		dir := e.owner(t).WorktreePath
		e.mu.Unlock()
		if dir == "" {
			dir = e.Root
		}
		ok, err := claude.EvalCondition(e.stopping, dir, row.Condition, gc.reply)
		if err != nil {
			e.skipErrored(t, row, "condition", err)
			return false
		}
		if !ok {
			e.logf(t, "Automation %q skipped: condition not met", rowName(row))
			return false
		}
	}
	return true
}

// skipErrored logs a condition that couldn't be evaluated, unless the engine
// is stopping and the error is only that.
func (e *Engine) skipErrored(t *store.Task, row config.Automation, what string, err error) {
	if e.stopping.Err() != nil {
		return
	}
	e.logf(t, "Automation %q skipped: %s errored: %v", rowName(row), what, err)
	if row.Type == "move_to_column" {
		e.notify("#%d needs you: the %s of %s errored, see the card log", t.Number, what, rowName(row))
	}
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
		err = e.runShellRow(t, row, row.Script, gc)
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

// runShellRow runs script with sh -c in the worktree of the card's session, or
// the project root without one.
func (e *Engine) runShellRow(t *store.Task, row config.Automation, script string, gc groupContext) error {
	timeout := time.Duration(max(1, min(row.TimeoutMinutes, 120))) * time.Minute
	if row.TimeoutMinutes == 0 {
		timeout = 10 * time.Minute
	}
	vars := e.vars(t, gc)
	var env []string
	for k, v := range vars {
		env = append(env, "DROVER_"+strings.ToUpper(k)+"="+v)
	}
	return runShell(e.stopping, vars["worktreePath"], e.render(t, script, gc, true), env, timeout)
}

// runShell runs script with sh -c in dir, killing it after timeout. Its error
// ends with the last of the script's output.
func runShell(ctx context.Context, dir, script string, env []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s: %w", timeout, err)
	}
	tail := strings.TrimSpace(string(out))
	if len(tail) > 300 {
		tail = "…" + tail[len(tail)-300:]
	}
	if tail != "" {
		err = fmt.Errorf("%w: %s", err, tail)
	}
	return err
}

// autoName replaces the placeholder title of a new session's card with a
// short summary of your first message, unless the title changed meanwhile.
func (e *Engine) autoName(taskID, prompt, placeholder string) {
	answer, err := claude.AskHaiku(e.stopping, e.Root,
		"Write a title of at most six words for this coding task. Reply with the title only.\n\n<task>\n"+truncatePrompt(prompt)+"\n</task>")
	title := strings.TrimRight(strings.Trim(claude.FirstLine(answer), "\"'` *"), ".")
	if err != nil || title == "" {
		return
	}
	e.with(func() {
		if t := e.find(taskID); t != nil && t.Title == placeholder {
			t.Title = title
		}
	})
}
