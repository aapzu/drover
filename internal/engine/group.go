package engine

import (
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/gh"
	"github.com/aapzu/drover/internal/store"
)

// A session that opens several pull requests is split into one card per PR.
// The cards move independently but share the owner's sessions and worktree;
// the session follows whichever card was last moved into an agent column.

// owner returns the task holding t's sessions. Called with mu held.
func (e *Engine) owner(t *store.Task) *store.Task { return store.Owner(e.state.Tasks, t) }

// sessionColumn is the column whose settings the owner's session follows.
// Called with mu held.
func (e *Engine) sessionColumn(owner *store.Task) config.Column {
	col, _, _ := e.board.Column(store.Driver(e.state.Tasks, owner).ColumnID)
	return col
}

func isAgentColumn(c config.Column) bool { return c.IsAgent() }

// workColumn is where the work happens: the first agent column that isn't a
// plan column.
func workColumn(board *config.Board) (config.Column, bool) {
	for _, c := range board.Columns {
		if isAgentColumn(c) && board.PermissionMode(c) != "plan" {
			return c, true
		}
	}
	return config.Column{}, false
}

// firstAgentColumn is where a card's work starts: the first agent column.
func firstAgentColumn(board *config.Board) (config.Column, bool) {
	for _, c := range board.Columns {
		if isAgentColumn(c) {
			return c, true
		}
	}
	return config.Column{}, false
}

// lockGroup takes the lock shared by every card of id's group and returns the
// card, its owner and the unlock function. card is nil if id is unknown.
func (e *Engine) lockGroup(id string) (card, owner *store.Task, unlock func()) {
	e.mu.Lock()
	ownerID := id
	if t := e.find(id); t != nil {
		ownerID = e.owner(t).ID
	}
	e.mu.Unlock()
	unlock = e.lockTask(ownerID)
	e.mu.Lock()
	defer e.mu.Unlock()
	card = e.find(id)
	if card != nil {
		owner = e.owner(card)
	}
	return card, owner, unlock
}

// startRef is where a PR's card goes once it is taken up: the reviewFlow's
// started column for a PR you review, else the prFlow's opened column. It is
// empty when that flow doesn't say.
func (e *Engine) startRef(review bool) string {
	switch {
	case review && e.board.ReviewFlow != nil:
		return e.board.ReviewFlow.Started
	case !review && e.board.PRFlow != nil:
		return e.board.PRFlow.Opened
	}
	return ""
}

// addPRs records pull requests the owner's session opened. The first goes on
// the owner's own card, which moves to the prFlow's opened column once the
// agent's turn ends. Each further PR becomes its own card in the group, in
// the opened column, else in the column of the card the session works for.
// With review set the PRs are ones you asked the agent to review. They are
// marked as such and use the reviewFlow's started column instead. The owner's
// card takes the first only while it is still in To Do, and a further card
// starts in that column straight away, before gh has confirmed the PR isn't
// yours; if it is yours, the card is removed again.
// Called with mu held.
func (e *Engine) addPRs(owner *store.Task, urls []string, review bool) {
	members := store.Members(e.state.Tasks, owner)
	column := store.Driver(e.state.Tasks, owner).ColumnID
	hasTarget := false
	if c, ok := e.board.Find(e.startRef(review)); ok {
		column, hasTarget = c.ID, true
	}
	logText, verb := "Opened PR #%d", "opened"
	if review {
		logText, verb = "Reviewing PR #%d", "is reviewing"
	}
	col, _, _ := e.board.Column(owner.ColumnID)
	if owner.PR == nil && len(members) == 1 && len(urls) > 0 && (!review || col.IsTodo()) {
		n := gh.Number(urls[0])
		owner.PR = &store.PR{URL: urls[0], Number: n, Review: review}
		owner.PRMovePending = hasTarget
		owner.AddLog(fmt.Sprintf(logText, n))
		e.notify("#%d %s PR #%d", owner.Number, verb, n)
		urls = urls[1:]
	}
	for _, url := range urls {
		if slices.Contains(owner.SeenPRs, url) ||
			slices.ContainsFunc(members, func(m *store.Task) bool { return m.PR != nil && m.PR.URL == url }) {
			continue
		}
		owner.SeenPRs = append(owner.SeenPRs, url)
		now, n := time.Now(), gh.Number(url)
		card := &store.Task{
			ID:        uuid.NewString(),
			Number:    e.state.NextNumber,
			Title:     fmt.Sprintf("%s · PR #%d", owner.Title, n),
			ColumnID:  column,
			PR:        &store.PR{URL: url, Number: n, Review: review},
			GroupID:   owner.ID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if owner.Issue != nil {
			issue := *owner.Issue
			card.Issue = &issue
		}
		e.state.NextNumber++
		card.AddLog(fmt.Sprintf("Split from #%d: its agent %s PR #%d", owner.Number, verb, n))
		owner.AddLog(fmt.Sprintf(logText+", now card #%d", n, card.Number))
		e.state.Tasks = append(e.state.Tasks, card)
		e.notify("#%d %s PR #%d: new card #%d", owner.Number, verb, n, card.Number)
		members = append(members, card)
	}
	e.lastPRPoll = time.Time{}
}

// handOver makes heir the owner of old's group before old is deleted: it
// takes the sessions, worktree and branch, and the other cards follow it.
// Called with mu held.
func (e *Engine) handOver(old, heir *store.Task) {
	for key, s := range old.Sessions {
		if s.Dir == "" {
			s.Dir = e.sessionDir(old, key)
		}
	}
	heir.Sessions, old.Sessions = old.Sessions, nil
	heir.WorktreePath, heir.Branch, heir.BaseBranch = old.WorktreePath, old.Branch, old.BaseBranch
	heir.Visit, heir.SeenPRs = old.Visit, old.SeenPRs
	heir.ActiveCard = old.ActiveCard
	if heir.ActiveCard == old.ID {
		heir.ActiveCard = heir.ID
	}
	heir.GroupID = ""
	for _, t := range e.state.Tasks {
		if t.GroupID == old.ID {
			t.GroupID = heir.ID
		}
	}
	heir.AddLog(fmt.Sprintf("Took over the agent and worktree of deleted #%d", old.Number))
}
