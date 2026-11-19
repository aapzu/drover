package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/assistant"
	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/gh"
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
	if id := assistant.SessionID(e.Root); id != "" {
		skip[id] = true
	}
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
// where the work is; others go to the first column.
func importColumn(board *config.Board, running bool) config.Column {
	if c, ok := workColumn(board); running && ok {
		return c
	}
	return board.FirstColumn()
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

// ImportIssues adds a card per issue not already on the board, in the first
// column without an agent, and returns the cards it added.
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
				ColumnID:  e.board.FirstColumn().ID,
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

// ReviewPR is an open pull request by someone else that you could import to
// review. Reviewed is set when you have reviewed it and it doesn't await
// your review again.
type ReviewPR struct {
	gh.Found
	Reviewed bool
}

// ReviewPRs lists the open pull requests in this repo and reviewRepos that
// await your review, then the ones you have reviewed, leaving out your own
// and those on the board. With teamReviewRequests on, requests to your teams
// count too.
func (e *Engine) ReviewPRs() ([]ReviewPR, error) {
	e.mu.Lock()
	extra, teams := slices.Clone(e.board.ReviewRepos), e.board.TeamReviewRequests
	e.mu.Unlock()
	me, err := e.login()
	if err != nil {
		return nil, err
	}
	repo, err := gh.Repo(e.Root)
	if err != nil {
		return nil, err
	}
	repos := []string{repo}
	for _, r := range extra {
		if !slices.ContainsFunc(repos, func(have string) bool { return strings.EqualFold(have, r) }) {
			repos = append(repos, r)
		}
	}
	requested := []string{"user-review-requested:@me"}
	if teams {
		requested = []string{"--review-requested=@me"}
	}
	var asked, reviewed []gh.Found
	var askErr, reviewErr error
	var wg sync.WaitGroup
	wg.Go(func() { asked, askErr = gh.Search(e.Root, repos, requested...) })
	wg.Go(func() { reviewed, reviewErr = gh.Search(e.Root, repos, "--reviewed-by=@me") })
	wg.Wait()
	if err := errors.Join(askErr, reviewErr); err != nil {
		return nil, err
	}
	return e.withoutReviewing(me, asked, reviewed), nil
}

// withoutReviewing lists requested and then reviewed as ReviewPRs, once each
// and without your own or those already on the board.
func (e *Engine) withoutReviewing(me string, requested, reviewed []gh.Found) []ReviewPR {
	e.mu.Lock()
	defer e.mu.Unlock()
	skip := map[string]bool{}
	for _, t := range e.state.Tasks {
		if t.PR != nil {
			skip[t.PR.URL] = true
		}
	}
	var out []ReviewPR
	add := func(found []gh.Found, done bool) {
		for _, f := range found {
			if skip[f.URL] || strings.EqualFold(f.Author.Login, me) {
				continue
			}
			skip[f.URL] = true
			out = append(out, ReviewPR{Found: f, Reviewed: done})
		}
	}
	add(requested, false)
	add(reviewed, true)
	return out
}

// ReviewPRByURL fetches one open pull request by someone else by its URL.
func (e *Engine) ReviewPRByURL(ref string) (ReviewPR, error) {
	url := strings.TrimSpace(ref)
	if !gh.IsURL(url) {
		return ReviewPR{}, fmt.Errorf("%q is not a pull request URL", ref)
	}
	me, err := e.login()
	if err != nil {
		return ReviewPR{}, err
	}
	info, err := gh.View(e.Root, url)
	switch {
	case err != nil:
		return ReviewPR{}, err
	case strings.EqualFold(info.Author.Login, me):
		return ReviewPR{}, fmt.Errorf("PR #%d is yours", gh.Number(url))
	case info.State != "OPEN":
		return ReviewPR{}, fmt.Errorf("PR #%d is %s", gh.Number(url), strings.ToLower(info.State))
	}
	found := gh.Found{Number: gh.Number(url), Title: info.Title, URL: url, Author: info.Author}
	return ReviewPR{Found: found, Reviewed: !info.ReviewedBy(me).SubmittedAt.IsZero()}, nil
}

// ImportReviews adds a review card per pull request not already on the board
// and returns the cards it added. A PR awaiting your review lands in the
// reviewFlow's requested column, one you have reviewed in its reviewed column.
// No agent starts; the prompt reviews the PR once you start one. The PR's
// state stays empty, so its first refresh only records a review already
// there and moves nothing.
func (e *Engine) ImportReviews(prs []ReviewPR) []*store.Task {
	var added []*store.Task
	for _, p := range prs {
		e.with(func() {
			for _, t := range e.state.Tasks {
				if t.PR != nil && t.PR.URL == p.URL {
					return
				}
			}
			now := time.Now()
			t := &store.Task{
				ID:        uuid.NewString(),
				Number:    e.state.NextNumber,
				Title:     "Review: " + p.Title,
				Prompt:    "/code-review " + p.URL,
				ColumnID:  e.board.ReviewImport(p.Reviewed).ID,
				PR:        &store.PR{URL: p.URL, Number: p.Number, Title: p.Title, Author: p.Author.Login, Review: true},
				CreatedAt: now,
				UpdatedAt: now,
			}
			e.state.NextNumber++
			t.AddLog(fmt.Sprintf("Imported PR #%d to review", p.Number))
			e.state.Tasks = append(e.state.Tasks, t)
			added = append(added, t)
		})
	}
	return added
}

// ReviewLevels are the levels /code-review takes before its target.
var ReviewLevels = []string{"low", "medium", "high", "xhigh", "max"}

var unleveledReview = regexp.MustCompile(`^/code-review (https?://\S+)$`)

// UnleveledReview reports whether t is a review card whose agent never started
// and whose prompt runs /code-review without a level, so starting it should
// ask for one.
func UnleveledReview(t *store.Task) bool {
	return t.PR.IsReview() && t.GroupID == "" && len(t.Sessions) == 0 && unleveledReview.MatchString(t.Prompt)
}

// SetReviewLevel puts level into the /code-review prompt of an imported
// review card that hasn't started.
func (e *Engine) SetReviewLevel(id, level string) error {
	if !slices.Contains(ReviewLevels, level) {
		return fmt.Errorf("%q is not a review level", level)
	}
	var err error
	e.with(func() {
		t := e.find(id)
		if t == nil || !UnleveledReview(t) {
			err = errors.New("the review has started already")
			return
		}
		url := unleveledReview.FindStringSubmatch(t.Prompt)[1]
		t.Prompt = fmt.Sprintf("/code-review %s %s", level, url)
		t.AddLog(fmt.Sprintf("Review level: %s", level))
	})
	return err
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
