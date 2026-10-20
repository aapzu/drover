package engine

import (
	"errors"
	"fmt"
	"path/filepath"
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
			if flow != nil && flow.Released != "" && !t.PR.Released && t.PR.MergeCommit != "" {
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
		if col, _, _ := e.board.Column(t.ColumnID); t.GroupID != "" || col.IsDone() {
			continue
		}
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

// scanTranscripts adds the PRs sessions created since the last scan.
func (e *Engine) scanTranscripts(scans []prScan) {
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
		return
	}
	var ship []*store.Task
	e.with(func() {
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
				e.addPRs(t, r.urls)
				if t.PRMovePending && agentIdle(t) {
					ship = append(ship, t)
				}
			}
		}
	})
	for _, t := range ship {
		e.shipTask(t)
	}
}

// refreshPR updates an open PR's card from GitHub and moves it along the
// prFlow.
func (e *Engine) refreshPR(id string) {
	e.mu.Lock()
	t := e.find(id)
	if t == nil || t.PR == nil {
		e.mu.Unlock()
		return
	}
	url := t.PR.URL
	e.mu.Unlock()
	info, err := gh.View(e.Root, url)

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
		if pr.Title == "" && info.Title != "" && card.GroupID != "" {
			card.Title = info.Title
		}
		prev := pr.State
		pr.Error = ""
		pr.Title, pr.Branch, pr.State = info.Title, info.Branch, info.State
		pr.ReviewDecision, pr.MergeCommit = info.ReviewDecision, info.MergeOID()
		step = e.prStep(card, owner, prev, info)
	})
	e.applyPRMove(card, owner, step)
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
		message = e.render(card, step.message, groupContext{to: step.to}, "")
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
	if step.to.IsDone() {
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
		if col, _, _ := e.board.Column(m.ColumnID); m.PR != nil && !col.IsDone() {
			return
		}
	}
	e.notify("#%d: every PR is released", owner.Number)
}
