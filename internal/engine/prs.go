package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/gh"
	"github.com/aapzu/drover/internal/gitx"
	"github.com/aapzu/drover/internal/store"
)

const prLookups = 4

// pollPRs returns the refresh of every PR card that can still change, when
// one is due. Called with mu held.
func (e *Engine) pollPRs() []func() {
	if time.Since(e.lastPRPoll) < e.PRPollInterval || e.prPolling.Load() {
		return nil
	}
	e.lastPRPoll = time.Now()
	flow := e.board.PRFlow
	var open, merged []string
	for _, t := range e.state.Tasks {
		col, _, _ := e.board.Column(t.ColumnID)
		if t.PR == nil || col.IsDone() {
			continue
		}
		switch t.PR.State {
		case "CLOSED":
		case "MERGED":
			if flow != nil && flow.Released != "" && !t.PR.Released && !t.PR.IsReview() && t.PR.MergeCommit != "" {
				merged = append(merged, t.ID)
			}
		default:
			open = append(open, t.ID)
		}
	}
	if len(merged) == 0 || !e.isRepo || time.Since(e.lastReleaseFetch) < e.ReleaseFetchInterval {
		merged = nil
	} else {
		e.lastReleaseFetch = time.Now()
	}
	scans := e.prScans()
	if len(open) == 0 && len(merged) == 0 && len(scans) == 0 {
		return nil
	}
	branch := ""
	if flow != nil {
		branch = flow.ReleaseBranch
	}
	e.prPolling.Store(true)
	return []func(){func() {
		defer e.prPolling.Store(false)
		e.scanTranscripts(scans)
		var wg sync.WaitGroup
		slots := make(chan struct{}, prLookups)
		for _, id := range open {
			slots <- struct{}{}
			wg.Go(func() {
				defer func() { <-slots }()
				e.refreshPR(id)
			})
		}
		wg.Wait()
		e.scanTranscripts(scans)
		if len(merged) == 0 {
			return
		}
		ref, err := gitx.FetchRelease(e.Root, branch)
		if err != nil {
			e.notify("Checking released PRs failed: %v", err)
			return
		}
		for _, id := range merged {
			e.checkRelease(id, ref)
		}
	}}
}

// prScan is a transcript to search for PRs its session created.
type prScan struct {
	owner, key, path string
	offset           int64
}

// prScans lists the transcripts of the sessions of cards not in Done.
// Called with mu held.
func (e *Engine) prScans() []prScan {
	var scans []prScan
	for _, t := range e.state.Tasks {
		scans = append(scans, e.prScansOf(t)...)
	}
	return scans
}

// prScansOf lists the transcripts of t's sessions, none for a split card or
// one in Done. Called with mu held.
func (e *Engine) prScansOf(t *store.Task) []prScan {
	if col, _, _ := e.board.Column(t.ColumnID); t.GroupID != "" || col.IsDone() {
		return nil
	}
	var scans []prScan
	for key, s := range t.Sessions {
		path := e.transcriptOf(t, s)
		if path == "" {
			continue
		}
		offset := s.PRScan
		if s.PRScanPath != path {
			offset = 0
		}
		scans = append(scans, prScan{owner: t.ID, key: key, path: path, offset: offset})
	}
	return scans
}

// transcriptOf is where s's current conversation is recorded. Called with mu held.
func (e *Engine) transcriptOf(t *store.Task, s *store.Session) string {
	if s.AgentSessionID == "" {
		return ""
	}
	if s.TranscriptPath != "" {
		return filepath.Join(filepath.Dir(s.TranscriptPath), s.AgentSessionID+".jsonl")
	}
	dir := t.WorktreePath
	if dir == "" {
		dir = e.Root
	}
	return claude.TranscriptPath(s.AgentSessionID, dir)
}

// scanUsage adds what the session's latest turns used to its totals.
func (e *Engine) scanUsage(taskID, key string) {
	e.mu.Lock()
	var s *store.Session
	path := ""
	if t := e.find(taskID); t != nil {
		if s = t.Sessions[key]; s != nil {
			path = e.transcriptOf(t, s)
		}
	}
	if path == "" {
		e.mu.Unlock()
		return
	}
	offset, usage := s.UsageScan, s.Usage
	if filepath.Base(s.UsageScanPath) != filepath.Base(path) {
		offset = 0
	}
	e.mu.Unlock()
	next, err := claude.ScanUsage(path, offset, &usage)
	if err != nil {
		return
	}
	e.with(func() {
		if t := e.find(taskID); t != nil && t.Sessions[key] == s && e.transcriptOf(t, s) == path {
			s.Usage, s.UsageScanPath, s.UsageScan = usage, path, next
		}
	})
}

