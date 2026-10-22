// Command drover is a terminal kanban board for coding agents: drag a card,
// an agent starts.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

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

The board is read from drover.json at the repo root. Agents run in tmux and
keep running after you quit.

` + cliUsage

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
	if len(args) > 0 {
		if cmd, ok := cliCommands[args[0]]; ok {
			root, err := projectRoot(".")
			if err == nil {
				err = cmd(root, args[1:])
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "drover:", err)
				os.Exit(1)
			}
			return
		}
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
	stopped := make(chan struct{})
	go func() { eng.Run(ctx); close(stopped) }()

	p := tea.NewProgram(ui.New(eng), tea.WithAltScreen())
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		if _, ok := <-hup; ok {
			p.Quit()
		}
	}()
	final, err := p.Run()
	signal.Stop(hup)
	close(hup)
	cancel()
	<-stopped
	m, ok := final.(ui.Model)
	if ok {
		if err := m.StopAssistant(); err != nil {
			fmt.Fprintln(os.Stderr, "drover:", err)
		}
	}
	if ok && m.SuspendAllRequested() {
		fmt.Println("Suspending all agents…")
		for _, h := range eng.SuspendAll() {
			if h.SessionID == "" {
				fmt.Printf("#%d %s\n  not started yet; press r on the board to start it\n", h.Number, h.Title)
				continue
			}
			fmt.Printf("#%d %s\n  cd %s && claude --resume %s\n", h.Number, h.Title, claude.ShellQuote(h.Dir), h.SessionID)
		}
	}
	return err
}

// projectRoot resolves the repository root, stepping out of any linked
// worktree so the board always opens on the main checkout.
func projectRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if main, err := gitx.MainRoot(abs); err == nil {
		abs = main
	}
	return abs, nil
}
