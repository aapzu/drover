// Package claude launches Claude Code and turns its hook events into session
// activity.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Launch struct {
	SettingsPath   string
	SessionID      string
	Resume         bool
	PermissionMode string
	Model          string
	Effort         string
	Prompt         string
}

func (l Launch) Argv() []string {
	argv := []string{"claude", "--settings", l.SettingsPath}
	if l.Resume {
		argv = append(argv, "--resume", l.SessionID)
	} else {
		argv = append(argv, "--session-id", l.SessionID)
	}
	if l.PermissionMode != "" {
		argv = append(argv, "--permission-mode", l.PermissionMode)
	}
	if l.Model != "" {
		argv = append(argv, "--model", l.Model)
	}
	if l.Effort != "" {
		argv = append(argv, "--effort", l.Effort)
	}
	if l.Prompt != "" && !l.Resume {
		argv = append(argv, "--", l.Prompt)
	}
	return argv
}

var hookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"Notification", "PermissionRequest", "Stop",
}

// WriteSettings writes a settings file whose hooks append every event to
// eventsPath through `<exe> hook`. Claude merges it with the user's settings.
func WriteSettings(settingsPath, exe, eventsPath string) error {
	command := fmt.Sprintf("%s hook %s", ShellQuote(exe), ShellQuote(eventsPath))
	hook := []map[string]any{{"type": "command", "command": command, "timeout": 10}}
	hooks := map[string]any{}
	for _, name := range hookEvents {
		entry := map[string]any{"hooks": hook}
		if name == "PreToolUse" || name == "PostToolUse" || name == "PermissionRequest" {
			entry["matcher"] = "*"
		}
		hooks[name] = []any{entry}
	}
	settings := map[string]any{
		"hooks":       hooks,
		"permissions": map[string]any{"allow": []string{"EnterWorktree"}},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0o644)
}

// ShellQuote single-quotes s for a POSIX shell.
func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// RecordHook is the body of `drover hook <file>`: it appends the hook
// payload from stdin as one JSON line. It never blocks Claude on failure.
func RecordHook(eventsPath string, stdin io.Reader) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return
	}
	var line bytes.Buffer
	if json.Compact(&line, raw) != nil {
		return
	}
	line.WriteByte('\n')
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(line.Bytes())
}

type Event struct {
	Name                 string `json:"hook_event_name"`
	SessionID            string `json:"session_id"`
	Source               string `json:"source"`
	ToolName             string `json:"tool_name"`
	NotificationType     string `json:"notification_type"`
	Message              string `json:"message"`
	Prompt               string `json:"prompt"`
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
	PermissionMode       string `json:"permission_mode"`
	Cwd                  string `json:"cwd"`
	ToolInput            struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

var (
	prCommand = regexp.MustCompile(`\bgh\s+(pr\s+create|stack)\b`)
	prURL     = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)
	prAsk     = regexp.MustCompile(`(?i)\b(open|create|raise|submit|put up|make)\s+(((a|an|new|draft)\s+)+(pr|pull request)s?|prs|pull requests)\b`)
	prNegated = regexp.MustCompile(`(?i)\b(don['’]?t|do not|not|never|no need to|without|before|until)\b[^.?!\n]{0,30}$`)
)

// AsksForPR reports whether a prompt asks the agent to open a pull request,
// like "open a PR" or "create pull requests". Slash commands never count.
func AsksForPR(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	if strings.HasPrefix(prompt, "/") {
		return false
	}
	for _, m := range prAsk.FindAllStringIndex(prompt, -1) {
		if !prNegated.MatchString(prompt[max(0, m[0]-64):m[0]]) {
			return true
		}
	}
	return false
}

// CreatedPRs returns the pull request URLs a Bash `gh pr create` or
// `gh stack` call printed. Other commands are ignored so PRs the agent only
// looked at never count as its own.
func (e Event) CreatedPRs() []string {
	if e.Name != "PostToolUse" || e.ToolName != "Bash" || !prCommand.MatchString(e.ToolInput.Command) {
		return nil
	}
	var urls []string
	for _, u := range prURL.FindAllString(string(e.ToolResponse), -1) {
		if !slices.Contains(urls, u) {
			urls = append(urls, u)
		}
	}
	return urls
}

var modeIndicators = []struct{ text, mode string }{
	{"plan mode", "plan"},
	{"accept edits", "acceptEdits"},
	{"auto mode", "auto"},
	{"bypass permissions", "bypassPermissions"},
	{"don't ask", "dontAsk"},
	{"manual mode", "default"},
}

