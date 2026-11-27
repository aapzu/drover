package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeGitHub struct {
	latest           string
	latestErr        error
	lookups          int
	checks, installs []string
}

func fakeUpdates(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{latest: "v1.1.0"}
	origCheck, origLatest, origInstall := checkUpdate, latestRelease, installUpdate
	t.Cleanup(func() { checkUpdate, latestRelease, installUpdate = origCheck, origLatest, origInstall })
	checkUpdate = func(_ context.Context, current string) (string, bool) {
		f.checks = append(f.checks, current)
		return "v1.1.0", true
	}
	latestRelease = func(context.Context) (string, error) {
		f.lookups++
		return f.latest, f.latestErr
	}
	installUpdate = func(_ context.Context, tag string) error {
		f.installs = append(f.installs, tag)
		return nil
	}
	return f
}

func send(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestInitChecksForUpdatesOnlyOnReleases(t *testing.T) {
	f := fakeUpdates(t)
	for _, c := range []struct {
		name, version string
		disabled      bool
		want          bool
	}{
		{"release", "v1.0.0", false, true},
		{"dev build", "", false, false},
		{"pseudo-version", "v1.0.1-0.20260101000000-abcdef123456", false, false},
		{"disabled in drover.json", "v1.0.0", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f.checks = nil
			m := testModel(120, 40, 1, 0).WithVersion(c.version)
			m.snap.Board.DisableUpdateCheck = c.disabled
			cmd := m.checkUpdateCmd()
			if (cmd != nil) != c.want {
				t.Fatalf("check scheduled = %t, want %t", cmd != nil, c.want)
			}
			if cmd == nil {
				return
			}
			if msg := cmd(); msg != updateAvailableMsg("v1.1.0") {
				t.Errorf("msg = %v", msg)
			}
		})
	}
}

func TestUpdateNoticeAndInstall(t *testing.T) {
	f := fakeUpdates(t)
	m := testModel(120, 40, 1, 0).WithVersion("v1.0.0")

	if got := m.renderFooter(); strings.Contains(got, "available") {
		t.Fatalf("footer shows an update before one was found:\n%s", got)
	}
	m = send(m, updateAvailableMsg("v1.1.0"))
	if got := m.renderFooter(); !strings.Contains(got, "v1.1.0 available: U") {
		t.Fatalf("footer doesn't announce the update:\n%s", got)
	}

	m = pressAndResolve(t, m, "U")
	if m.mode != modeConfirm || !strings.Contains(m.confirmText, "v1.0.0 to v1.1.0") {
		t.Fatalf("U didn't ask to confirm: mode %v, %q", m.mode, m.confirmText)
	}
	next, cmd := m.updateConfirm(keyPress("y"))
	m = next.(Model)
	if len(f.installs) != 0 {
		t.Fatal("installed before the command ran")
	}
	msgs := runSequence(cmd)
	if _, ok := msgs[0].(updatingMsg); !ok || len(msgs) != 2 {
		t.Fatalf("messages = %#v, want updatingMsg first, then the result", msgs)
	}
	m = send(m, msgs[0])
	if !strings.Contains(m.renderFooter(), "updating drover") {
		t.Errorf("footer = %q", m.renderFooter())
	}
	again, cmd2 := m.updateBoard(keyPress("U"))
	if got := again.(Model); cmd2 != nil || got.mode != modeBoard || !strings.Contains(got.flash, "Already updating") {
		t.Errorf("U during an install: mode %v, flash %q", got.mode, got.flash)
	}
	done := msgs[1].(updateDoneMsg)
	if len(f.installs) != 1 || f.installs[0] != "v1.1.0" {
		t.Fatalf("installs = %v", f.installs)
	}
	m = send(m, done)
	if m.updateTag != "" || m.mode != modeConfirm || !strings.Contains(m.confirmText, "Restart the board now?") {
		t.Fatalf("after the install: tag %q, mode %v, confirm %q", m.updateTag, m.mode, m.confirmText)
	}
	if m.RestartRequested() {
		t.Fatal("restarted without asking")
	}

	declined, cmd := m.updateConfirm(keyPress("n"))
	if cmd != nil || declined.(Model).RestartRequested() {
		t.Error("declining the restart did something")
	}
	if !strings.Contains(declined.(Model).flash, "Restart the board to use it") {
		t.Errorf("flash after declining = %q", declined.(Model).flash)
	}

	_, cmd = m.updateConfirm(keyPress("y"))
	if cmd == nil {
		t.Fatal("y returned no command")
	}
	msg := cmd()
	if _, ok := msg.(restartMsg); !ok {
		t.Fatalf("y returned %T, want restartMsg", msg)
	}
	next, quit := m.Update(msg)
	if !next.(Model).RestartRequested() {
		t.Error("restartMsg didn't request a restart")
	}
	if quit == nil {
		t.Fatal("restartMsg didn't quit the board")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Errorf("restartMsg returned %T, want tea.QuitMsg", quit())
	}
}

