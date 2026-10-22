// Package assistant answers questions about a board and changes it on request,
// through a Claude session that drives the drover CLI: interactive in a tmux
// session for the board's side panel, headless for drover ask.
package assistant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

const (
	model       = "sonnet"
	askTimeout  = 10 * time.Minute
	sessionFile = "assistant-session"
	lockFile    = "assistant.lock"
)

const maxErrorLen = 600

var (
	hasSession    = tmux.Has
	newSession    = tmux.NewSession
	stopSession   = tmux.Stop
	hasTranscript = func(root, id string) (bool, error) {
		_, err := os.Stat(claude.TranscriptPath(id, root))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
)

var (
	nonName      = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	sessionNames sync.Map
)

// SessionName is the tmux session of the project's side panel.
func SessionName(root string) string {
	if name, ok := sessionNames.Load(root); ok {
		return name.(string)
	}
	name := nonName.ReplaceAllString(filepath.Base(store.Dir(root)), "-") + "-assistant"
	sessionNames.Store(root, name)
	return name
}

// SessionID is the saved conversation's id, or "" before there is one.
func SessionID(root string) string {
	id, _ := load(root)
	return id
}

// Running reports whether the project's side panel has a Claude session, which
// lasts until the board quits, hidden or not.
func Running(root string) bool { return hasSession(SessionName(root)) }

// Stop ends the side panel's session, after a Start or Restart in progress, and
// reports a session that is still there. The conversation can be resumed.
func Stop(root string) error {
	if unlock, err := lockFor(root, stopWait); err == nil {
		defer unlock()
	}
	name := SessionName(root)
	stopSession(name)
	if hasSession(name) {
		return fmt.Errorf("could not stop the assistant's tmux session %s (tmux -L %s kill-session -t %s)", name, tmux.Socket, name)
	}
	return nil
}

var errBusy = errors.New("drover ask is answering a question; open the assistant when it has answered")

var errPanelOpen = errors.New("the board's assistant session is running; ask it in the board's panel, or quit the board first")

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
	if Running(root) {
		return "", errPanelOpen
	}

	board, _, err := config.Load(root)
	if err != nil {
		return "", fmt.Errorf("load the board: %w", err)
	}
	if fresh {
		if err := forget(root); err != nil {
			return "", err
		}
	}
	id, resume, err := conversation(root)
	if err != nil {
		return "", err
	}
	if !resume {
		id = uuid.NewString()
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	out, err := runClaude(ctx, root, append([]string{"claude", "-p"}, argv(exe, id, resume, systemPrompt(exe, root, board))...), question)
	if err != nil {
		if resume {
			err = fmt.Errorf("%w (to start a new conversation: drover ask --new, or from the board's command palette)", err)
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

// Start opens the side panel's Claude session in tmux unless one is already
// running. It resumes the project's conversation once that has a transcript, and
// starts it under the saved id before. exe is the drover binary it runs.
func Start(root, exe string, board *config.Board) error {
	unlock, err := lockFor(root, 0)
	if err != nil {
		return err
	}
	defer unlock()
	if Running(root) {
		return nil
	}
	return start(root, exe, board)
}

func start(root, exe string, board *config.Board) error {
	id, resume, err := conversation(root)
	if err != nil {
		return err
	}
	if !resume && !validID(id) {
		id = uuid.NewString()
		if err := save(root, id); err != nil {
			return fmt.Errorf("could not save the conversation: %w", err)
		}
	}
	args := append(argv(exe, id, resume, systemPrompt(exe, root, board)), "--permission-mode", "dontAsk")
	return newSession(SessionName(root), root, append([]string{"claude"}, args...), nil)
}

// Restart ends the panel's session, forgets the conversation and starts a new one.
func Restart(root, exe string, board *config.Board) error {
	unlock, err := lockFor(root, 0)
	if err != nil {
		return err
	}
	defer unlock()
	stopSession(SessionName(root))
	if err := forget(root); err != nil {
		return err
	}
	return start(root, exe, board)
}

// Reset makes the next question start a new conversation. It waits for a
// question in progress and fails while the panel's session runs.
func Reset(root string) error {
	unlock, err := lock(root)
	if err != nil {
		return err
	}
	defer unlock()
	if Running(root) {
		return errPanelOpen
	}
	return forget(root)
}

func forget(root string) error {
	if err := os.Remove(filepath.Join(store.Dir(root), sessionFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// argv is what follows `claude` for both the panel and drover ask.
func argv(exe, id string, resume bool, prompt string) []string {
	allow, deny := rules(exe)
	args := []string{"--model", model, "--append-system-prompt", prompt, "--tools", "Bash", "--allowedTools"}
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
		"Answer briefly, in plain text without markdown headings. Refer to cards as #<number> with their title."
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

// loadID returns the saved conversation id, or "" when there is none or the
// file holds something else. A file that can't be read is an error.
func loadID(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(store.Dir(root), sessionFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the saved conversation: %w", err)
	}
	if id := strings.TrimSpace(string(data)); validID(id) {
		return id, nil
	}
	return "", nil
}

// load is loadID for callers that treat an unreadable file as no conversation.
func load(root string) (id string, ok bool) {
	id, _ = loadID(root)
	return id, id != ""
}

func validID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

// conversation is the saved conversation id.
func conversation(root string) (id string, resume bool, err error) {
	if id, err = loadID(root); err != nil || id == "" {
		return "", false, err
	}
	resume, err = hasTranscript(root, id)
	if err != nil {
		return "", false, fmt.Errorf("look for the conversation's transcript: %w", err)
	}
	return id, resume, nil
}

func save(root, id string) error {
	path := filepath.Join(store.Dir(root), sessionFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

const stopWait = 3 * time.Second

// lock blocks until no other process is using the project's conversation.
func lock(root string) (unlock func(), err error) { return lockFor(root, -1) }

// lockFor takes the project's conversation lock, waiting up to wait for it: 0
// fails at once with errBusy, a negative wait has no limit.
func lockFor(root string, wait time.Duration) (unlock func(), err error) {
	dir := store.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if wait >= 0 {
		how |= syscall.LOCK_NB
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), how)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK) && time.Now().Before(deadline):
			time.Sleep(20 * time.Millisecond)
			continue
		}
		break
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return nil, errBusy
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