// scanTranscripts adds the PRs sessions created since the last scan and
// returns how many cards it split off.
func (e *Engine) scanTranscripts(scans []prScan) (split int) {
	type found struct {
		prScan
		urls []string
		next int64
		err  error
	}
	var results []found
	for _, sc := range scans {
		urls, next, err := claude.TranscriptPRs(sc.path, sc.offset)
		if err != nil || next != sc.offset {
			results = append(results, found{sc, urls, next, err})
		}
	}
	if len(results) == 0 {
		return 0
	}
	var ship []*store.Task
	e.with(func() {
		before := len(e.state.Tasks)
		for _, r := range results {
			t := e.find(r.owner)
			if t == nil || t.Sessions[r.key] == nil || e.transcriptOf(t, t.Sessions[r.key]) != r.path {
				continue
			}
			s := t.Sessions[r.key]
			if r.err != nil {
				if e.prScanErrs[r.path] != r.err.Error() {
					t.AddLog(fmt.Sprintf("Looking for PRs in the conversation failed: %v", r.err))
				}
				e.prScanErrs[r.path] = r.err.Error()
				continue
			}
			delete(e.prScanErrs, r.path)
			s.PRScanPath, s.PRScan = r.path, r.next
			if len(r.urls) > 0 {
				e.addPRs(t, r.urls, false)
				if t.PRMovePending && agentIdle(t) {
					ship = append(ship, t)
				}
			}
		}
		split = len(e.state.Tasks) - before
	})
	for _, t := range ship {
		e.shipTask(t)
	}
	return split
}

// refreshPR updates an open PR's card from GitHub and moves it along the
// prFlow, or along the reviewFlow for a PR you review. For those it first
// asks gh whether the PR is yours after all.
func (e *Engine) refreshPR(id string) {
	e.mu.Lock()
	t := e.find(id)
	if t == nil || t.PR == nil {
		e.mu.Unlock()
		return
	}
	url, review := t.PR.URL, t.PR.Review
	e.mu.Unlock()
	info, err := gh.View(e.Root, url)
	var me string
	if err == nil && review {
		if me, err = e.login(); err != nil {
			err = fmt.Errorf("can't tell whether it is yours, so the card stays put: %w", err)
		}
	}

	card, owner, unlock := e.lockGroup(id)
	defer unlock()
	if card == nil || card.PR == nil {
		return
	}
	var step prMove
	confirmed := false
	e.with(func() {
		pr := card.PR
		if err != nil {
			e.prError(card, err)
			return
		}
		if pr.Review && info.Author.Login == me {
			e.dropReviewNomination(card, owner)
			return
		}
		if pr.Title == "" && info.Title != "" && card.GroupID != "" {
			card.Title = info.Title
		}
		prev, prevHead := pr.State, pr.HeadSHA
		pr.Error = ""
		pr.Title, pr.Branch, pr.State = info.Title, info.Branch, info.State
		pr.ReviewDecision, pr.MergeCommit, pr.HeadSHA = info.ReviewDecision, info.MergeOID(), info.HeadSHA
		if pr.Review {
			pr.Author = info.Author.Login
			confirmed = info.State == "OPEN"
			if card.Title == "" && info.Title != "" {
				card.Title = "Review: " + info.Title
			}
			step = e.reviewStep(card, owner, prev, prevHead, info, me)
			return
		}
		step = e.prStep(card, owner, prev, info)
	})
	e.applyPRMove(card, owner, step)
	if confirmed {
		e.shipOpenedPR(card)
	}
}

// checkRelease moves a merged PR's card on once its merge commit is on ref.
func (e *Engine) checkRelease(id, ref string) {
	e.mu.Lock()
	t := e.find(id)
	if t == nil || t.PR == nil {
		e.mu.Unlock()
		return
	}
	commit := t.PR.MergeCommit
	e.mu.Unlock()
	reached, err := gitx.Contains(e.Root, ref, commit)

	card, owner, unlock := e.lockGroup(id)
	defer unlock()
	if card == nil || card.PR == nil {
		return
	}
	var step prMove
	e.with(func() {
		pr := card.PR
		if err != nil {
			e.prError(card, err)
			return
		}
		pr.Error = ""
		flow := e.board.PRFlow
		if !reached || flow == nil {
			return
		}
		released := func() {
			pr.Released = true
			card.AddLog(fmt.Sprintf("PR #%d reached the release branch", pr.Number))
		}
		if e.inColumn(card, flow.Merged) {
			step = prMove{to: e.flowColumn(flow.Released), onMoved: released}
		} else {
			released()
		}
	})
	e.applyPRMove(card, owner, step)
}

