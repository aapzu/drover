// Package store persists tasks and sessions for one project in
// .kangentic/tui/state.json. The desktop app keeps its own SQLite database;
// the two do not share task data.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type SessionStatus string

const (
	StatusRunning   SessionStatus = "running"
	StatusQueued    SessionStatus = "queued"
	StatusSuspended SessionStatus = "suspended"
	StatusExited    SessionStatus = "exited"
	// StatusExternal is an imported agent still running in another terminal.
	// The board mirrors its status but never starts a second copy of it.
	StatusExternal SessionStatus = "external"
)

type Activity string

const (
	ActivityStarting   Activity = "starting"
	ActivityThinking   Activity = "thinking"
	ActivityIdle       Activity = "idle"
	ActivityPermission Activity = "permission"
)

// MainSession is the session key for the task's main conversation; isolated
// columns use their column id as the key.
const MainSession = "main"

type State struct {
	NextNumber int     `json:"nextNumber"`
	Tasks      []*Task `json:"tasks"`
}

type Task struct {
	ID       string `json:"id"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Prompt   string `json:"prompt"`
	ColumnID string `json:"columnId"`
	Issue    *Issue `json:"issue,omitempty"`
	PR       *PR    `json:"pr,omitempty"`
	// GroupID is set on cards split off when one session opens several PRs.
	// It names the owner task, which holds the sessions and the worktree
	// that every card in the group shares.
	GroupID string `json:"groupId,omitempty"`
	// ActiveCard is the group card whose move last drove the shared session;
	// its column decides the session's settings and turn-end automations.
	ActiveCard     string              `json:"activeCard,omitempty"`
	BaseBranch     string              `json:"baseBranch,omitempty"`
	Branch         string              `json:"branch,omitempty"`
	WorktreePath   string              `json:"worktreePath,omitempty"`
	Visit          int                 `json:"visit"`
	Sessions       map[string]*Session `json:"sessions,omitempty"`
	AutomationRuns map[string]int      `json:"automationRuns,omitempty"`
	Log            []LogEntry          `json:"log,omitempty"`
	CreatedAt      time.Time           `json:"createdAt"`
	UpdatedAt      time.Time           `json:"updatedAt"`
}

type Session struct {
	Key string `json:"key"`
	// Dir holds the session's settings and events. It is fixed at spawn so
	// the session keeps it when another task takes over the group.
	Dir            string        `json:"dir,omitempty"`
	AgentSessionID string        `json:"agentSessionId"`
	Tmux           string        `json:"tmux"`
	Status         SessionStatus `json:"status"`
	Activity       Activity      `json:"activity"`
	Model          string        `json:"model,omitempty"`
	Effort         string        `json:"effort,omitempty"`
	PermissionMode string        `json:"permissionMode,omitempty"`
	EventsOffset   int64         `json:"eventsOffset"`
	// Started is set once the agent has recorded a conversation, so a later
	// spawn can resume it instead of starting over.
	Started     bool `json:"started"`
	TurnInVisit bool `json:"turnInVisit"`
	// RespawnPending restarts the agent with its column's settings once the
	// current turn ends; set when the card moves mid-turn.
	RespawnPending bool      `json:"respawnPending,omitempty"`
	Pending        []Pending `json:"pending,omitempty"`
	LastReply      string    `json:"lastReply,omitempty"`
	// Imported sessions came from outside the board; PID and TranscriptPath
	// track them while they run in another terminal.
	Imported       bool      `json:"imported,omitempty"`
	PID            int       `json:"pid,omitempty"`
	TranscriptPath string    `json:"transcriptPath,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Issue links a task to a tracker issue, currently Linear.
type Issue struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Key      string `json:"key"`
	URL      string `json:"url"`
	TeamID   string `json:"teamId,omitempty"`
	State    string `json:"state,omitempty"`
}

// PR is a GitHub pull request the task's agent opened.
type PR struct {
	URL    string `json:"url"`
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	Branch string `json:"branch,omitempty"`
	State  string `json:"state,omitempty"`
}

// Pending is a message waiting for the session to become idle.
type Pending struct {
	Text string `json:"text"`
}

type LogEntry struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

const maxLog = 200

func (t *Task) AddLog(text string) {
	t.Log = append(t.Log, LogEntry{At: time.Now(), Text: text})
	if len(t.Log) > maxLog {
		t.Log = t.Log[len(t.Log)-maxLog:]
	}
}

func (t *Task) Clone() *Task {
	c := *t
	c.Sessions = make(map[string]*Session, len(t.Sessions))
	for k, s := range t.Sessions {
		sc := *s
		sc.Pending = append([]Pending(nil), s.Pending...)
		c.Sessions[k] = &sc
	}
	c.AutomationRuns = make(map[string]int, len(t.AutomationRuns))
	for k, v := range t.AutomationRuns {
		c.AutomationRuns[k] = v
	}
	c.Log = append([]LogEntry(nil), t.Log...)
	if t.Issue != nil {
		issue := *t.Issue
		c.Issue = &issue
	}
	if t.PR != nil {
		pr := *t.PR
		c.PR = &pr
	}
	return &c
}

func find(tasks []*Task, id string) *Task {
	for _, t := range tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// Owner returns the task that holds t's sessions and worktree.
func Owner(tasks []*Task, t *Task) *Task {
	if t.GroupID == "" {
		return t
	}
	if o := find(tasks, t.GroupID); o != nil {
		return o
	}
	return t
}

// Members returns the owner followed by the cards split off from it.
func Members(tasks []*Task, owner *Task) []*Task {
	out := []*Task{owner}
	for _, t := range tasks {
		if t.GroupID == owner.ID {
			out = append(out, t)
		}
	}
	return out
}

// Driver returns the group card whose column the owner's session follows.
func Driver(tasks []*Task, owner *Task) *Task {
	if owner.ActiveCard != "" {
		if d := find(tasks, owner.ActiveCard); d != nil && (d == owner || d.GroupID == owner.ID) {
			return d
		}
	}
	return owner
}

func Dir(root string) string { return filepath.Join(root, ".kangentic", "tui") }

func path(root string) string { return filepath.Join(Dir(root), "state.json") }

func Load(root string) (*State, error) {
	data, err := os.ReadFile(path(root))
	if errors.Is(err, os.ErrNotExist) {
		return &State{NextNumber: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	s := &State{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.NextNumber < 1 {
		s.NextNumber = 1
	}
	return s, nil
}

// Save writes atomically so a crash never leaves a truncated state file.
func (s *State) Save(root string) error {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path(root) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path(root))
}
