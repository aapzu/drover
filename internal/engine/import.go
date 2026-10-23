package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/gitx"
	"github.com/aapzu/drover/internal/linear"
	"github.com/aapzu/drover/internal/store"
)

// ExternalError is returned for actions that would start a second copy of a
// conversation that is still running in another terminal.
type ExternalError struct {
	ID          string
	Number, PID int
}

func (e *ExternalError) Error() string {
	return fmt.Sprintf("#%d is still running in another terminal (pid %d)", e.Number, e.PID)
}

func externalSession(t *store.Task) *store.Session {
	for _, s := range t.Sessions {
		if s.Status == store.StatusExternal {
			return s
		}
	}
	return nil
}

// DiscoverSessions lists Claude Code conversations for this project that are
// not on the board yet, running ones first, then limit past ones. more reports
// whether older ones were left out.
func (e *Engine) DiscoverSessions(limit int) (found []claude.Found, more bool) {
	skip := map[string]bool{}
	e.mu.Lock()
	for _, t := range e.state.Tasks {
		for _, s := range t.Sessions {
			skip[s.AgentSessionID] = true
		}
	}
	e.mu.Unlock()
	all, more := claude.Discover(e.Root, skip, limit)
	for _, f := range all {
		// Sessions the board itself launched in its worktrees are not imports.
		if !e.ownsWorktree(f.Cwd) {
			found = append(found, f)
		}
	}
	return found, more
}

// importColumn puts a running agent in the work column, so the card shows
// where the work is; others go to To Do.
func importColumn(board *config.Board, running bool) config.Column {
	if c, ok := workColumn(board); running && ok {
		return c
	}
	return board.TodoColumn()
}

