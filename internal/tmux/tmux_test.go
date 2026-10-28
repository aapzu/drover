package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func waitFor(t *testing.T, name, want string) string {
	t.Helper()
	var out string
	for range 50 {
		out, _, _, _ = Capture(name, 0)
		if strings.Contains(out, want) {
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane never showed %q:\n%s", want, out)
	return ""
}

// useSocket gives the test its own tmux server, so one test's server shutting
// down can't break the next test's startup.
func useSocket(t *testing.T) {
	t.Helper()
	Socket = fmt.Sprintf("kt-tmux-test-%d-%s", os.Getpid(), SafeName(t.Name()))
	t.Cleanup(func() { exec.Command("tmux", "-L", Socket, "kill-server").Run() })
}

func TestTypingIntoDetachedSession(t *testing.T) {
	if Available() != nil {
		t.Skip("tmux not installed")
	}
	useSocket(t)

	name := "typing"
	if err := NewSession(name, t.TempDir(), []string{"cat"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := SendLiteral(name, "hello world"); err != nil {
		t.Fatal(err)
	}
	if err := SendKey(name, "Enter"); err != nil {
		t.Fatal(err)
	}
	if out := waitFor(t, name, "hello world\nhello world"); strings.Count(out, "hello world") != 2 {
		t.Fatalf("cat did not echo the line back:\n%s", out)
	}

	if err := Paste(name, "pasted\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, name, "pasted")

	if err := Resize(name, 50, 12); err != nil {
		t.Fatal(err)
	}
	size, err := run("display-message", "-p", "-t", "="+name+":", "#{window_width}x#{window_height}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(size) != "50x12" {
		t.Fatalf("window is %s, want 50x12", size)
	}
}

func TestResizeWaitsForDetach(t *testing.T) {
	if Available() != nil {
		t.Skip("tmux not installed")
	}
	useSocket(t)

	name := "attached"
	if err := NewSession(name, t.TempDir(), []string{"cat"}, nil); err != nil {
		t.Fatal(err)
	}
	size := func() string {
		out, err := run("display-message", "-p", "-t", "="+name+":", "#{window_width}x#{window_height}")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}

	AttachCommand(name, "")
	defer Detached(name)
	before := size()
	if err := Resize(name, 50, 12); err != nil {
		t.Fatal(err)
	}
	if got := size(); got != before {
		t.Fatalf("attached window was resized from %s to %s", before, got)
	}

	Detached(name)
	if err := Resize(name, 50, 12); err != nil {
		t.Fatal(err)
	}
	if got := size(); got != "50x12" {
		t.Fatalf("window is %s after detach, want 50x12", got)
	}
}

func TestStatusRight(t *testing.T) {
	prefix, suffix := "#[bold]filter: ", "#[nobold] · "+backHint
	cases := []struct{ name, filter, want string }{
		{"empty", "", backHint},
		{"at the limit", strings.Repeat("x", maxStatusFilter), prefix + strings.Repeat("x", maxStatusFilter) + suffix},
		{"over the limit", strings.Repeat("x", maxStatusFilter+1), prefix + strings.Repeat("x", maxStatusFilter-1) + "…" + suffix},
		{"hashes escape after the cut", strings.Repeat("#", 40), prefix + strings.Repeat("##", maxStatusFilter-1) + "…" + suffix},
		{"percents escape after the cut", strings.Repeat("%", 40), prefix + strings.Repeat("%%", maxStatusFilter-1) + "…" + suffix},
		{"style codes stay literal", "#[fg=red]x #", prefix + "##[fg=red]x ##" + suffix},
	}
	for _, c := range cases {
		if got := statusRight(c.filter); got != c.want {
			t.Errorf("%s: statusRight(%q) = %q, want %q", c.name, c.filter, got, c.want)
		}
	}

	for _, filter := range []string{strings.Repeat("ä", 40), strings.Repeat("漢", 40)} {
		got := statusRight(filter)
		shown := strings.TrimSuffix(strings.TrimPrefix(got, prefix), suffix)
		if !utf8.ValidString(got) || ansi.StringWidth(shown) > maxStatusFilter || !strings.HasSuffix(shown, "…") {
			t.Errorf("statusRight(%.4q…) = %q, want valid UTF-8 cut to at most %d cells", filter, got, maxStatusFilter)
		}
	}
}

func TestAttachShowsFilterInStatusBar(t *testing.T) {
	if Available() != nil {
		t.Skip("tmux not installed")
	}
	useSocket(t)

	name := "filtered"
	if err := NewSession(name, t.TempDir(), []string{"cat"}, nil); err != nil {
		t.Fatal(err)
	}
	shown := func() string {
		out, err := run("display-message", "-p", "-t", "="+name+":", "#{T:status-right}")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	AttachCommand(name, "#{session_name} #(echo hi) %d")
	Detached(name)
	if got := shown(); !strings.Contains(got, "filter: #{session_name} #(echo hi) %d") || !strings.Contains(got, "Ctrl-q") {
		t.Fatalf("status-right is %q, want the filter and the Ctrl-q hint", got)
	}

	AttachCommand(name, strings.Repeat("x", 100))
	Detached(name)
	if got := shown(); !strings.Contains(got, "filter: xxx") || strings.Contains(got, strings.Repeat("x", maxStatusFilter+1)) || !strings.Contains(got, "Ctrl-q") {
		t.Fatalf("status-right is %q, want the long filter cut short and the Ctrl-q hint kept", got)
	}

	AttachCommand(name, "")
	Detached(name)
	if got := shown(); strings.Contains(got, "filter") || !strings.Contains(got, "Ctrl-q") {
		t.Fatalf("status-right is %q after clearing the filter, want the default", got)
	}
}

func TestCaptureReturnsHistory(t *testing.T) {
	if Available() != nil {
		t.Skip("tmux not installed")
	}
	useSocket(t)

	name := "history"
	if err := NewSession(name, t.TempDir(), []string{"sh", "-c", "seq 1 100; cat"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := Resize(name, 40, 10); err != nil {
		t.Fatal(err)
	}
	waitFor(t, name, "100")
	out, history, _, err := Capture(name, 30)
	if err != nil {
		t.Fatal(err)
	}
	if history < 30 {
		t.Fatalf("history is %d lines, want at least 30", history)
	}
	if rows := strings.Count(out, "\n"); rows != 40 {
		t.Fatalf("got %d rows, want 30 of history plus 10 on screen:\n%s", rows, out)
	}
}

func TestCaptureReportsMouseTracking(t *testing.T) {
	if Available() != nil {
		t.Skip("tmux not installed")
	}
	useSocket(t)

	cases := []struct {
		name, setup string
		want        bool
	}{
		{"mouse-sgr", `\033[?1000h\033[?1006h`, true},
		{"mouse-no-sgr", `\033[?1000h`, false},
		{"mouse-off", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			script := fmt.Sprintf(`printf '%s'; echo ready; cat`, c.setup)
			if err := NewSession(c.name, t.TempDir(), []string{"sh", "-c", script}, nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { Kill(c.name) })
			waitFor(t, c.name, "ready")
			if _, _, mouse, err := Capture(c.name, 0); err != nil || mouse != c.want {
				t.Fatalf("mouse is %v (err %v), want %v", mouse, err, c.want)
			}
		})
	}
}

func TestPaneOfFindsPaneByAncestor(t *testing.T) {
	if Available() != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("kt-tmux-user-%d", os.Getpid())
	UserServer = []string{"-L", socket, "-f", "/dev/null"}
	t.Cleanup(func() {
		UserServer = nil
		exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	if err := userCommand("new-session", "-d", "--", "sh", "-c", "sleep 60; true").Run(); err != nil {
		t.Fatal(err)
	}
	out, err := userCommand("list-panes", "-a", "-F", "#{pane_pid} #{pane_id}").Output()
	if err != nil {
		t.Fatal(err)
	}
	var shell int
	var want string
	fmt.Sscan(string(out), &shell, &want)
	var child int
	for range 50 {
		out, _ := exec.Command("pgrep", "-P", fmt.Sprint(shell)).Output()
		if fmt.Sscan(string(out), &child); child != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("the pane's sleep never started")
	}
	for _, pid := range []int{shell, child} {
		if got, ok := PaneOf(pid); !ok || got != want {
			t.Errorf("PaneOf(%d) = %q, %v; want %q", pid, got, ok, want)
		}
	}
	if _, ok := PaneOf(os.Getpid()); ok {
		t.Error("a process outside the server matched a pane")
	}
}
