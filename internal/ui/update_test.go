package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func fakeUpdates(t *testing.T) (checks *[]string, installs *[]string) {
	t.Helper()
	checks, installs = new([]string), new([]string)
	origCheck, origInstall := checkUpdate, installUpdate
	t.Cleanup(func() { checkUpdate, installUpdate = origCheck, origInstall })
	checkUpdate = func(_ context.Context, current string) (string, bool) {
		*checks = append(*checks, current)
		return "v1.1.0", true
	}
	installUpdate = func(_ context.Context, tag string) error {
		*installs = append(*installs, tag)
		return nil
	}
	return checks, installs
}

func send(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func TestInitChecksForUpdatesOnlyOnReleases(t *testing.T) {
	checks, _ := fakeUpdates(t)
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
			*checks = nil
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
	_, installs := fakeUpdates(t)
	m := testModel(120, 40, 1, 0).WithVersion("v1.0.0")

	if got := m.renderFooter(); strings.Contains(got, "available") {
		t.Fatalf("footer shows an update before one was found:\n%s", got)
	}
	m = send(m, updateAvailableMsg("v1.1.0"))
	if got := m.renderFooter(); !strings.Contains(got, "v1.1.0 available: U") {
		t.Fatalf("footer doesn't announce the update:\n%s", got)
	}

	next, _ := m.updateBoard(keyPress("U"))
	m = next.(Model)
	if m.mode != modeConfirm || !strings.Contains(m.confirmText, "v1.0.0 to v1.1.0") {
		t.Fatalf("U didn't ask to confirm: mode %v, %q", m.mode, m.confirmText)
	}
	next, cmd := m.updateConfirm(keyPress("y"))
	m = next.(Model)
	if len(*installs) != 0 {
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
	again, _ := m.updateBoard(keyPress("U"))
	if got := again.(Model); got.mode != modeBoard || !strings.Contains(got.flash, "Already updating") {
		t.Errorf("U during an install: mode %v, flash %q", got.mode, got.flash)
	}
	done := msgs[1].(updateDoneMsg)
	if len(*installs) != 1 || (*installs)[0] != "v1.1.0" {
		t.Fatalf("installs = %v", *installs)
	}
	next, quit := m.Update(done)
	m = next.(Model)
	if m.updateTag != "" || !m.RestartRequested() {
		t.Errorf("after the install: tag %q, restart %v", m.updateTag, m.RestartRequested())
	}
	if quit == nil {
		t.Fatal("the board didn't quit after the install")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Errorf("command returned %T, want tea.QuitMsg", quit())
	}
}

func TestUpdateConfirmCanBeDeclinedAndFailuresAreShown(t *testing.T) {
	_, installs := fakeUpdates(t)
	m := testModel(120, 40, 1, 0).WithVersion("v1.0.0")
	m = send(m, updateAvailableMsg("v1.1.0"))

	next, _ := m.updateBoard(keyPress("U"))
	next, cmd := next.(Model).updateConfirm(keyPress("n"))
	if cmd != nil || len(*installs) != 0 || next.(Model).updateTag != "v1.1.0" {
		t.Error("declining changed something")
	}

	next, quit := m.Update(updateDoneMsg{tag: "v1.1.0", err: errors.New("disk full")})
	m = next.(Model)
	if quit != nil || m.RestartRequested() {
		t.Error("a failed install quit the board")
	}
	if m.updateTag != "v1.1.0" || !strings.Contains(m.flash, "Update failed: disk full") {
		t.Errorf("after a failed install: tag %q, flash %q", m.updateTag, m.flash)
	}
}

func TestUpdateKeyWithoutAnUpdateSaysSo(t *testing.T) {
	m := testModel(120, 40, 1, 0).WithVersion("v1.0.0")
	next, _ := m.updateBoard(keyPress("U"))
	if got := next.(Model); got.mode != modeBoard || !strings.Contains(got.flash, "up to date") {
		t.Errorf("mode %v, flash %q", got.mode, got.flash)
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