// ImportSessions adds a card per conversation not already on the board and
// returns how many cards it added. The conversation stays in its own directory:
// resuming it anywhere else would not find it.
func (e *Engine) ImportSessions(found []claude.Found) int {
	var imported []*store.Task
	for _, f := range found {
		branch := f.Branch
		if current := gitx.CurrentBranch(f.Cwd); current != "" {
			branch = current
		}
		reply := claude.LastAssistantText(f.TranscriptPath)
		e.with(func() {
			for _, t := range e.state.Tasks {
				for _, s := range t.Sessions {
					if s.AgentSessionID == f.SessionID {
						return
					}
				}
			}
			now := time.Now()
			t := &store.Task{
				ID:           uuid.NewString(),
				Number:       e.state.NextNumber,
				Title:        f.Title,
				Prompt:       f.FirstPrompt,
				ColumnID:     importColumn(e.board, f.Running).ID,
				Branch:       branch,
				WorktreePath: f.Cwd,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			e.state.NextNumber++
			s := &store.Session{
				Key:            store.MainSession,
				AgentSessionID: f.SessionID,
				Tmux:           e.tmuxName(t),
				Status:         store.StatusSuspended,
				Activity:       store.ActivityIdle,
				Started:        true,
				LastReply:      reply,
				Imported:       true,
				TranscriptPath: f.TranscriptPath,
				UpdatedAt:      now,
			}
			if f.Running {
				s.Status, s.PID = store.StatusExternal, f.PID
				if f.Busy {
					s.Activity = store.ActivityThinking
				}
			}
			t.Sessions = map[string]*store.Session{store.MainSession: s}
			t.AddLog(fmt.Sprintf("Imported Claude session %s from %s", claude.ShortID(f.SessionID), f.Cwd))
			e.state.Tasks = append(e.state.Tasks, t)
			imported = append(imported, t)
		})
	}
	var scans []prScan
	e.mu.Lock()
	for _, t := range imported {
		scans = append(scans, e.prScansOf(t)...)
	}
	e.mu.Unlock()
	return len(imported) + e.scanTranscripts(scans)
}

// externalCheck refuses to start an imported or handed-off conversation that
// is running in another terminal. Sessions the board runs itself are skipped.
// Call without mu held.
func (e *Engine) externalCheck(t *store.Task) error {
	e.mu.Lock()
	imported := false
	for _, s := range t.Sessions {
		imported = imported || s.Imported || s.HandedOff
	}
	e.mu.Unlock()
	if !imported {
		return nil
	}
	live := claude.Running()
	var err error
	e.with(func() {
		for _, s := range t.Sessions {
			if !(s.Imported || s.HandedOff) || s.Status == store.StatusRunning || s.Status == store.StatusQueued {
				continue
			}
			if l, ok := live[s.AgentSessionID]; ok {
				if s.Status != store.StatusExternal {
					t.AddLog(fmt.Sprintf("Session is running in another terminal (pid %d)", l.PID))
				}
				s.Status, s.PID = store.StatusExternal, l.PID
				err = &ExternalError{ID: t.ID, Number: t.Number, PID: l.PID}
			} else if s.Status == store.StatusExternal && claude.ProcessAlive(s.PID) {
				err = &ExternalError{ID: t.ID, Number: t.Number, PID: s.PID}
			}
		}
	})
	return err
}

// pollExternal mirrors imported agents' status from Claude Code's registry and
// releases them to the board once their process is gone. Called with mu held;
// transcript reads are returned as follow-ups to run after unlocking.
func (e *Engine) pollExternal() []func() {
	var any bool
	for _, t := range e.state.Tasks {
		if externalSession(t) != nil {
			any = true
			break
		}
	}
	if !any {
		return nil
	}
	live := claude.Running()
	byPID := map[int]claude.Live{}
	for _, l := range live {
		byPID[l.PID] = l
	}
	var out []func()
	refreshReply := func(s *store.Session) {
		path := s.TranscriptPath
		out = append(out, func() {
			reply := claude.LastAssistantText(path)
			e.with(func() { s.LastReply, s.Summary = reply, "" })
		})
	}
	for _, t := range e.state.Tasks {
		s := externalSession(t)
		if s == nil {
			continue
		}
		l, ok := live[s.AgentSessionID]
		if ok {
			s.PID = l.PID
		} else if l, ok = byPID[s.PID]; ok {
			// /clear keeps the process but starts a new conversation id.
			s.AgentSessionID = l.SessionID
		}
		if !ok {
			// The registry is Claude Code's internal file and may be mid-write,
			// or not list a just-backgrounded session yet; only a vanished
			// process means the agent really exited.
			if claude.ProcessAlive(s.PID) || (s.PID == 0 && time.Since(s.UpdatedAt) < 30*time.Second) {
				continue
			}
			s.Status, s.Activity = store.StatusSuspended, store.ActivityIdle
			refreshReply(s)
			t.AddLog("Agent exited in its own terminal; press r to resume it on the board")
			e.notify("#%d exited in its terminal. Press r to resume it here.", t.Number)
			continue
		}
		activity := store.ActivityIdle
		if l.Busy() {
			activity = store.ActivityThinking
		}
		if s.Activity == store.ActivityThinking && activity == store.ActivityIdle {
			refreshReply(s)
		}
		s.Activity = activity
	}
	return out
}

// LinearIssues lists the user's open Linear issues that are not on the board.
func (e *Engine) LinearIssues(ctx context.Context) ([]linear.Issue, error) {
	if e.linear == nil {
		return nil, errors.New("set LINEAR_API_KEY to import Linear issues")
	}
	issues, err := e.linear.MyIssues(ctx)
	if err != nil {
		return nil, err
	}
	return e.withoutImported(issues), nil
}

// LinearIssue fetches one issue by key or URL.
func (e *Engine) LinearIssue(ctx context.Context, ref string) (linear.Issue, error) {
	if e.linear == nil {
		return linear.Issue{}, errors.New("set LINEAR_API_KEY to import Linear issues")
	}
	key, ok := linear.ParseKey(ref)
	if !ok {
		return linear.Issue{}, fmt.Errorf("%q is not a Linear issue key or URL", ref)
	}
	return e.linear.Issue(ctx, key)
}

func (e *Engine) HasLinear() bool { return e.linear != nil }

func (e *Engine) withoutImported(issues []linear.Issue) []linear.Issue {
	e.mu.Lock()
	defer e.mu.Unlock()
	have := map[string]bool{}
	for _, t := range e.state.Tasks {
		if t.Issue != nil {
			have[t.Issue.ID] = true
		}
	}
	var out []linear.Issue
	for _, i := range issues {
		if !have[i.ID] {
			out = append(out, i)
		}
	}
	return out
}

// ImportIssues adds a To Do card per issue not already on the board and
// returns the cards it added.
func (e *Engine) ImportIssues(issues []linear.Issue) []*store.Task {
	var added []*store.Task
	for _, i := range issues {
		e.with(func() {
			for _, t := range e.state.Tasks {
				if t.Issue != nil && t.Issue.ID == i.ID {
					return
				}
			}
			now := time.Now()
			prompt := strings.TrimSpace(i.Description)
			if prompt != "" {
				prompt += "\n\n"
			}
			prompt += fmt.Sprintf("Linear issue %s: %s", i.Identifier, i.URL)
			t := &store.Task{
				ID:        uuid.NewString(),
				Number:    e.state.NextNumber,
				Title:     i.Title,
				Prompt:    prompt,
				ColumnID:  e.board.TodoColumn().ID,
				Branch:    i.BranchName,
				CreatedAt: now,
				UpdatedAt: now,
				Issue: &store.Issue{
					Provider: "linear", ID: i.ID, Key: i.Identifier, URL: i.URL,
					TeamID: i.Team.ID, State: i.State.Name,
				},
			}
			e.state.NextNumber++
			t.AddLog(fmt.Sprintf("Imported Linear issue %s", i.Identifier))
			e.state.Tasks = append(e.state.Tasks, t)
			added = append(added, t)
		})
	}
	return added
}

// syncIssueStarted moves the task's Linear issue to a started state when an
// agent starts on it. Opt-in with DROVER_LINEAR_SYNC=1.
func (e *Engine) syncIssueStarted(t *store.Task) {
	e.mu.Lock()
	var issueID, key string
	if t.Issue != nil && t.Issue.Provider == "linear" {
		issueID, key = t.Issue.ID, t.Issue.Key
	}
	e.mu.Unlock()
	if issueID == "" || e.linear == nil || !linear.SyncEnabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		state, err := e.linear.Start(ctx, issueID)
		switch {
		case err != nil:
			e.logf(t, "Linear: could not update %s: %v", key, err)
		case state != "":
			e.with(func() {
				t.Issue.State = state
				t.AddLog(fmt.Sprintf("Linear: moved %s to %s", key, state))
			})
		}
	}()
}
