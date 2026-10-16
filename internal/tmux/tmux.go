// Package tmux runs agent sessions on a private tmux server, so they keep
// running after the TUI exits and never mix with the user's own sessions.
package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Socket names the private tmux server; KANGENTIC_TUI_TMUX_SOCKET overrides it.
var Socket = func() string {
	if s := os.Getenv("KANGENTIC_TUI_TMUX_SOCKET"); s != "" {
		return s
	}
	return "kangentic-tui"
}()

// DetachKey returns from an attached session to the board.
const DetachKey = "C-q"

const conf = `set -g status on
set -g status-style "bg=#1f2937,fg=#e5e7eb"
set -g status-left-length 80
set -g status-right "#[fg=#9ca3af]Ctrl-q: back to board "
set -g status-left "#[bold] #{session_name} #[nobold]"
set -g mouse on
set -g history-limit 20000
set -g window-size latest
set -g escape-time 10
set -g remain-on-exit off
bind -n C-q detach-client
`

func confPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "kangentic-tui", "tmux.conf")
}

func ensureConf() string {
	path := confPath()
	if current, err := os.ReadFile(path); err != nil || string(current) != conf {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(conf), 0o644)
	}
	return path
}

func command(args ...string) *exec.Cmd {
	escaped := make([]string, len(args))
	for i, a := range args {
		// tmux reads an argument ending in ';' as a command separator.
		if strings.HasSuffix(a, ";") {
			a = a[:len(a)-1] + `\;`
		}
		escaped[i] = a
	}
	cmd := exec.Command("tmux", append([]string{"-L", Socket, "-f", ensureConf()}, escaped...)...)
	cmd.Env = withoutTmuxEnv()
	return cmd
}

// withoutTmuxEnv lets the private server be driven, and attached to, from
// inside the user's own tmux.
func withoutTmuxEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			env = append(env, kv)
		}
	}
	return env
}

func run(args ...string) (string, error) {
	cmd := command(args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %s", args[0], strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return string(out), nil
}

func Available() error {
	_, err := exec.LookPath("tmux")
	return err
}

// NewSession starts argv detached in dir. The session ends when argv exits.
func NewSession(name, dir string, argv []string, env map[string]string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", dir, "-x", "220", "-y", "60"}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "--")
	args = append(args, argv...)
	_, err := run(args...)
	return err
}

func Has(name string) bool {
	_, err := run("has-session", "-t", "="+name)
	return err == nil
}

// List returns the names of all live sessions on the private server.
func List() map[string]bool {
	out, err := run("list-sessions", "-F", "#{session_name}")
	live := map[string]bool{}
	if err != nil {
		return live
	}
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		if name != "" {
			live[name] = true
		}
	}
	return live
}

func Kill(name string) error {
	if !Has(name) {
		return nil
	}
	_, err := run("kill-session", "-t", "="+name)
	return err
}

// Stop asks the agent to exit gracefully before killing the session.
func Stop(name string) {
	if !Has(name) {
		return
	}
	run("send-keys", "-t", "="+name, "C-c")
	time.Sleep(150 * time.Millisecond)
	run("send-keys", "-t", "="+name, "C-c")
	for i := 0; i < 15 && Has(name); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	Kill(name)
}

// Capture returns the visible pane plus some scrollback, with colors.
func Capture(name string, scrollback int) (string, error) {
	return run("capture-pane", "-p", "-e", "-J", "-t", "="+name+":", "-S", fmt.Sprintf("-%d", scrollback))
}

func Interrupt(name string) error {
	_, err := run("send-keys", "-t", "="+name+":", "Escape")
	return err
}

// SendText types a message into the agent and submits it. Multi-line text is
// pasted as one bracketed paste so newlines do not submit early.
func SendText(name, text string) error {
	target := "=" + name + ":"
	if strings.Contains(text, "\n") || len(text) > 200 {
		buf := "kt-" + name
		load := command("load-buffer", "-b", buf, "-")
		load.Stdin = strings.NewReader(text)
		if out, err := load.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux load-buffer: %s", strings.TrimSpace(string(out)))
		}
		if _, err := run("paste-buffer", "-p", "-d", "-b", buf, "-t", target); err != nil {
			return err
		}
	} else if _, err := run("send-keys", "-t", target, "-l", "--", text); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	_, err := run("send-keys", "-t", target, "Enter")
	return err
}

// AttachCommand returns a command that attaches the terminal to the session;
// DetachKey hands control back.
func AttachCommand(name string) *exec.Cmd {
	return command("attach-session", "-t", "="+name)
}
