// Package assistant answers questions about a board and changes it on request,
// through a headless Claude session that drives the drover CLI.
package assistant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
)

// The session file holds the id of the conversation every question of a project
// continues.
const (
	model       = "sonnet"
	askTimeout  = 10 * time.Minute
	sessionFile = "assistant-session"
	lockFile    = "assistant.lock"
)

const maxErrorLen = 600

// runClaude is replaced in tests.
var runClaude = func(ctx context.Context, dir string, argv []string, stdin string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	// A drover command the assistant started can hold claude's pipes open after claude is killed.
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "", errors.New("the claude CLI must be on PATH")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("no answer after %s; the board may have been partly changed", askTimeout)
	case err != nil:
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(string(out))
		}
		if len(detail) > maxErrorLen {
			detail = strings.ToValidUTF8(detail[:maxErrorLen], "") + "…"
		}
		if detail == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, detail)
	}
	return string(out), nil
}

// Ask puts question to the project's assistant, which runs the drover binary at
// exe to read and change the board, and returns its answer. fresh forgets the
// conversation first.
//
// When the answer is fine but the conversation could not be saved, the answer
// comes back with an error saying the next question starts a new one.
func Ask(ctx context.Context, root, exe, question string, fresh bool) (string, error) {
	unlock, err := lock(root)
	if err != nil {
		return "", err
	}
	defer unlock()

	board, _, err := config.Load(root)
	if err != nil {
		return "", fmt.Errorf("load the board: %w", err)
	}
	if fresh {
		if err := forget(root); err != nil {
			return "", err
		}
	}
	id, resume := load(root)
	if !resume {
		id = uuid.NewString()
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	out, err := runClaude(ctx, root, argv(exe, id, resume, systemPrompt(exe, root, board)), question)
	if err != nil {
		if resume {
			err = fmt.Errorf("%w (to start a new conversation: drover ask --new, or ctrl+n on the board)", err)
		}
		return "", fmt.Errorf("the assistant failed: %w", err)
	}
	answer := strings.TrimSpace(out)
	if answer == "" {
		return "", errors.New("the assistant returned no answer")
	}
	if !resume {
		if err := save(root, id); err != nil {
			return answer, fmt.Errorf("could not save the conversation, so the next question starts a new one: %w", err)
		}
	}
	return answer, nil
}

// Reset makes the next question start a new conversation. It waits for a
// question in progress.
func Reset(root string) error {
	unlock, err := lock(root)
	if err != nil {
		return err
	}
	defer unlock()
	return forget(root)
}

func forget(root string) error {
	if err := os.Remove(filepath.Join(store.Dir(root), sessionFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func argv(exe, id string, resume bool, prompt string) []string {
	allow, deny := rules(exe)
	args := []string{"claude", "-p", "--model", model, "--append-system-prompt", prompt, "--tools", "Bash", "--allowedTools"}
	args = append(args, allow...)
	args = append(args, "--disallowedTools")
	args = append(args, deny...)
	if resume {
		return append(args, "--resume", id)
	}
	return append(args, "--session-id", id)
}

// rules lists the Bash commands the assistant may run: the board's own CLI,
// without the forced removal that discards uncommitted work. Each rule exists
// quoted and bare, since the model may drop the quotes the prompt asks for.
func rules(exe string) (allow, deny []string) {
	for _, bin := range []string{claude.ShellQuote(exe), exe} {
		for _, sub := range []string{"list", "move", "add", "edit", "rm"} {
			allow = append(allow, fmt.Sprintf("Bash(%s %s *)", bin, sub), fmt.Sprintf("Bash(%s %s)", bin, sub))
		}
		deny = append(deny, fmt.Sprintf("Bash(%s rm *-force*)", bin))
	}
	return allow, deny
}

// systemPrompt is the --append-system-prompt text that makes a Claude session the
// assistant of the board at root; exe is the drover binary it should run.
func systemPrompt(exe, root string, board *config.Board) string {
	cmd := claude.ShellQuote(exe)
	return "You are the assistant of a drover kanban board: cards move through columns, and entering an agent column starts a Claude Code agent in the card's own git worktree. " +
		"The board belongs to the git repository " + filepath.Base(root) + " at " + root + baseBranch(board) + ".\n" +
		"Its columns, in order, are:\n" + describeColumns(board) + "\n" +
		"You answer questions about the board and change it when asked. You work only through the drover CLI, run as `" + cmd + " <command>`.\n" +
		"`" + cmd + " list --json` shows every card: number, title, column, branch, worktree, issue, pull request and what its agent is doing. " +
		"Run it before every answer that depends on the cards. The board changes while you talk, so never rely on an earlier run.\n" +
		"`" + cmd + " move <number> <column>` moves a card; columns are ids or names, and an unknown one is refused with the valid ones. " +
		"`add <title> [-p prompt] [-c column]`, `edit <number> [--title t] [--prompt p]` and `rm <number>` change cards. " +
		"A move into an agent column starts an agent, and into a column without one stops it.\n" +
		"Change the board only when asked. Before `rm`, or a move to the To Do column, say what you are about to do and wait for the user to agree: " +
		"a move to To Do deletes the card's worktree even with uncommitted changes. `rm` refuses while the worktree has uncommitted changes; " +
		"report that instead of working around it, and never pass --force.\n" +
		"Answer briefly, in plain text without markdown headings. Always write cards as #<number> and their title, even in short answers."
}

func baseBranch(b *config.Board) string {
	if b.DefaultBaseBranch == "" {
		return ""
	}
	return ", and worktrees branch from " + b.DefaultBaseBranch + " by default"
}

// describeColumns lists one line per column: its name, id and what entering it does.
func describeColumns(b *config.Board) string {
	lines := make([]string, len(b.Columns))
	for i, c := range b.Columns {
		what := "no agent runs here"
		switch {
		case c.IsTodo():
			what = "no agent; a card moved here loses its worktree and conversation"
		case c.IsDone():
			what = "finished cards; the agent stops and the worktree is kept until the card is archived"
		case c.IsAgent():
			what = "an agent works on the card"
			if c.PermissionMode != "" {
				what += " in " + c.PermissionMode + " mode"
			}
		}
		lines[i] = fmt.Sprintf("- %s (id %s): %s", c.Name, c.ID, what)
	}
	return strings.Join(lines, "\n")
}

func load(root string) (id string, ok bool) {
	data, err := os.ReadFile(filepath.Join(store.Dir(root), sessionFile))
	if err != nil {
		return "", false
	}
	id = strings.TrimSpace(string(data))
	_, err = uuid.Parse(id)
	return id, err == nil
}

func save(root, id string) error {
	return os.WriteFile(filepath.Join(store.Dir(root), sessionFile), []byte(id+"\n"), 0o644)
}

// lock blocks until no other process is using the project's conversation.
func lock(root string) (unlock func(), err error) {
	dir := store.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
