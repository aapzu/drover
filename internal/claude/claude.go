// Package claude launches Claude Code and turns its hook events into session
// activity, the same signals Kangentic relies on.
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
	command := fmt.Sprintf("%s hook %s", shellQuote(exe), shellQuote(eventsPath))
	hook := []map[string]any{{"type": "command", "command": command, "timeout": 10}}
	hooks := map[string]any{}
	for _, name := range hookEvents {
		entry := map[string]any{"hooks": hook}
		if name == "PreToolUse" || name == "PostToolUse" || name == "PermissionRequest" {
			entry["matcher"] = "*"
		}
		hooks[name] = []any{entry}
	}
	data, err := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0o644)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// RecordHook is the body of `kangentic-tui hook <file>`: it appends the hook
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
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
	ToolInput            struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

var (
	prCommand = regexp.MustCompile(`\bgh\s+(pr\s+create|stack)\b`)
	prURL     = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)
)

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

// ReadEvents returns complete event lines after offset and the new offset.
// A trailing partial line is left for the next read.
func ReadEvents(path string, offset int64) ([]Event, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, offset, nil
		}
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	var events []Event
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}
		offset += int64(len(line))
		var ev Event
		if json.Unmarshal(line, &ev) == nil && ev.Name != "" {
			events = append(events, ev)
		}
	}
	return events, offset, nil
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
