package engine

import (
	"errors"
	"fmt"
	"os"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/gitx"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

// shellName is the tmux session of the shell you open on the owner's card.
func (e *Engine) shellName(owner *store.Task) string { return e.tmuxName(owner) + "-sh" }

// Shell returns a tmux session running your shell in the card's worktree, or
// in the main checkout before it has one, and starts it if needed.
func (e *Engine) Shell(id string) (string, error) {
	e.mu.Lock()
	card := e.find(id)
	if card == nil {
		e.mu.Unlock()
		return "", errors.New("task not found")
	}
	owner := e.owner(card)
	name, dir := e.shellName(owner), owner.WorktreePath
	e.mu.Unlock()
	if tmux.Has(name) {
		return name, nil
	}
	if dir == "" {
		dir = e.Root
	} else if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("the worktree is missing: %w", err)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "sh"
	}
	return name, tmux.NewSession(name, dir, []string{shell}, nil)
}

// UpdateFromBase fetches the card's base branch, or uses the local one
// without an origin or when fetching fails, and fast-forwards its worktree to it. If the branch has
// commits of its own, a running agent is asked to merge instead. It returns
// what happened.
func (e *Engine) UpdateFromBase(id string) (string, error) {
	card, owner, unlock := e.lockGroup(id)
	defer unlock()
	if card == nil {
		return "", errors.New("task not found")
	}
	e.mu.Lock()
	path, base, fallback := owner.WorktreePath, owner.BaseBranch, e.board.DefaultBaseBranch
	copied := e.board.CopyFiles
	s := owner.Sessions[store.MainSession]
	running := s != nil && s.Status == store.StatusRunning
	working := running && s.Activity != store.ActivityIdle
	e.mu.Unlock()
	if path == "" || !e.isRepo {
		return "", errors.New("this card has no worktree yet")
	}
	if working {
		return "", errors.New("the agent is working; update once it's idle")
	}
	if gitx.Dirty(path, copied...) {
		return "", errors.New("the worktree has uncommitted changes")
	}
	base, err := gitx.ResolveBase(e.Root, base, fallback)
	if err != nil {
		return "", err
	}
	ref, name := base, base
	if gitx.HasOrigin(e.Root) {
		if fetched, err := gitx.Fetch(e.Root, base); err == nil {
			ref, name = fetched, "origin/"+base
		} else {
			name = fmt.Sprintf("local %s (fetch failed: %s)", base, claude.FirstLine(err.Error()))
		}
	}
	n, err := gitx.FastForward(path, ref)
	switch {
	case errors.Is(err, gitx.ErrDiverged) && running:
		e.sendMessage(owner, store.MainSession, fmt.Sprintf("Merge %s into this branch, resolve any conflicts, and run the tests.", name))
		e.logf(card, "Asked the agent to merge %s", name)
		return fmt.Sprintf("#%d has commits of its own; asked the agent to merge %s", card.Number, name), nil
	case errors.Is(err, gitx.ErrDiverged):
		return "", fmt.Errorf("the branch has commits of its own; start the agent with r and press u again to have it merge %s", name)
	case err != nil:
		return "", err
	case n == 0:
		return fmt.Sprintf("#%d is up to date with %s", card.Number, name), nil
	}
	e.logf(card, "Fast-forwarded %d commits from %s", n, name)
	return fmt.Sprintf("#%d fast-forwarded %d commits from %s", card.Number, n, name), nil
}