// prMove is where a PR card goes next.
type prMove struct {
	to      config.Column
	message string
	onMoved func()
}

// prStep records what changed on the card's PR and returns the move it
// calls for, if any. Called with mu held.
func (e *Engine) prStep(card, owner *store.Task, prevState string, info gh.PR) prMove {
	pr := card.PR
	flow := e.board.PRFlow
	if flow == nil {
		flow = &config.PRFlow{}
	}
	switch at := info.ChangesRequestedAt(); {
	case info.State == "MERGED" && prevState != "MERGED":
		card.AddLog(fmt.Sprintf("PR #%d merged", pr.Number))
		return prMove{to: e.flowColumn(flow.Merged)}
	case info.State == "CLOSED" && prevState != "CLOSED":
		card.AddLog(fmt.Sprintf("PR #%d closed without merging", pr.Number))
		e.notify("#%d: PR #%d was closed", card.Number, pr.Number)
	case info.State == "OPEN" && at.After(pr.ChangesSeenAt):
		pr.ChangesSeenAt = at
		card.AddLog(fmt.Sprintf("Changes requested on PR #%d", pr.Number))
		e.notify("#%d: changes requested on PR #%d", card.Number, pr.Number)
		if e.inColumn(card, flow.Opened) {
			return prMove{to: e.flowColumn(flow.ChangesRequested), message: flow.ChangesRequestedMessage,
				onMoved: func() { pr.FixFrom = info.HeadSHA }}
		}
	case pr.FixFrom != "" && info.HeadSHA != pr.FixFrom && e.inColumn(card, flow.ChangesRequested) && agentIdle(owner):
		return prMove{to: e.flowColumn(flow.Opened), onMoved: func() {
			pr.FixFrom = ""
			card.AddLog(fmt.Sprintf("Pushed review fixes to PR #%d", pr.Number))
		}}
	}
	return prMove{}
}

// dropReviewNomination forgets a PR that a review prompt named but gh shows
// you wrote: the owner's card goes back to having no PR, and a card split off
// for it is removed. Called with mu held.
func (e *Engine) dropReviewNomination(card, owner *store.Task) {
	pr := card.PR
	if card.GroupID == "" {
		card.AddLog(fmt.Sprintf("PR #%d is yours, not tracking it as a review", pr.Number))
		card.PR, card.PRMovePending = nil, false
		return
	}
	owner.AddLog(fmt.Sprintf("PR #%d is yours, not tracking it as a review (card #%d removed)", pr.Number, card.Number))
	owner.SeenPRs = slices.DeleteFunc(owner.SeenPRs, func(u string) bool { return u == pr.URL })
	e.state.Tasks = slices.DeleteFunc(e.state.Tasks, func(t *store.Task) bool { return t == card })
}

// loginRetry is how long a failed login lookup is remembered, so a gh that is
// offline or signed out costs one lookup per interval, not one per card.
var loginRetry = 30 * time.Second

// login is the GitHub user the CLI is signed in as, looked up once.
func (e *Engine) login() (string, error) {
	e.meMu.Lock()
	defer e.meMu.Unlock()
	if e.me != "" {
		return e.me, nil
	}
	if e.meErr != nil && time.Since(e.meFailedAt) < loginRetry {
		return "", e.meErr
	}
	me, err := gh.Me(e.Root)
	if err != nil {
		e.meErr, e.meFailedAt = err, time.Now()
		return "", err
	}
	e.me, e.meErr = me, nil
	return me, nil
}

