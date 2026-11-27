package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	selfupdate "github.com/aapzu/drover/internal/update"
)

const lookupTimeout = 10 * time.Second

var (
	checkUpdate = func(ctx context.Context, current string) (string, bool) {
		return (&selfupdate.Client{}).Check(ctx, current)
	}
	latestRelease = func(ctx context.Context) (string, error) {
		return (&selfupdate.Client{}).Latest(ctx)
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
type restartMsg struct{}
type updateDoneMsg struct {
	tag string
	err error
}

// latestReleaseMsg is what asking GitHub for the newest release found. install
// tells whether it was asked for to install it, or only to report it.
type latestReleaseMsg struct {
	tag     string
	err     error
	install bool
}

// WithVersion tells the board which release it is, so it can look for a newer
// one. Without a release version it never checks.
func (m Model) WithVersion(version string) Model {
	m.version = version
	return m
}

// checkUpdateCmd is the check at startup, which asks GitHub at most once a day.
func (m Model) checkUpdateCmd() tea.Cmd {
	if m.snap.Board.DisableUpdateCheck || !selfupdate.Releasable(m.version) {
		return nil
	}
	current := m.version
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		defer cancel()
		if tag, ok := checkUpdate(ctx, current); ok {
			return updateAvailableMsg(tag)
		}
		return nil
	}
}

// lookUpLatest asks GitHub for the newest release, never the daily cache.
func (m *Model) lookUpLatest(install bool) tea.Cmd {
	if m.updating {
		m.setFlash("Already updating drover…")
		return nil
	}
	if !selfupdate.Releasable(m.version) {
		m.setFlash("This is a development build; install a release to update")
		return nil
	}
	m.setFlash("Looking for a newer drover…")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		defer cancel()
		tag, err := latestRelease(ctx)
		return latestReleaseMsg{tag: tag, err: err, install: install}
	}
}

func (m Model) latestReleaseFound(msg latestReleaseMsg) Model {
	if msg.err != nil {
		m.setFlash("Couldn't look for updates: " + msg.err.Error())
		return m
	}
	if !selfupdate.Newer(m.version, msg.tag) {
		m.updateTag = ""
		m.setFlash(fmt.Sprintf("drover %s is up to date", m.version))
		return m
	}
	if msg.tag == m.installedTag {
		m.setFlash(msg.tag + " is already installed. Restart the board to use it")
		return m
	}
	m.updateTag = msg.tag
	if !msg.install || m.updating || m.mode != modeBoard {
		m.setFlash(msg.tag + " is available. Press U to install it")
		return m
	}
	m.flash = ""
	m.confirmUpdate(msg.tag)
	return m
}

func (m *Model) confirmUpdate(tag string) {
	m.mode = modeConfirm
	m.confirmText = fmt.Sprintf("Update drover from %s to %s? (y/N)", m.version, tag)
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
	m.installedTag = msg.tag
	m.setFlash(fmt.Sprintf("Updated drover to %s. Restart the board to use it.", msg.tag))
	if m.mode == modeBoard {
		m.mode = modeConfirm
		m.confirmText = fmt.Sprintf("Updated drover to %s. Restart the board now? (y/N)", msg.tag)
		m.confirmAction = func() tea.Cmd { return func() tea.Msg { return restartMsg{} } }
	}
	return m
}
