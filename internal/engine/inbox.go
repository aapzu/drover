package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/inbox"
	"github.com/aapzu/drover/internal/linear"
	"github.com/aapzu/drover/internal/store"
)

// serveInbox applies the requests the drover CLI queued, one after another.
// Requests still queued when the board quits wait for the next one.
func (e *Engine) serveInbox(ctx context.Context) {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	lastErr := ""
	for {
		for ctx.Err() == nil {
			p, ok, err := inbox.Next(e.Root)
			if err != nil {
				if msg := err.Error(); msg != lastErr {
					lastErr = msg
					e.notify("inbox: %v", err)
				}
				break
			}
			lastErr = ""
			if !ok {
				break
			}
			e.answer(p)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *Engine) answer(p inbox.Pending) {
	res := e.applyRequest(p)
	op := cmp.Or(p.Request.Op, "request")
	switch {
	case res.Error != "":
		e.notify("drover %s: %s", op, res.Error)
	case p.Request.Op == inbox.OpAdd:
		e.notify("Added #%d %s", res.Number, res.Title)
	}
	if err := inbox.Reply(e.Root, p.ID, res); err != nil {
		e.notify("drover %s: could not answer the CLI: %v", op, err)
	}
	inbox.Finish(e.Root, p.ID)
}

func (e *Engine) applyRequest(p inbox.Pending) (res inbox.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = inbox.Result{Error: fmt.Sprintf("internal error: %v", r)}
		}
	}()
	if p.Err != nil {
		return inbox.Result{Error: fmt.Sprintf("unreadable request: %v", p.Err)}
	}
	r := p.Request
	var err error
	switch r.Op {
	case inbox.OpAdd:
		res, err = e.inboxAdd(r)
	case inbox.OpMove:
		res, err = e.inboxMove(r)
	case inbox.OpEdit:
		res, err = e.inboxEdit(r)
	case inbox.OpRemove:
		res, err = e.inboxRemove(r)
	default:
		err = fmt.Errorf("unknown request %q", r.Op)
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

// taskID returns the id of card #number.
func (e *Engine) taskID(number int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.state.ByNumber(number)
	if t == nil {
		return "", fmt.Errorf("no card #%d", number)
	}
	return t.ID, nil
}

func (e *Engine) findColumn(ref string) (config.Column, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	col, ok := e.board.Find(ref)
	if !ok {
		return col, fmt.Errorf("unknown column %q", ref)
	}
	return col, nil
}

func (e *Engine) inboxAdd(r inbox.Request) (inbox.Result, error) {
	var col config.Column
	if r.Column != "" {
		var err error
		if col, err = e.findColumn(r.Column); err != nil {
			return inbox.Result{}, err
		}
	}
	var task *store.Task
	if r.Linear != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		issue, err := e.LinearIssue(ctx, r.Linear)
		if err != nil {
			return inbox.Result{}, err
		}
		added := e.ImportIssues([]linear.Issue{issue})
		if len(added) == 0 {
			return inbox.Result{}, fmt.Errorf("%s is already on the board", issue.Identifier)
		}
		task = added[0]
	} else {
		title := strings.TrimSpace(deref(r.Title))
		if title == "" {
			return inbox.Result{}, errors.New("a card needs a title")
		}
		task = e.CreateTask(title, deref(r.Prompt))
	}
	res := inbox.Result{Number: task.Number, Title: task.Title}
	todo := e.todoColumn()
	res.Column = todo.Name
	if col.ID == "" || col.ID == todo.ID {
		return res, nil
	}
	if err := e.Move(task.ID, col.ID, MoveOptions{}); err != nil {
		return res, fmt.Errorf("added #%d to %s, but moving it to %s failed: %w", res.Number, todo.Name, col.Name, err)
	}
	res.Column = col.Name
	return res, nil
}

func (e *Engine) inboxMove(r inbox.Request) (inbox.Result, error) {
	res := inbox.Result{Number: r.Number}
	id, err := e.taskID(r.Number)
	if err != nil {
		return res, err
	}
	col, err := e.findColumn(r.Column)
	if err != nil {
		return res, err
	}
	if err := e.Move(id, col.ID, MoveOptions{Force: r.Force}); err != nil {
		if errors.Is(err, ErrDirty) {
			return res, fmt.Errorf("#%d's worktree has uncommitted changes; use --force to remove it", r.Number)
		}
		return res, err
	}
	res.Column = col.Name
	return res, nil
}

func (e *Engine) inboxEdit(r inbox.Request) (res inbox.Result, err error) {
	res.Number = r.Number
	e.with(func() {
		t := e.state.ByNumber(r.Number)
		if t == nil {
			err = fmt.Errorf("no card #%d", r.Number)
			return
		}
		if r.Title != nil {
			title := strings.TrimSpace(*r.Title)
			if title == "" {
				err = errors.New("a card needs a title")
				return
			}
			t.Title = title
		}
		if r.Prompt != nil {
			t.Prompt = *r.Prompt
		}
	})
	return res, err
}

func (e *Engine) inboxRemove(r inbox.Request) (inbox.Result, error) {
	res := inbox.Result{Number: r.Number}
	id, err := e.taskID(r.Number)
	if err != nil {
		return res, err
	}
	e.mu.Lock()
	alone := len(store.Members(e.state.Tasks, e.owner(e.find(id)))) == 1
	e.mu.Unlock()
	if alone && !r.Force && e.WorktreeDirty(id) {
		return res, fmt.Errorf("#%d's worktree has uncommitted changes; use --force to delete it", r.Number)
	}
	return res, e.DeleteTask(id)
}

func (e *Engine) todoColumn() config.Column {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.board.TodoColumn()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