// reviewStep records what changed on a PR you review and returns the move it
// calls for, if any. A review you submit moves the card to reviewFlow.reviewed
// from any column. New commits after it are logged and notified, and move a
// card in reviewFlow.reviewed to reviewFlow.updated. A review already on the PR
// when drover first looks is only recorded, so it moves nothing; a merge or
// close still moves the card to reviewFlow.done. Called with mu held.
func (e *Engine) reviewStep(card, owner *store.Task, prevState, prevHead string, info gh.PR, me string) prMove {
	pr := card.PR
	flow := e.board.ReviewFlow
	if flow == nil {
		flow = &config.ReviewFlow{}
	}
	first := prevState == ""
	mine := info.ReviewedBy(me)
	// Moving the card would stop an agent that is still writing, so a review
	// is picked up at the first poll after its turn.
	reviewed := mine.SubmittedAt.After(pr.MyReviewAt) && (first || agentIdle(owner))
	if reviewed {
		pr.MyReview, pr.MyReviewAt, pr.ReviewedSHA = mine.State, mine.SubmittedAt, mine.Commit.OID
		if pr.ReviewedSHA == "" {
			pr.ReviewedSHA = info.HeadSHA
		}
	}
	if first {
		return prMove{}
	}
	switch {
	case info.State == "MERGED" && prevState != "MERGED":
		card.AddLog(fmt.Sprintf("PR #%d merged", pr.Number))
		return prMove{to: e.flowColumn(flow.Done)}
	case info.State == "CLOSED" && prevState != "CLOSED":
		card.AddLog(fmt.Sprintf("PR #%d closed without merging", pr.Number))
		e.notify("#%d: PR #%d was closed", card.Number, pr.Number)
		return prMove{to: e.flowColumn(flow.Done)}
	case info.State != "OPEN":
	case reviewed:
		card.AddLog(fmt.Sprintf("You reviewed PR #%d: %s", pr.Number, strings.ToLower(mine.State)))
		return prMove{to: e.flowColumn(flow.Reviewed)}
	case info.HeadSHA != prevHead && pr.ReviewedSHA != "":
		card.AddLog(fmt.Sprintf("New commits on PR #%d since your review", pr.Number))
		e.notify("#%d: new commits on PR #%d since your review", card.Number, pr.Number)
		if e.inColumn(card, flow.Reviewed) {
			return prMove{to: e.flowColumn(flow.Updated)}
		}
	}
	return prMove{}
}

// flowColumn resolves a prFlow target; an unset one is the zero column,
// which means no move. Called with mu held.
func (e *Engine) flowColumn(ref string) config.Column {
	c, _ := e.board.Find(ref)
	return c
}

// inColumn reports whether card is in the prFlow target ref. Called with mu held.
func (e *Engine) inColumn(card *store.Task, ref string) bool {
	c, ok := e.board.Find(ref)
	return ok && c.ID == card.ColumnID
}

// prError records a failed check once, until it changes. Called with mu held.
func (e *Engine) prError(card *store.Task, err error) {
	pr := card.PR
	if pr.Error != err.Error() {
		card.AddLog(fmt.Sprintf("Checking PR #%d failed: %v", pr.Number, err))
		e.notify("#%d: checking PR #%d failed: %v", card.Number, pr.Number, err)
	}
	pr.Error = err.Error()
}

// applyPRMove makes the move prStep or checkRelease asked for. The caller
// holds the group's lock.
func (e *Engine) applyPRMove(card, owner *store.Task, step prMove) {
	if step.to.ID == "" {
		return
	}
	message := ""
	if step.message != "" {
		message = e.render(card, step.message, groupContext{to: step.to}, false)
	}
	err := e.moveLocked(card.ID, step.to.ID, MoveOptions{Message: message})
	switch {
	case errors.Is(err, ErrDirty):
		e.logf(card, "Not moved to %s: the worktree has uncommitted changes", step.to.Name)
		e.notify("#%d needs you: uncommitted changes block the move to %s", card.Number, step.to.Name)
		return
	case err != nil:
		e.logf(card, "Moving to %s failed: %v", step.to.Name, err)
		e.notify("#%d: moving to %s failed: %v", card.Number, step.to.Name, err)
		return
	}
	if step.onMoved != nil {
		e.with(step.onMoved)
	}
	if step.to.IsDone() && !card.PR.IsReview() {
		e.notifyAllReleased(owner)
	}
}

// agentRunning reports whether any of the owner's sessions is running, queued
// or run outside drover, so that a turn end may still come.
func agentRunning(owner *store.Task) bool {
	for _, s := range owner.Sessions {
		if s.Status == store.StatusRunning || s.Status == store.StatusQueued || s.Status == store.StatusExternal {
			return true
		}
	}
	return false
}

func hasExternal(owner *store.Task) bool {
	for _, s := range owner.Sessions {
		if s.Status == store.StatusExternal {
			return true
		}
	}
	return false
}

func agentIdle(owner *store.Task) bool {
	for _, s := range owner.Sessions {
		if (s.Status == store.StatusRunning || s.Status == store.StatusQueued) &&
			(s.Activity != store.ActivityIdle || len(s.Pending) > 0) {
			return false
		}
	}
	return true
}

func (e *Engine) notifyAllReleased(owner *store.Task) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range store.Members(e.state.Tasks, owner) {
		if col, _, _ := e.board.Column(m.ColumnID); m.PR != nil && !m.PR.IsReview() && !col.IsDone() {
			return
		}
	}
	e.notify("#%d: every PR is released", owner.Number)
}