func TestUpdateDoneOutsideTheBoardDoesNotAsk(t *testing.T) {
	m := testModel(120, 40, 1, 0).WithVersion("v1.0.0")
	m.mode = modeForm
	m = send(m, updateDoneMsg{tag: "v1.1.0"})
	if m.mode != modeForm || m.RestartRequested() {
		t.Errorf("took over a form: mode %v, restart %v", m.mode, m.RestartRequested())
	}
	if !strings.Contains(m.flash, "Updated drover to v1.1.0") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestUpdateConfirmCanBeDeclinedAndFailuresAreShown(t *testing.T) {
	f := fakeUpdates(t)
	m := testModel(120, 40, 1, 0).WithVersion("v1.0.0")
	m = send(m, updateAvailableMsg("v1.1.0"))

	next, cmd := pressAndResolve(t, m, "U").updateConfirm(keyPress("n"))
	if cmd != nil || len(f.installs) != 0 || next.(Model).updateTag != "v1.1.0" {
		t.Error("declining changed something")
	}

	m = send(m, updateDoneMsg{tag: "v1.1.0", err: errors.New("disk full")})
	if m.mode != modeBoard || m.RestartRequested() {
		t.Error("a failed install asked to restart")
	}
	if m.updateTag != "v1.1.0" || !strings.Contains(m.flash, "Update failed: disk full") {
		t.Errorf("after a failed install: tag %q, flash %q", m.updateTag, m.flash)
	}
}

// pressAndResolve presses a key that looks up the newest release and feeds the
// answer back to the board, as the program would.
func pressAndResolve(t *testing.T, m Model, key string) Model {
	t.Helper()
	next, cmd := m.updateBoard(keyPress(key))
	if cmd == nil {
		t.Fatalf("%s didn't look anything up; flash %q", key, next.(Model).flash)
	}
	got := cmd()
	msg, ok := got.(latestReleaseMsg)
	if !ok {
		t.Fatalf("%s sent %T, want latestReleaseMsg", key, got)
	}
	return send(next.(Model), msg)
}

func TestInstallLooksUpTheLatestReleaseInsteadOfTrustingTheNotice(t *testing.T) {
	f := fakeUpdates(t)
	m := testModel(120, 40, 1, 0).WithVersion("v1.0.0")

	m = pressAndResolve(t, m, "U")
	if f.lookups != 1 || m.mode != modeConfirm || !strings.Contains(m.confirmText, "v1.0.0 to v1.1.0") {
		t.Errorf("no notice yet: lookups %d, mode %v, %q", f.lookups, m.mode, m.confirmText)
	}

	m = send(testModel(120, 40, 1, 0).WithVersion("v1.0.0"), updateAvailableMsg("v1.0.5"))
	f.latest = "v1.2.0"
	m = pressAndResolve(t, m, "U")
	if !strings.Contains(m.confirmText, "v1.0.0 to v1.2.0") {
		t.Errorf("stale notice won: %q", m.confirmText)
	}
}

func TestInstallWhenUpToDateOrOffline(t *testing.T) {
	f := fakeUpdates(t)
	base := testModel(120, 40, 1, 0).WithVersion("v1.0.0")

	f.latest = "v1.0.0"
	m := pressAndResolve(t, send(base, updateAvailableMsg("v1.1.0")), "U")
	if m.mode != modeBoard || m.updateTag != "" || !strings.Contains(m.flash, "up to date") {
		t.Errorf("up to date: mode %v, tag %q, flash %q", m.mode, m.updateTag, m.flash)
	}

	f.latestErr = errors.New("offline")
	m = pressAndResolve(t, base, "U")
	if m.mode != modeBoard || !strings.Contains(m.flash, "offline") {
		t.Errorf("offline: mode %v, flash %q", m.mode, m.flash)
	}
}

func TestCheckKeyReportsWithoutInstalling(t *testing.T) {
	f := fakeUpdates(t)
	base := testModel(120, 40, 1, 0).WithVersion("v1.0.0")

	m := pressAndResolve(t, base, "V")
	if f.lookups != 1 || m.mode != modeBoard || m.updateTag != "v1.1.0" || !strings.Contains(m.flash, "v1.1.0 is available") {
		t.Errorf("newer: lookups %d, mode %v, tag %q, flash %q", f.lookups, m.mode, m.updateTag, m.flash)
	}
	if len(f.installs) != 0 {
		t.Error("checking installed something")
	}

	f.latest = "v1.0.0"
	m = pressAndResolve(t, m, "V")
	if m.updateTag != "" || !strings.Contains(m.flash, "v1.0.0 is up to date") {
		t.Errorf("up to date: tag %q, flash %q", m.updateTag, m.flash)
	}

	f.latestErr = errors.New("offline")
	m = pressAndResolve(t, base, "V")
	if m.updateTag != "" || !strings.Contains(m.flash, "offline") {
		t.Errorf("offline: tag %q, flash %q", m.updateTag, m.flash)
	}
}

func TestLateInstallLookupDoesNotHijackAnotherModeOrInstallTwice(t *testing.T) {
	fakeUpdates(t)
	base := testModel(120, 40, 1, 0).WithVersion("v1.0.0")
	msg := latestReleaseMsg{tag: "v1.1.0", install: true}

	renaming := base
	renaming.mode = modeRename
	if got := renaming.latestReleaseFound(msg); got.mode != modeRename || got.updateTag != "v1.1.0" {
		t.Errorf("rename hijacked: mode %v, tag %q", got.mode, got.updateTag)
	}

	updating := base
	updating.updating = true
	if got := updating.latestReleaseFound(msg); got.mode != modeBoard {
		t.Errorf("opened a second install while one runs: mode %v", got.mode)
	}
}

func TestLookupAfterInstallSaysItIsAlreadyInstalled(t *testing.T) {
	fakeUpdates(t)
	m := testModel(120, 40, 1, 0).WithVersion("v1.0.0")
	m = send(m, updateDoneMsg{tag: "v1.1.0"})
	m.mode = modeBoard

	for _, key := range []string{"U", "V"} {
		got := pressAndResolve(t, m, key)
		if got.mode != modeBoard || got.updateTag != "" || !strings.Contains(got.flash, "already installed") {
			t.Errorf("%s: mode %v, tag %q, flash %q", key, got.mode, got.updateTag, got.flash)
		}
	}
}

func TestUpdateKeysRefuseDevBuilds(t *testing.T) {
	f := fakeUpdates(t)
	for _, key := range []string{"U", "V"} {
		m := testModel(120, 40, 1, 0).WithVersion("")
		next, cmd := m.updateBoard(keyPress(key))
		if cmd != nil || f.lookups != 0 || !strings.Contains(next.(Model).flash, "development build") {
			t.Errorf("%s: cmd %v, lookups %d, flash %q", key, cmd != nil, f.lookups, next.(Model).flash)
		}
	}
}

// runSequence runs the commands of a tea.Sequence in order and returns what
// they sent. bubbletea keeps the sequence message unexported, hence reflection.
func runSequence(cmd tea.Cmd) []tea.Msg {
	seq := reflect.ValueOf(cmd())
	var out []tea.Msg
	for i := range seq.Len() {
		out = append(out, seq.Index(i).Interface().(tea.Cmd)())
	}
	return out
}
