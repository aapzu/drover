package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

type groupContext struct {
	from, to config.Column
	trigger  string
	reply    string
}

func isSendMessage(kind string) bool { return kind == "send_message" || kind == "send_command" }

func rowName(row config.Automation) string {
	if row.Name != "" {
		return row.Name
	}
	return row.Type
}

// runGroup runs an enter or exit group top to bottom. A failing row is logged
// and never stops the rest of the group or the move.
func (e *Engine) runGroup(t *store.Task, col config.Column, trigger string, rows []config.Automation, gc groupContext) {
	if len(rows) == 0 {
		return
	}
	gc.trigger = trigger
	e.mu.Lock()
	gc.reply = ""
	if s := e.owner(t).Sessions[sessionKey(col)]; s != nil {
		gc.reply = s.LastReply
	}
	e.mu.Unlock()

	deadline := time.Time{}
	if trigger == "exit" {
		deadline = time.Now().Add(60 * time.Second)
	}
	for i, row := range rows {
		if !deadline.IsZero() && time.Now().After(deadline) {
			e.logf(t, "Exit automations hit the 60s cap; skipped the rest")
			return
		}
		key := runKey(col.ID, trigger, i)
		if !e.shouldRun(t, col, row, key, gc.reply, false) {
			continue
		}
		if row.Type == "move_to_column" {
			e.logf(t, "Automation %q skipped: move_to_column only runs on turn end", rowName(row))
			continue
		}
		e.execute(t, col, row, key, gc)
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

	if sessionKey(col) == sessKey {
		gc := groupContext{from: col, to: col, trigger: "turn_end", reply: reply}
		for i, row := range col.Automations.OnTurnEnd {
			key := runKey(col.ID, "turn_end", i)
			if !e.shouldRun(t, col, row, key, reply, true) {
				continue
			}
			if row.Type != "move_to_column" {
				e.execute(t, col, row, key, gc)
				continue
			}
			target, ok := moveTarget(board, row.TargetColumn, idx)
			if !ok {
				e.logf(t, "Automation %q: no column %q", rowName(row), row.TargetColumn)
				continue
			}
			gc.to = target
			message := e.render(t, row.Message, gc, "")
			e.logf(t, "Automation %q: moving to %s", rowName(row), target.Name)
			err := e.moveLocked(t.ID, target.ID, MoveOptions{Message: message})
			if err == nil {
				e.with(func() { t.AutomationRuns = bump(t.AutomationRuns, key) })
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
	if e.leaveScratch(t, owner, col, board) {
		return
	}
	e.flushPending(owner, sessKey)
}

// leaveScratch moves a card whose session still runs in the main checkout into
// the first agent column once a turn that named it ends, which gives it a
// worktree and carries the conversation over. If the move fails before the
// conversation leaves the main checkout, the card goes back to try again after
// the next turn. The caller holds the task lock.
func (e *Engine) leaveScratch(t, owner *store.Task, col config.Column, board *config.Board) bool {
	if !col.IsTodo() {
		return false
	}
	e.mu.Lock()
	s := scratchSession(owner)
	due := s != nil && t.Title != "" &&
		s.Status == store.StatusRunning && s.Activity == store.ActivityIdle && len(s.Pending) == 0
	e.mu.Unlock()
	target, ok := firstAgentColumn(board)
	if !due || !ok {
		return false
	}
	e.logf(t, "First turn done, moving to %s", target.Name)
	if err := e.moveLocked(t.ID, target.ID, MoveOptions{}); err != nil {
		e.logf(t, "Move to %s failed: %v", target.Name, err)
		e.with(func() {
			if scratchSession(owner) != nil {
				t.ColumnID = col.ID
			}
		})
		return false
	}
	return true
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

// shouldRun applies maxRuns, finalLine and condition. A row that does not
// match is not counted, so it is checked again at the next turn end.
func (e *Engine) shouldRun(t *store.Task, col config.Column, row config.Automation, key, reply string, turnEnd bool) bool {
	e.mu.Lock()
	runs := t.AutomationRuns[key]
	worktree := e.owner(t).WorktreePath
	e.mu.Unlock()
	if row.MaxRuns > 0 && runs >= row.MaxRuns {
		return false
	}
	if row.FinalLine != "" && turnEnd && !MatchFinalLine(reply, row.FinalLine) {
		return false
	}
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

func (e *Engine) execute(t *store.Task, col config.Column, row config.Automation, key string, gc groupContext) {
	var err error
	switch {
	case isSendMessage(row.Type):
		e.mu.Lock()
		owner := e.owner(t)
		e.mu.Unlock()
		e.sendMessage(owner, sessionKey(col), e.render(t, row.Message, gc, ""), row.Mode == "immediate")
	case row.Type == "run_script":
		err = e.runScript(t, row, gc)
	case row.Type == "webhook":
		err = e.webhook(t, row, gc)
	case row.Type == "notify":
		title, body := row.Title, row.Body
		if title == "" {
			title = "{{title}}"
		}
		if body == "" {
			body = "{{toColumn}}"
		}
		desktopNotify(e.render(t, title, gc, ""), e.render(t, body, gc, ""))
		e.notify("%s", e.render(t, title+": "+body, gc, ""))
	default:
		err = fmt.Errorf("unsupported automation type %q", row.Type)
	}
	e.with(func() {
		if err != nil {
			t.AddLog(fmt.Sprintf("Automation %q failed: %v", rowName(row), err))
			return
		}
		t.AutomationRuns = bump(t.AutomationRuns, key)
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
	e.sendPending(t, key, next, false)
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

// render fills {{placeholders}}; unknown ones are left as written. escape
// selects how values are quoted: "" (none), "shell", "json" or "url".
func (e *Engine) render(t *store.Task, tpl string, gc groupContext, escape string) string {
	vars := e.vars(t, gc)
	return placeholder.ReplaceAllStringFunc(tpl, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		v, ok := vars[name]
		if !ok {
			return m
		}
		switch escape {
		case "shell":
			return claude.ShellQuote(v)
		case "json":
			b, _ := json.Marshal(v)
			return string(b[1 : len(b)-1])
		case "url":
			return url.QueryEscape(v)
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
	cmd := exec.CommandContext(ctx, "sh", "-c", e.render(t, row.Script, gc, "shell"))
	cmd.Dir = vars["worktreePath"]
	cmd.Env = os.Environ()
	for k, v := range vars {
		cmd.Env = append(cmd.Env, "KANGENTIC_"+strings.ToUpper(k)+"="+v)
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

func (e *Engine) webhook(t *store.Task, row config.Automation, gc groupContext) error {
	method := strings.ToUpper(row.Method)
	if method == "" {
		method = http.MethodPost
	}
	target := e.render(t, row.URL, gc, "url")
	body := e.render(t, row.Body, gc, "json")
	idempotencyKey := uuid.NewString()
	client := &http.Client{Timeout: 30 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		var reader *bytes.Reader
		if method != http.MethodGet {
			reader = bytes.NewReader([]byte(body))
		} else {
			reader = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, target, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idempotencyKey)
		for k, v := range row.Headers {
			req.Header.Set(k, e.render(t, v, gc, ""))
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		if resp.StatusCode != 408 && resp.StatusCode != 429 && resp.StatusCode < 500 {
			return lastErr
		}
	}
	return lastErr
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

var nonWord = regexp.MustCompile(`[\p{L}\p{N}]`)

// MatchFinalLine checks the last line of reply that contains a word against
// pattern, ignoring case, markdown emphasis and spacing; * matches anything.
func MatchFinalLine(reply, pattern string) bool {
	lines := strings.Split(reply, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if nonWord.MatchString(lines[i]) {
			last = lines[i]
			break
		}
	}
	if last == "" {
		return false
	}
	parts := strings.Split(normalizeLine(pattern), "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re, err := regexp.Compile("^" + strings.Join(parts, ".*") + "$")
	if err != nil {
		return false
	}
	return re.MatchString(normalizeLine(last))
}

var (
	emphasis = strings.NewReplacer("**", "", "__", "", "`", "")
	spaces   = regexp.MustCompile(`\s+`)
)

func normalizeLine(s string) string {
	s = emphasis.Replace(s)
	s = strings.TrimLeft(strings.TrimSpace(s), "#>-• ")
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	s = strings.TrimRight(s, ".!")
	return strings.ToLower(s)
}
