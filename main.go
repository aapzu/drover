// Command drover is a terminal front end for Kangentic-style agent
// boards: drag a card, an agent starts.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/gitx"
	"github.com/aapzu/drover/internal/tmux"
	"github.com/aapzu/drover/internal/ui"
)

const usage = `drover: a kanban board for AI coding agents, in your terminal.

Usage:
  drover [project-dir]          open the board for a git repository (default: .)
  drover hook <file>            internal: record a Claude Code hook event

The board is read from kangentic.json at the repo root, the same file the
Kangentic desktop app uses. Agents run in tmux and keep running after you quit.
`

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "hook" {
		if len(args) == 2 {
			claude.RecordHook(args[1], os.Stdin)
		}
		return
	}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Print(usage)
		return
	}
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	if err := run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "drover:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	if err := tmux.Available(); err != nil {
		return fmt.Errorf("tmux is required to run agents: %w", err)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("the claude CLI must be on PATH: %w", err)
	}
	root, err := projectRoot(dir)
	if err != nil {
		return err
	}
	eng, err := engine.New(root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_, err = tea.NewProgram(ui.New(eng), tea.WithAltScreen()).Run()
	return err
}

// projectRoot resolves the repository root, stepping out of a task worktree
// so the board always opens on the main checkout.
func projectRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if top, err := gitx.Toplevel(abs); err == nil {
		abs = top
	}
	if i := strings.Index(abs, string(filepath.Separator)+filepath.Join(".kangentic", "worktrees")); i >= 0 {
		abs = abs[:i]
	}
	return abs, nil
}
