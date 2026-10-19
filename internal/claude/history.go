package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ConfigDir is where Claude Code keeps its state, honouring CLAUDE_CONFIG_DIR.
func ConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// Found is a Claude Code conversation that belongs to a project, either
// running right now or recorded in a transcript.
type Found struct {
	SessionID      string
	Title          string
	FirstPrompt    string
	Cwd            string
	Branch         string
	TranscriptPath string
	Updated        time.Time
	Running        bool
	PID            int
	Busy           bool
}

// Live is one entry of Claude Code's registry of running processes.
type Live struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Spare     bool   `json:"spare"`
	Kind      string `json:"kind"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (l Live) Busy() bool { return l.Status == "busy" }

// Background reports whether Claude Code hosts the conversation in a
// background process rather than in a terminal.
func (l Live) Background() bool { return l.Kind == "bg" }

// Running returns the live Claude processes by session id. The registry is
// an internal Claude Code file, so unreadable entries are skipped.
func Running() map[string]Live {
	out := map[string]Live{}
	files, _ := filepath.Glob(filepath.Join(ConfigDir(), "sessions", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var l Live
		if json.Unmarshal(data, &l) != nil || l.SessionID == "" || l.Spare || !processAlive(l.PID) {
			continue
		}
		out[l.SessionID] = l
	}
	return out
}

// ShortID is the first 8 characters of a session id, for display.
func ShortID(id string) string { return id[:min(8, len(id))] }

// ProcessAlive reports whether pid exists, even when owned by another user.
func ProcessAlive(pid int) bool { return processAlive(pid) }

// Terminate asks pid to exit and kills it if it is still alive after 2s.
func Terminate(pid int) error {
	exited := func(tries int) bool {
		for i := 0; i < tries && processAlive(pid); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		return !processAlive(pid)
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		if exited(20) {
			return nil
		}
	}
	return fmt.Errorf("pid %d did not exit", pid)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Within reports whether path is root or inside it.
func Within(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// projectDirName is how Claude Code names a project's transcript directory:
// every character other than a letter or digit becomes '-'.
func projectDirName(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Discover lists conversations started in root or any directory below it
// (such as worktrees), running ones first, then the most recent up to limit.
// skip holds session ids that are already on the board.
func Discover(root string, skip map[string]bool, limit int) []Found {
	live := Running()
	byID := map[string]*Found{}

	prefix := projectDirName(root)
	dirs, _ := os.ReadDir(filepath.Join(ConfigDir(), "projects"))
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	for _, d := range dirs {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), prefix) {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(ConfigDir(), "projects", d.Name(), "*.jsonl"))
		for _, f := range files {
			if info, err := os.Stat(f); err == nil {
				candidates = append(candidates, candidate{f, info.ModTime()})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.After(candidates[j].mod) })

	for _, c := range candidates {
		id := strings.TrimSuffix(filepath.Base(c.path), ".jsonl")
		_, running := live[id]
		if skip[id] || (!running && len(byID) >= limit) {
			continue
		}
		f, ok := readTranscript(c.path)
		if !ok || !Within(root, f.Cwd) {
			continue
		}
		f.SessionID, f.Updated = id, c.mod
		byID[id] = &f
	}

	for id, l := range live {
		if skip[id] || !Within(root, l.Cwd) {
			continue
		}
		f := byID[id]
		if f == nil {
			f = &Found{SessionID: id, Cwd: l.Cwd, Updated: time.UnixMilli(l.UpdatedAt)}
			byID[id] = f
		}
		f.Running, f.PID, f.Busy = true, l.PID, l.Busy()
		if l.Name != "" && l.Name != ShortID(id) {
			f.Title = l.Name
		}
	}

	out := make([]Found, 0, len(byID))
	for _, f := range byID {
		if f.Title == "" {
			f.Title = firstLine(f.FirstPrompt)
		}
		if f.Title == "" {
			f.Title = "Session " + ShortID(f.SessionID)
		}
		out = append(out, *f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		return out[i].Updated.After(out[j].Updated)
	})
	return out
}

// readTranscript pulls the title, first prompt, directory and branch out of
// a transcript. Titles prefer the user's own name over generated ones.
func readTranscript(path string) (Found, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Found{}, false
	}
	defer file.Close()
	f := Found{TranscriptPath: path}
	// A slash command is only used when the session has no typed prompt.
	var custom, agentName, aiTitle, summary, firstCommand string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var e struct {
			Type        string          `json:"type"`
			Cwd         string          `json:"cwd"`
			GitBranch   string          `json:"gitBranch"`
			IsSidechain bool            `json:"isSidechain"`
			IsMeta      bool            `json:"isMeta"`
			CustomTitle string          `json:"customTitle"`
			AgentName   string          `json:"agentName"`
			AITitle     string          `json:"aiTitle"`
			Summary     string          `json:"summary"`
			Message     json.RawMessage `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "custom-title":
			custom = e.CustomTitle
		case "agent-name":
			agentName = e.AgentName
		case "ai-title":
			aiTitle = e.AITitle
		case "summary":
			summary = e.Summary
		case "user":
			if e.IsSidechain {
				continue
			}
			if f.Cwd == "" {
				f.Cwd = e.Cwd
			}
			if e.GitBranch != "" && e.GitBranch != "HEAD" {
				f.Branch = e.GitBranch
			}
			if f.FirstPrompt == "" && !e.IsMeta {
				text, command := promptText(e.Message)
				switch {
				case !command:
					f.FirstPrompt = text
				case firstCommand == "":
					firstCommand = text
				}
			}
		}
	}
	if f.FirstPrompt == "" {
		f.FirstPrompt = firstCommand
	}
	for _, t := range []string{custom, agentName, aiTitle, summary} {
		if t != "" {
			f.Title = t
			break
		}
	}
	if f.Cwd == "" || (f.FirstPrompt == "" && f.Title == "") {
		return Found{}, false
	}
	return f, true
}

// promptText returns what the user typed, ignoring tool results and the
// <system-…> wrappers Claude Code stores as user messages. Slash commands are
// returned as "/name args" with command set.
func promptText(raw json.RawMessage) (text string, command bool) {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return "", false
	}
	if json.Unmarshal(m.Content, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			return "", false
		}
		for _, b := range blocks {
			if b.Type == "text" {
				text = b.Text
				break
			}
		}
	}
	text = strings.TrimSpace(text)
	// A session started with a slash command stores it in tags.
	if name, ok := tagValue(text, "command-name"); ok {
		args, _ := tagValue(text, "command-args")
		return strings.TrimSpace("/" + strings.TrimPrefix(name, "/") + " " + args), true
	}
	if strings.HasPrefix(text, "<") {
		return "", false
	}
	return text, false
}

func tagValue(s, tag string) (string, bool) {
	_, rest, ok := strings.Cut(s, "<"+tag+">")
	if !ok {
		return "", false
	}
	value, _, ok := strings.Cut(rest, "</"+tag+">")
	return strings.TrimSpace(value), ok
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 80 {
		line = line[:77] + "…"
	}
	return line
}
