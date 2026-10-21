// Package tmux runs agent sessions on a private tmux server, so they keep
// running after the TUI exits and never mix with the user's own sessions.
package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Socket names the private tmux server; DROVER_TMUX_SOCKET overrides it.
var Socket = func() string {
	if s := os.Getenv("DROVER_TMUX_SOCKET"); s != "" {
		return s
	}
	return "drover"
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
set -s extended-keys on
set -as terminal-features '*:extkeys'
set -g remain-on-exit off
bind -n C-q detach-client
`

func confPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "drover", "tmux.conf")
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

// Capture returns the visible screen plus up to scrollback lines of history,
// with colors, how many lines of history the pane has, and whether its app has
// enabled mouse tracking with SGR encoding. Alternate-screen apps have no history.
func Capture(name string, scrollback int) (out string, history int, mouse bool, err error) {
	target := "=" + name + ":"
	info, err := run("display-message", "-p", "-t", target, "#{history_size} #{mouse_any_flag}#{mouse_sgr_flag}")
	if err != nil {
		return "", 0, false, err
	}
	size, flags, _ := strings.Cut(strings.TrimSpace(info), " ")
	history, _ = strconv.Atoi(size)
	out, err = run("capture-pane", "-p", "-e", "-t", target, "-S", fmt.Sprintf("-%d", scrollback))
	return out, history, flags == "11", err
}

// Screen returns the visible screen as plain text.
func Screen(name string) (string, error) {
	return run("capture-pane", "-p", "-t", "="+name+":")
}

// SendText types a message into the agent and submits it. Multi-line text is
// pasted as one bracketed paste so newlines do not submit early.
func SendText(name, text string) error {
	if strings.Contains(text, "\n") || len(text) > 200 {
		if err := Paste(name, text); err != nil {
			return err
		}
	} else if err := SendLiteral(name, text); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond)
	return SendKey(name, "Enter")
}

// SendLiteral types text without interpreting key names.
func SendLiteral(name, text string) error {
	_, err := run("send-keys", "-t", "="+name+":", "-l", "--", text)
	return err
}

// SendKey sends one tmux key name such as Enter, C-c or M-Left.
func SendKey(name, key string) error {
	_, err := run("send-keys", "-t", "="+name+":", key)
	return err
}

// Paste inserts text as one bracketed paste.
func Paste(name, text string) error {
	buf := "kt-" + name
	load := command("load-buffer", "-b", buf, "-")
	load.Stdin = strings.NewReader(text)
	if out, err := load.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %s", strings.TrimSpace(string(out)))
	}
	_, err := run("paste-buffer", "-p", "-d", "-b", buf, "-t", "="+name+":")
	return err
}

// Resize pins the window to the board's preview size so the preview shows the
// agent's whole screen. It does nothing when the size already matches.
func Resize(name string, width, height int) error {
	size, err := run("display-message", "-p", "-t", "="+name+":", "#{window_width}x#{window_height}")
	if err == nil && strings.TrimSpace(size) == fmt.Sprintf("%dx%d", width, height) {
		return nil
	}
	_, err = run("resize-window", "-t", "="+name+":", "-x", fmt.Sprint(width), "-y", fmt.Sprint(height))
	return err
}

// AttachCommand returns a command that attaches the terminal to the session;
// DetachKey hands control back.
func AttachCommand(name string) *exec.Cmd {
	// Undo Resize so the window follows the attached terminal.
	run("set-option", "-w", "-t", "="+name+":", "window-size", "latest")
	return command("attach-session", "-t", "="+name)
}

// UserServer holds the tmux flags that select your own tmux server, where
// agents started outside the board may run; empty means tmux's default.
var UserServer []string

func userCommand(args ...string) *exec.Cmd {
	return exec.Command("tmux", append(append([]string{}, UserServer...), args...)...)
}

// PaneOf returns the pane on your own tmux server whose process is pid or one
// of its ancestors.
func PaneOf(pid int) (string, bool) {
	out, err := userCommand("list-panes", "-a", "-F", "#{pane_pid} #{pane_id}").Output()
	if err != nil {
		return "", false
	}
	panes := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var panePID int
		var id string
		if _, err := fmt.Sscan(line, &panePID, &id); err == nil {
			panes[panePID] = id
		}
	}
	parents := map[int]int{}
	out, _ = exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var child, parent int
		if _, err := fmt.Sscan(line, &child, &parent); err == nil {
			parents[child] = parent
		}
	}
	for range 32 {
		if id, ok := panes[pid]; ok {
			return id, true
		}
		if pid <= 1 {
			break
		}
		pid = parents[pid]
	}
	return "", false
}

// AttachPaneCommand returns a command that focuses pane on your own tmux
// server and shows it: it switches the current client when the board runs
// inside tmux, and attaches otherwise.
func AttachPaneCommand(pane string) *exec.Cmd {
	show := "attach-session"
	if os.Getenv("TMUX") != "" {
		show = "switch-client"
	}
	return userCommand("select-window", "-t", pane, ";", "select-pane", "-t", pane, ";", show, "-t", pane)
}
