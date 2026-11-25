package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	selfupdate "github.com/aapzu/drover/internal/update"
)

var (
	checkUpdate = func(ctx context.Context, current string) (string, bool) {
		return (&selfupdate.Client{}).Check(ctx, current)
	}
	installUpdate = func(ctx context.Context, tag string) error {
		exe, err := selfupdate.Executable()
		if err != nil {
			return err
		}
		return (&selfupdate.Client{}).Install(ctx, tag, exe)
	}
)

type updateAvailableMsg string
type updatingMsg struct{}
type updateDoneMsg struct {
	tag string
	err error
}

// WithVersion tells the board which release it is, so it can look for a newer
// one. Without a release version it never checks.
func (m Model) WithVersion(version string) Model {
	m.version = version
	return m
}

func (m Model) checkUpdateCmd() tea.Cmd {
	if m.snap.Board.DisableUpdateCheck || !selfupdate.Releasable(m.version) {
		return nil
	}
	current := m.version
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if tag, ok := checkUpdate(ctx, current); ok {
			return updateAvailableMsg(tag)
		}
		return nil
	}
}

func (m *Model) confirmUpdate() {
	if m.updating {
		m.setFlash("Already updating drover…")
		return
	}
	if m.updateTag == "" {
		m.setFlash("drover is up to date")
		return
	}
	tag := m.updateTag
	m.mode = modeConfirm
	m.confirmText = fmt.Sprintf("Update drover from %s to %s? Restart the board afterwards to use it. (y/N)", m.version, tag)
	m.confirmAction = func() tea.Cmd {
		install := func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			return updateDoneMsg{tag: tag, err: installUpdate(ctx, tag)}
		}
		return tea.Sequence(func() tea.Msg { return updatingMsg{} }, install)
	}
}

func (m Model) updateFinished(msg updateDoneMsg) Model {
	m.updating = false
	if msg.err != nil {
		m.setFlash("Update failed: " + msg.err.Error())
		return m
	}
	m.updateTag = ""
	m.setFlash(fmt.Sprintf("Updated drover to %s. Restart the board to use it.", msg.tag))
	return m
}
