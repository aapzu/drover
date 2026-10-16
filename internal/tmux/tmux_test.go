package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, name, want string) string {
	t.Helper()
	var out string
	for range 50 {
		out, _ = Capture(name, 0)
		if strings.Contains(out, want) {
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane never showed %q:\n%s", want, out)
	return ""
}

func TestTypingIntoDetachedSession(t *testing.T) {
	if Available() != nil {
		t.Skip("tmux not installed")
	}
	Socket = fmt.Sprintf("kt-tmux-test-%d", os.Getpid())
	t.Cleanup(func() { exec.Command("tmux", "-L", Socket, "kill-server").Run() })

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
