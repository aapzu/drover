// Package tmux runs agent sessions on a private tmux server, so they keep
// running after the TUI exits and never mix with the user's own sessions.
package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Socket names the private tmux server; DROVER_TMUX_SOCKET overrides it.
var Socket = func() string {
	if s := os.Getenv("DROVER_TMUX_SOCKET"); s != "" {
		return s
	}
	return "drover"
}()

var nonName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// SafeName makes s usable inside a session name.
func SafeName(s string) string { return nonName.ReplaceAllString(s, "-") }

// DetachKey returns from an attached session to the board.
const DetachKey = "C-q"

// statusEscaper makes tmux show text literally instead of expanding #{formats},
// running #(commands), applying #[styles] or substituting strftime codes.
var statusEscaper = strings.NewReplacer("#", "##", "%", "%%")

const backHint = "#[fg=#9ca3af]Ctrl-q: back to board "

// maxStatusFilter is the widest filter, in cells, that leaves room for backHint
// within status-right-length.
const maxStatusFilter = 30

// statusRight is the status bar's right side: the filter, when set, then backHint.
func statusRight(filter string) string {
	if filter == "" {
		return backHint
	}
	filter = ansi.Truncate(filter, maxStatusFilter, "…")
	return "#[bold]filter: " + statusEscaper.Replace(filter) + "#[nobold] · " + backHint
}

