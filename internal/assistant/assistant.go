// Package assistant answers questions about a board and its repository, and
// changes the board on request, through a Claude session that drives the drover
// CLI and whose tools may only read the repository.
package assistant

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/aapzu/drover/internal/tmux"
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

var (
	hasSession    = tmux.Has
	newSession    = tmux.NewSession
	stopSession   = tmux.Stop
	hasTranscript = func(root, id string) (bool, error) {
		_, err := os.Stat(claude.TranscriptPath(id, root))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
)

func SessionName(root string) string {
	return tmux.SafeName(filepath.Base(store.Dir(root))) + "-assistant"
}

func SessionID(root string) string {
	id, _ := loadID(root)
	return id
}

// running reports whether the project's side panel has a Claude session, which
// lasts until Claude exits or the board that created it quits, hidden or not.
func running(root string) bool { return hasSession(SessionName(root)) }

func killCommand(name string) string {
	return fmt.Sprintf("tmux -L %s kill-session -t %s", tmux.Socket, name)
}

func stopAndVerify(root string) error {
	name := SessionName(root)
	stopSession(name)
	if hasSession(name) {
		return fmt.Errorf("could not stop the assistant's tmux session %s (to end the session: %s)", name, killCommand(name))
	}
	return nil
}

// Stop waits up to stopWait for another process holding the conversation, then
// stops the session whether or not it got the lock.
func Stop(root string) error {
	unlock, lockErr := lockFor(root, stopWait)
	if lockErr == nil {
		defer unlock()
	} else if errors.Is(lockErr, errBusy) {
		lockErr = nil
	}
	return errors.Join(lockErr, stopAndVerify(root))
}

var errNoBoard = errors.New("there is no board to describe to the assistant")

var errBusy = errors.New("the assistant is busy (drover ask is answering, or another board is starting, restarting or stopping it); try again in a moment, or after drover ask finishes")

var errPanelOpen = errors.New("the board's assistant session is running; use the panel on the board, or quit the board first")

func panelOpenErr(root string) error {
	return fmt.Errorf("%w (to end the session: %s)", errPanelOpen, killCommand(SessionName(root)))
}

// repoTools are the tools the assistant has besides running commands; none of them can change a file.
var repoTools = []string{"Read", "Grep", "Glob"}

// gitReadOnly are the git subcommands the assistant may run.
var gitReadOnly = []string{"status", "log", "show", "diff", "blame", "ls-files", "ls-tree", "shortlog", "rev-parse", "describe", "cat-file"}

// gitDenied are known examples, not a complete list, of options that make an
// otherwise read-only subcommand write a file, run a program or read outside the
// repo.
var gitDenied = []string{"git *--output*", "git *--ext-diff*", "git *--textconv*", "git *--no-index*", "git blame *--cont*"}

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
// conversation first. It refuses while the board's assistant session runs.
//
// When the answer is fine but the conversation could not be saved, the answer
// comes back with an error saying the next question starts a new one.
func Ask(ctx context.Context, root, exe, question string, fresh bool) (string, error) {
	unlock, err := lock(root)
	if err != nil {
		return "", err
	}
	defer unlock()
	if running(root) {
		return "", panelOpenErr(root)
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
	out, err := runClaude(ctx, root, append([]string{"claude", "-p"}, argv(exe, root, id, resume, systemPrompt(exe, root, board))...), question)
	if err != nil {
		if resume {
			err = fmt.Errorf("%w (to start a new conversation: drover ask --new)", err)
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

// Start fails at once while another process holds the conversation. It reports
// created only when it started the session; one already running is left alone.
func Start(root, exe string, board *config.Board) (created bool, err error) {
	if board == nil {
		return false, errNoBoard
	}
	unlock, err := lockFor(root, noWait)
	if err != nil {
		return false, err
	}
	defer unlock()
	if running(root) {
		return false, nil
	}
	if err := launch(root, exe, board); err != nil {
		return false, err
	}
	return true, nil
}

func launch(root, exe string, board *config.Board) error {
	id, resume, err := conversation(root)
	if err != nil {
		return err
	}
	if id == "" {
		id = uuid.NewString()
		if err := save(root, id); err != nil {
			return fmt.Errorf("could not save the conversation: %w", err)
		}
	}
	args := append(argv(exe, root, id, resume, systemPrompt(exe, root, board)), "--permission-mode", "dontAsk")
	return newSession(SessionName(root), root, append([]string{"claude"}, args...), nil)
}

func Restart(root, exe string, board *config.Board) error {
	if board == nil {
		return errNoBoard
	}
	unlock, err := lockFor(root, noWait)
	if err != nil {
		return err
	}
	defer unlock()
	if err := stopAndVerify(root); err != nil {
		return err
	}
	if err := forget(root); err != nil {
		return err
	}
	return launch(root, exe, board)
}

// Reset waits for a question in progress and fails while the panel's session runs.
func Reset(root string) error {
	unlock, err := lock(root)
	if err != nil {
		return err
	}
	defer unlock()
	if running(root) {
		return fmt.Errorf("%w; press N on the board to start over", panelOpenErr(root))
	}
	return forget(root)
}

func sessionPath(root string) string {
	return filepath.Join(store.Dir(root), sessionFile)
}

func forget(root string) error {
	if err := os.Remove(sessionPath(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func argv(exe, root, id string, resume bool, prompt string) []string {
	allow, deny := rules(exe)
	args := []string{"--model", model, "--append-system-prompt", prompt, "--setting-sources", "", "--settings", settings(root), "--tools", strings.Join(append([]string{"Bash"}, repoTools...), ","), "--allowedTools"}
	args = append(args, allow...)
	args = append(args, "--disallowedTools")
	args = append(args, deny...)
	if resume {
		return append(args, "--resume", id)
	}
	return append(args, "--session-id", id)
}

// settings returns the --settings JSON.
func settings(root string) string {
	data, _ := json.Marshal(map[string]any{
		"env": map[string]string{"GIT_OPTIONAL_LOCKS": "0"},
		"sandbox": map[string]any{
			"filesystem": map[string][]string{
				"denyWrite":  {root},
				"allowWrite": {store.Dir(root)},
			},
		},
	})
	return string(data)
}

// rules lists what the assistant may do: read the repo, run read-only git
// commands, and run the board's own CLI without the forced removal that discards
// uncommitted work.
func rules(exe string) (allow, deny []string) {
	for _, sub := range gitReadOnly {
		allow = append(allow, fmt.Sprintf("Bash(git %s *)", sub), fmt.Sprintf("Bash(git %s)", sub))
	}
	for _, pattern := range gitDenied {
		deny = append(deny, fmt.Sprintf("Bash(%s)", pattern))
	}
	deny = append(deny, "Edit", "Write", "NotebookEdit")
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
	return "You are the assistant of a drover kanban board: cards move through columns, and entering an agent column starts a Claude Code agent in the card's own git worktree, or in the project root until the card has one when the column skips it (noWorktree). " +
		"The board belongs to the git repository " + filepath.Base(root) + " at " + root + baseBranch(board) + ".\n" +
		"Its columns, in order, are:\n" + describeColumns(board) + "\n" +
		"You answer questions about the board and about the repository (how something works, where a behavior lives, what a package does), and change the board when asked. " +
		"You work on the board only through the drover CLI, run as `" + cmd + " <command>`.\n" +
		"The repository is read-only to you: read and search it with Read, Grep and Glob, and its history with git, using only these subcommands: " + strings.Join(gitReadOnly, ", ") + ". " +
		"What you read is the main checkout; each card's own work is in its worktree, whose path `list --json` gives and which you can read when it is inside the repository. " +
		"You cannot edit files, run other shell commands, or change branches or worktrees, so never try.\n" +
		"When the user wants a change to the code, say that you can't make it and offer a card for it, with a clear title and a prompt that says what to change and why. " +
		"Run `add` only after the user agrees to the card.\n" +
		"`" + cmd + " list --json` shows every card: number, title, column, branch, worktree, issue, pull request and what its agent is doing. " +
		"Run it before every answer that depends on the cards. The board changes while you talk, so never rely on an earlier run.\n" +
		"A card's `pr` has the `url`, `number`, `state` (open, merged, closed or unknown), `reviewDecision` (approved or changes requested), `released` (the merge reached the release branch), `reviewing` (a pull request you were asked to review, not one you opened) and `error` of its pull request. " +
		"A card without `pr` has no pull request the board has seen. " +
		"It is as the board last saw it: the board polls open pull requests about once a minute while it runs, and never polls a merged or closed one again, nor any card in a done column. " +
		"It is the only source for whether a pull request is merged or closed: never answer that from git history or merge commits, never run `gh`, and never ask the user to. " +
		"If a `state` is unknown, or an `error` is set, say the board doesn't know yet or may be out of date instead of guessing.\n" +
		moveOffer(board) +
		"`" + cmd + " move <number> <column>` moves a card; columns are ids or names, and an unknown one is refused with the valid ones. " +
		"`add <title> [-p prompt] [-c column]`, `edit <number> [--title t] [--prompt p]` and `rm <number>` change cards. " +
		"`add` puts the card in the first column and starts no agent: its `agent` reads `not started` until the user starts one. " +
		"A move into an agent column starts an agent, and into a column without one stops it.\n" +
		"Change the board only when asked. Before `rm`, say what you are about to do and wait for the user to agree. " +
		"`rm` refuses while the worktree has uncommitted changes; " +
		"report that instead of working around it, and never pass --force.\n" +
		"Answer briefly, in plain text without markdown headings, and always write cards as #<number> and their title."
}

func baseBranch(b *config.Board) string {
	if b.DefaultBaseBranch == "" {
		return ""
	}
	return ", and worktrees branch from " + b.DefaultBaseBranch + " by default"
}

// moveOffer is the prompt sentence about cards whose pull request has merged or
// closed, or "" when the board has no PR-opened or done column to name. A merged
// card goes where prFlow.merged says, since the board moves it there itself.
func moveOffer(b *config.Board) string {
	if b.PRFlow == nil {
		return ""
	}
	opened, ok := b.Find(b.PRFlow.Opened)
	if !ok {
		return ""
	}
	var done config.Column
	for _, c := range b.Columns {
		if c.IsDone() {
			done = c
			break
		}
	}
	if done.Name == "" {
		return ""
	}
	merged, ok := b.Find(b.PRFlow.Merged)
	if !ok || merged.ID == done.ID {
		return "When a card in " + opened.Name + " has a merged or closed pull request that isn't `reviewing`, say so and offer to move it to " + done.Name + ", and move it only after the user agrees.\n"
	}
	return "When a card in " + opened.Name + " has a merged or closed pull request that isn't `reviewing`, say so and offer to move a merged one to " + merged.Name + " and a closed one to " + done.Name + ", and move it only after the user agrees.\n"
}

// describeColumns lists one line per column: its name, id and what entering it does.
func describeColumns(b *config.Board) string {
	lines := make([]string, len(b.Columns))
	for i, c := range b.Columns {
		what := "no agent runs here"
		switch {
		case c.IsDone():
			what = "finished cards; the agent stops and the worktree is kept until the card is archived"
		case c.KeepAgent:
			what = "a running agent is kept instead of suspended, and none starts here"
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

func loadID(root string) (string, error) {
	data, err := os.ReadFile(sessionPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the saved conversation: %w", err)
	}
	if id := strings.TrimSpace(string(data)); uuid.Validate(id) == nil {
		return id, nil
	}
	return "", nil
}

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
	path := sessionPath(root)
	tmp := path + ".tmp"
	err := os.WriteFile(tmp, []byte(id+"\n"), 0o644)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

const (
	stopWait    = 3 * time.Second
	noWait      = time.Duration(0)
	waitForever = time.Duration(-1)
)

// lock blocks until no other process is using the project's conversation.
func lock(root string) (unlock func(), err error) { return lockFor(root, waitForever) }

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
