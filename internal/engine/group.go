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

func isAgentColumn(c config.Column) bool { return c.AutoSpawn && !c.IsTodo() && !c.IsDone() }

// workColumn is where the work happens: the first agent column that is
// neither a plan column nor runs its own session.
func workColumn(board *config.Board) (config.Column, bool) {
	for _, c := range board.Columns {
		if isAgentColumn(c) && board.PermissionMode(c) != "plan" && !c.Isolated() {
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

// addPRs records pull requests the owner's session opened. Each becomes its
// own card in the group, in the prFlow's opened column, else in the column of
// the card the session works for. Called with mu held.
func (e *Engine) addPRs(owner *store.Task, urls []string) {
	members := store.Members(e.state.Tasks, owner)
	column := store.Driver(e.state.Tasks, owner).ColumnID
	if f := e.board.PRFlow; f != nil {
		if c, ok := e.board.Find(f.Opened); ok {
			column = c.ID
		}
	}
	for _, url := range urls {
		if slices.ContainsFunc(members, func(m *store.Task) bool { return m.PR != nil && m.PR.URL == url }) {
			continue
		}
		now, n := time.Now(), gh.Number(url)
		card := &store.Task{
			ID:        uuid.NewString(),
			Number:    e.state.NextNumber,
			Title:     fmt.Sprintf("%s · PR #%d", owner.Title, n),
			ColumnID:  column,
			PR:        &store.PR{URL: url, Number: n},
			GroupID:   owner.ID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if owner.Issue != nil {
			issue := *owner.Issue
			card.Issue = &issue
		}
		e.state.NextNumber++
		card.AddLog(fmt.Sprintf("Split from #%d: its agent opened PR #%d", owner.Number, n))
		owner.AddLog(fmt.Sprintf("Opened PR #%d, now card #%d", n, card.Number))
		e.state.Tasks = append(e.state.Tasks, card)
		e.notify("#%d opened PR #%d: new card #%d", owner.Number, n, card.Number)
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
	heir.Visit = old.Visit
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