const conf = `set -g status on
set -g status-style "bg=#1f2937,fg=#e5e7eb"
set -g status-left-length 80
set -g status-right "` + backHint + `"
set -g status-right-length 80
set -g status-left "#[bold] #{session_name} #[nobold]"
set -g mouse on
set -g focus-events on
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
	return rawCommand(context.Background(), escapeSeparators(args)...)
}

// escapeSeparators stops tmux from reading an argument ending in ';' as a
// command separator.
func escapeSeparators(args []string) []string {
	escaped := make([]string, len(args))
	for i, a := range args {
		if strings.HasSuffix(a, ";") {
			a = a[:len(a)-1] + `\;`
		}
		escaped[i] = a
	}
	return escaped
}

// rawCommand passes args as they are, so a lone ";" chains tmux commands.
func rawCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-L", Socket, "-f", ensureConf()}, args...)...)
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
	return output(command(args...), args[0])
}

func output(cmd *exec.Cmd, name string) (string, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %s", name, strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return string(out), nil
}

func Available() error {
	_, err := exec.LookPath("tmux")
	return err
}

// NewSession starts argv detached in dir. The session ends when argv exits.
func NewSession(name, dir string, argv []string, env map[string]string) error {
	_, err := run(newSessionArgs(name, dir, argv, env)...)
	return err
}

// NewAgentSession is NewSession, except that when argv exits non-zero or is
// signalled the session stays until Kill or Stop, so Died can say why. Has and
// List report it as gone.
func NewAgentSession(name, dir string, argv []string, env map[string]string) error {
	// Set in the same tmux invocation, so it applies before tmux can reap an
	// argv that fails at once.
	args := append(escapeSeparators(newSessionArgs(name, dir, argv, env)), ";",
		"set-option", "-t", "="+name+":", "remain-on-exit", "failed")
	_, err := output(rawCommand(context.Background(), args...), "new-session")
	// tmux older than 3.3 rejects the option after creating the session, which
	// then ends as usual without a reason to read.
	if err != nil && !strings.Contains(err.Error(), "duplicate session") && exists(name) {
		return nil
	}
	return err
}

func newSessionArgs(name, dir string, argv []string, env map[string]string) []string {
	args := []string{"new-session", "-d", "-s", name, "-c", dir, "-x", "220", "-y", "60"}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "--")
	return append(args, argv...)
}

// DisableMouse stops tmux from capturing the mouse in the session, so the
// terminal handles selection and scrolling when you attach.
func DisableMouse(name string) error {
	_, err := run("set-option", "-t", "="+name+":", "mouse", "off")
	return err
}

// Has reports whether the session is running a process. A session kept after
// its process died is not running, though it still exists.
func Has(name string) bool {
	out, err := run("list-panes", "-t", "="+name+":", "-F", "#{pane_dead}")
	return err == nil && slices.Contains(strings.Fields(out), "0")
}

func exists(name string) bool {
	_, err := run("has-session", "-t", "="+name)
	return err == nil
}

// List returns the names of all sessions on the private server that are
// running a process.
func List() map[string]bool {
	live, _ := Panes()
	return live
}

// Panes returns the names of the sessions that are running a process, and of
// those NewAgentSession kept after the process died.
func Panes() (live, dead map[string]bool) {
	live, dead = map[string]bool{}, map[string]bool{}
	out, err := run("list-panes", "-a", "-F", "#{pane_dead} #{session_name}")
	if err != nil {
		return live, dead
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if isDead, name, ok := strings.Cut(line, " "); ok {
			if isDead == "0" {
				live[name] = true
			} else {
				dead[name] = true
			}
		}
	}
	for name := range live {
		delete(dead, name)
	}
	return live, dead
}

// Exit is how a NewAgentSession process ended.
type Exit struct {
	// Status is the exit status, and 0 when a signal killed the process.
	Status int
	// Signal is tmux's lowercase name for the signal that killed the process,
	// such as "kill".
	Signal string
	// Output is up to the last three lines the process printed.
	Output []string
}

const exitOutputLines = 3

func (x Exit) String() string {
	how := fmt.Sprintf("exit status %d", x.Status)
	switch {
	case x.Signal == "":
	case unicode.IsDigit(rune(x.Signal[0])):
		how = "killed by signal " + x.Signal
	default:
		how = "killed by SIG" + strings.ToUpper(x.Signal)
	}
	if len(x.Output) == 0 {
		return how
	}
	return how + ", last output: " + strings.Join(x.Output, " | ")
}

// linuxSignals names the signals a process is likely to die of, by their
// number on Linux.
var linuxSignals = map[int]string{1: "hup", 2: "int", 3: "quit", 4: "ill", 6: "abrt", 7: "bus", 8: "fpe", 9: "kill", 11: "segv", 13: "pipe", 14: "alrm", 15: "term"}

// signalName is tmux's name for a signal as the lowercase name without the
// "SIG" prefix. tmux only knows the names on systems with sys_signame, such as
// macOS, and gives the number everywhere else.
func signalName(sig string) string {
	if n, err := strconv.Atoi(sig); err == nil {
		if name, ok := linuxSignals[n]; ok {
			return name
		}
	}
	return sig
}

// Died reports how the session's process ended, if it did and the session was
// started by NewAgentSession. The session stays until you Kill it. It can
// block for half a second while tmux settles.
func Died(name string) (Exit, bool) {
	target := "=" + name + ":"
	out, err := run("display-message", "-p", "-t", target, "#{pane_dead} #{pane_dead_status} #{pane_dead_signal}")
	if err != nil {
		return Exit{}, false
	}
	fields := strings.Split(strings.TrimRight(out, "\n"), " ")
	if len(fields) != 3 || fields[0] != "1" {
		return Exit{}, false
	}
	var x Exit
	x.Status, _ = strconv.Atoi(fields[1])
	x.Signal = signalName(fields[2])
	x.Output = deadOutput(target)
	return x, true
}

// deadOutput returns the last lines a dead pane printed. tmux marks a pane dead
// before it has read the process's last output, then clears the screen into
// the history and shows a "Pane is dead" line, so wait for that line and read
// the history.
func deadOutput(target string) []string {
	var lines []string
	for i := range 10 {
		screen, err := run("capture-pane", "-p", "-S", "-20", "-t", target)
		if err != nil {
			return nil
		}
		lines = lines[:0]
		banner := false
		for _, l := range strings.Split(screen, "\n") {
			l = strings.TrimSpace(l)
			banner = banner || strings.HasPrefix(l, "Pane is dead")
			if l != "" && !strings.HasPrefix(l, "Pane is dead") {
				lines = append(lines, l)
			}
		}
		if banner || i == 9 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	tail := lines[max(0, len(lines)-exitOutputLines):]
	for i, l := range tail {
		tail[i] = ansi.Truncate(l, 120, "…")
	}
	return tail
}

func Kill(name string) error {
	if !exists(name) {
		return nil
	}
	_, err := run("kill-session", "-t", "="+name)
	return err
}

// Stop asks the agent to exit gracefully before killing the session.
func Stop(name string) {
	if !exists(name) {
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

// Cursor is where the pane's app has parked the terminal cursor, as a column
// and an index into the lines of a capture, and whether the app shows it.
type Cursor struct {
	X, Line int
	Visible bool
}

// Capture returns the visible screen plus up to scrollback lines of history,
// with colors, how many lines of history the pane has, whether its app has
// enabled mouse tracking with SGR encoding, and where its cursor is.
// Alternate-screen apps have no history.
func Capture(name string, scrollback int) (out string, history int, mouse bool, cur Cursor, err error) {
	out, _, history, mouse, cur, err = capture(name, scrollback)
	return out, history, mouse, cur, err
}

// CaptureSized is Capture after resizing the window to width x height, unless
// a terminal is attached to it.
func CaptureSized(name string, width, height, scrollback int) (out string, history int, mouse bool, cur Cursor, err error) {
	out, size, history, mouse, cur, err := capture(name, scrollback)
	if err != nil || size == fmt.Sprintf("%dx%d", width, height) {
		return out, history, mouse, cur, err
	}
	attachedMu.Lock()
	defer attachedMu.Unlock()
	if attached[name] {
		return out, history, mouse, cur, nil
	}
	if _, err := run("resize-window", "-t", "="+name+":", "-x", fmt.Sprint(width), "-y", fmt.Sprint(height)); err != nil {
		return out, history, mouse, cur, nil
	}
	out, _, history, mouse, cur, err = capture(name, scrollback)
	return out, history, mouse, cur, err
}

// captureTimeout keeps a hung tmux from stalling the preview, which waits for
// each capture before starting the next.
const captureTimeout = 5 * time.Second

// capture reads the window's size and the pane's state in the same tmux call
// as the pane itself.
func capture(name string, scrollback int) (out, size string, history int, mouse bool, cur Cursor, err error) {
	target := "=" + name + ":"
	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	raw, err := output(rawCommand(ctx,
		"display-message", "-p", "-t", target, "#{window_width}x#{window_height} #{history_size} #{mouse_any_flag}#{mouse_sgr_flag} #{cursor_x} #{cursor_y} #{cursor_flag}", ";",
		"capture-pane", "-p", "-e", "-t", target, "-S", fmt.Sprintf("-%d", scrollback),
	), "display-message; capture-pane")
	if err != nil {
		return "", "", 0, false, Cursor{}, err
	}
	info, out, _ := strings.Cut(raw, "\n")
	fields := strings.Fields(info)
	if len(fields) != 6 {
		return "", "", 0, false, Cursor{}, fmt.Errorf("tmux display-message: unexpected output %q", info)
	}
	history, _ = strconv.Atoi(fields[1])
	x, _ := strconv.Atoi(fields[3])
	y, _ := strconv.Atoi(fields[4])
	cur = Cursor{X: x, Line: min(scrollback, history) + y, Visible: fields[5] == "1"}
	return out, fields[0], history, fields[2] == "11", cur, nil
}

// Screen returns the visible screen as plain text.
func Screen(name string) (string, error) {
	return run("capture-pane", "-p", "-t", "="+name+":")
}

// SendText types a message into the agent and submits it. Multi-line text is
// pasted as one bracketed paste so newlines do not submit early.
func SendText(name, text string) error {
	if err := WaitStable(name, stableTimeout); err != nil {
		return err
	}
	if strings.Contains(text, "\n") || len(text) > 200 {
		if err := Paste(name, text); err != nil {
			return err
		}
	} else if err := SendLiteral(name, text); err != nil {
		return err
	}
	if err := WaitStable(name, stableTimeout); err != nil {
		return err
	}
	return SendKey(name, "Enter")
}

const (
	stableTimeout = 3 * time.Second
	stableFor     = 300 * time.Millisecond
	stablePoll    = 100 * time.Millisecond
)

// WaitStable returns once the screen has stopped changing for a moment, or
// after timeout.
func WaitStable(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last, since := "", time.Now()
	for {
		screen, err := Screen(name)
		if err != nil {
			return err
		}
		now := time.Now()
		if screen != last {
			last, since = screen, now
		} else if now.Sub(since) >= stableFor {
			return nil
		}
		if now.After(deadline) {
			return nil
		}
		time.Sleep(stablePoll)
	}
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

// attachedMu guards attached: sessions with a terminal attached.
var (
	attachedMu sync.Mutex
	attached   = map[string]bool{}
)

// Resize pins the window to the size the board draws it at so the board shows
// the session's whole screen. It does nothing when the size already matches or
// a terminal is attached.
func Resize(name string, width, height int) error {
	attachedMu.Lock()
	defer attachedMu.Unlock()
	if attached[name] {
		return nil
	}
	size, err := run("display-message", "-p", "-t", "="+name+":", "#{window_width}x#{window_height}")
	if err == nil && strings.TrimSpace(size) == fmt.Sprintf("%dx%d", width, height) {
		return nil
	}
	_, err = run("resize-window", "-t", "="+name+":", "-x", fmt.Sprint(width), "-y", fmt.Sprint(height))
	return err
}

// AttachCommand returns a command that attaches the terminal to the session;
// DetachKey hands control back. A non-empty filter is shown in the status bar.
// Resize skips the session from this call until Detached runs, so call
// Detached once the command exits, even if it failed.
func AttachCommand(name, filter string) *exec.Cmd {
	attachedMu.Lock()
	defer attachedMu.Unlock()
	attached[name] = true
	// Undo Resize so the window follows the attached terminal.
	run("set-option", "-w", "-t", "="+name+":", "window-size", "latest")
	run("set-option", "-t", "="+name+":", "status-right", statusRight(filter))
	return command("attach-session", "-t", "="+name)
}

// Detached lets Resize resize the session again once its terminal has exited.
func Detached(name string) {
	attachedMu.Lock()
	defer attachedMu.Unlock()
	delete(attached, name)
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