// ScreenMode returns the permission mode shown at the bottom of a plain
// Claude Code screen, or "" when none is shown.
func ScreenMode(screen string) string {
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	for i := len(lines) - 1; i >= max(0, len(lines)-6); i-- {
		for _, m := range modeIndicators {
			if strings.Contains(lines[i], m.text) {
				return m.mode
			}
		}
	}
	return ""
}

// ReadEvents returns complete event lines after offset and the new offset.
// A trailing partial line is left for the next read.
func ReadEvents(path string, offset int64) ([]Event, int64, error) {
	var events []Event
	offset, err := readLines(path, offset, func(line []byte) {
		var ev Event
		if json.Unmarshal(line, &ev) == nil && ev.Name != "" {
			events = append(events, ev)
		}
	})
	return events, offset, err
}

// TranscriptPRs returns the pull requests Claude Code recorded as created in
// the transcript after offset, and the new offset.
func TranscriptPRs(path string, offset int64) ([]string, int64, error) {
	var urls []string
	offset, err := readLines(path, offset, func(line []byte) {
		if !bytes.Contains(line, []byte(`"gitOperation"`)) {
			return
		}
		var entry struct {
			ToolUseResult struct {
				GitOperation struct {
					PR struct {
						URL    string `json:"url"`
						Action string `json:"action"`
					} `json:"pr"`
				} `json:"gitOperation"`
			} `json:"toolUseResult"`
		}
		if json.Unmarshal(line, &entry) != nil {
			return
		}
		pr := entry.ToolUseResult.GitOperation.PR
		if pr.Action == "created" && pr.URL != "" && !slices.Contains(urls, pr.URL) {
			urls = append(urls, pr.URL)
		}
	})
	return urls, offset, err
}

// readLines calls fn with each complete line after offset and returns the
// new offset. A trailing partial line is left for the next read.
func readLines(path string, offset int64, fn func([]byte)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return offset, nil
		}
		return offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return offset, nil
		}
		if err != nil {
			return offset, err
		}
		offset += int64(len(line))
		fn(line)
	}
}

// Reply returns the agent's final message for a Stop event, falling back to
// the transcript on Claude versions that do not include it in the payload.
func (e Event) Reply() string {
	if e.LastAssistantMessage != "" {
		return e.LastAssistantMessage
	}
	return LastAssistantText(e.TranscriptPath)
}

func LastAssistantText(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		return ""
	}
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &entry) != nil || entry.Type != "assistant" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(entry.Message.Content, &blocks) != nil {
			continue
		}
		var texts []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return ""
}

// EvalCondition asks a small read-only Claude call whether condition holds for
// the agent's reply.
func EvalCondition(ctx context.Context, dir, condition, reply string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	prompt := fmt.Sprintf("You judge automation conditions. Answer with exactly YES or NO.\n\nCondition: %s\n\nAgent's final message:\n<message>\n%s\n</message>", condition, reply)
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", "haiku", "--permission-mode", "plan", prompt)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("condition check failed: %w", err)
	}
	answer := strings.ToUpper(strings.TrimSpace(string(out)))
	return strings.HasPrefix(answer, "YES"), nil
}

// Interrupted reports whether the transcript's last conversation entry is the
// record of you interrupting a turn after the given time, which Claude Code
// leaves without firing a Stop hook. The time keeps an earlier turn's
// interrupt from counting while the next prompt is not recorded yet.
func Interrupted(transcriptPath string, after time.Time) bool {
	const tail = 32 << 10
	marker := []byte("[Request interrupted by user")
	f, err := os.Open(transcriptPath)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	start := max(0, info.Size()-tail)
	data := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(data, start); err != nil && err != io.EOF {
		return false
	}
	// A line still being written is unsettled; the next poll sees it whole.
	if !bytes.Contains(data, marker) || !bytes.HasSuffix(data, []byte("\n")) {
		return false
	}
	data = bytes.TrimSuffix(data, []byte("\n"))
	for len(data) > 0 {
		var line []byte
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			line, data = data[i+1:], data[:i]
		} else {
			line, data = data, nil
		}
		var entry struct {
			Type        string          `json:"type"`
			IsSidechain bool            `json:"isSidechain"`
			Timestamp   string          `json:"timestamp"`
			Message     json.RawMessage `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.IsSidechain || (entry.Type != "user" && entry.Type != "assistant") {
			continue
		}
		if entry.Type == "assistant" {
			return false
		}
		if text, _ := promptText(entry.Message); !bytes.HasPrefix([]byte(text), marker) {
			return false
		}
		at, err := time.Parse(time.RFC3339, entry.Timestamp)
		return err != nil || at.After(after)
	}
	return false
}
