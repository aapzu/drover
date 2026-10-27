// Package store persists tasks and sessions for one project in
// $XDG_STATE_HOME/drover/<project>-<hash>/state.json, outside the repo so it
// can never be committed.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/aapzu/drover/internal/claude"
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
	GroupID string   `json:"groupId,omitempty"`
	SeenPRs []string `json:"seenPRs,omitempty"`
	// ActiveCard is the group card whose move last drove the shared session;
	// its column decides the session's settings and turn-end automations.
	ActiveCard    string              `json:"activeCard,omitempty"`
	PRMovePending bool                `json:"prMovePending,omitempty"`
	BaseBranch    string              `json:"baseBranch,omitempty"`
	Branch        string              `json:"branch,omitempty"`
	WorktreePath  string              `json:"worktreePath,omitempty"`
	Visit         int                 `json:"visit"`
	Sessions      map[string]*Session `json:"sessions,omitempty"`
	Log           []LogEntry          `json:"log,omitempty"`
	CreatedAt     time.Time           `json:"createdAt"`
	UpdatedAt     time.Time           `json:"updatedAt"`
	// Archived cards stay in Done but are hidden from the board.
	Archived bool `json:"archived,omitempty"`
	Hidden   bool `json:"hidden,omitempty"`
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
	// ModeInherited is set when drover left the mode to Claude Code, which
	// PermissionMode cannot show once hooks report it. Older sessions count as forced.
	ModeInherited bool `json:"modeInherited,omitempty"`
	// Started is set once the agent has recorded a conversation, so a later
	// spawn can resume it instead of starting over.
	Started     bool `json:"started"`
	TurnInVisit bool `json:"turnInVisit"`
	// RespawnPending switches the agent to its column's settings once the
	// current turn ends; set when the card moves mid-turn.
	RespawnPending     bool      `json:"respawnPending,omitempty"`
	Background         []string  `json:"background,omitempty"`
	BackgroundScanPath string    `json:"backgroundScanPath,omitempty"`
	BackgroundScan     int64     `json:"backgroundScan,omitempty"`
	Pending            []Pending `json:"pending,omitempty"`
	LastReply          string    `json:"lastReply,omitempty"`
	Summary            string    `json:"summary,omitempty"`
	// Imported sessions came from outside the board; PID and TranscriptPath
	// track them while they run in another terminal.
	Imported  bool `json:"imported,omitempty"`
	HandedOff bool `json:"handedOff,omitempty"`
	// Forked is set by a fork SessionStart; a blank startup right after it
	// means the conversation was moved to a background session.
	Forked         bool         `json:"forked,omitempty"`
	PID            int          `json:"pid,omitempty"`
	TranscriptPath string       `json:"transcriptPath,omitempty"`
	PRScanPath     string       `json:"prScanPath,omitempty"`
	PRScan         int64        `json:"prScan,omitempty"`
	Usage          claude.Usage `json:"usage,omitzero"`
	UsageScanPath  string       `json:"usageScanPath,omitempty"`
	UsageScan      int64        `json:"usageScan,omitempty"`
	UpdatedAt      time.Time    `json:"updatedAt"`
}

// HasBackground reports whether the session's background tasks are still running.
func (s *Session) HasBackground() bool { return len(s.Background) > 0 }

// SetMode records the mode drover launched or switched the agent to; "" leaves
// the choice to Claude Code.
func (s *Session) SetMode(mode string) {
	s.PermissionMode, s.ModeInherited = mode, mode == ""
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

// PR is a GitHub pull request the task's agent opened, or one you asked it to
// review (Review).
type PR struct {
	URL              string    `json:"url"`
	Number           int       `json:"number"`
	Title            string    `json:"title,omitempty"`
	Branch           string    `json:"branch,omitempty"`
	State            string    `json:"state,omitempty"`
	ReviewDecision   string    `json:"reviewDecision,omitempty"`
	MergeCommit      string    `json:"mergeCommit,omitempty"`
	ChangesSeenAt    time.Time `json:"changesSeenAt,omitzero"`
	FixFrom          string    `json:"fixFrom,omitempty"`
	Released         bool      `json:"released,omitempty"`
	MergeMovePending bool      `json:"mergeMovePending,omitempty"`
	// Review is set when you ask the agent to review the PR, and dropped
	// again if gh shows you wrote it. Until gh has answered, State is empty.
	Review     bool      `json:"review,omitempty"`
	Author     string    `json:"author,omitempty"`
	HeadSHA    string    `json:"headSha,omitempty"`
	MyReview   string    `json:"myReview,omitempty"`
	MyReviewAt time.Time `json:"myReviewAt,omitzero"`
	// ReviewedSHA is the commit your latest review was on.
	ReviewedSHA string `json:"reviewedSha,omitempty"`
	Error       string `json:"error,omitempty"`
}

// IsReview reports whether p is a PR you review, and is safe on nil.
func (p *PR) IsReview() bool { return p != nil && p.Review }

// Pending is a message waiting for the session to become idle.
type Pending struct {
	Text string `json:"text"`
}

type LogEntry struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

const maxLog = 200

// AgentSession is the session a card shows, taken from its owner: the main one,
// else any that is running.
func (t *Task) AgentSession() *Session {
	if s := t.Sessions[MainSession]; s != nil {
		return s
	}
	for _, s := range t.Sessions {
		if s.Status == StatusRunning {
			return s
		}
	}
	return nil
}

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
	c.Log = append([]LogEntry(nil), t.Log...)
	c.SeenPRs = slices.Clone(t.SeenPRs)
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

// ByNumber returns the task numbered n, or nil.
func (s *State) ByNumber(n int) *Task {
	for _, t := range s.Tasks {
		if t.Number == n {
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

// Dir is the project's state directory, keyed by its root path.
func Dir(root string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(base, "drover", filepath.Base(root)+"-"+hex.EncodeToString(sum[:4]))
}

func stateFile(dir string) string { return filepath.Join(dir, "state.json") }

func Load(root string) (*State, error) {
	data, err := os.ReadFile(stateFile(Dir(root)))
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
	dir := Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	file := stateFile(dir)
	if err := os.WriteFile(file+".tmp", data, 0o644); err != nil {
		return err
	}
	return os.Rename(file+".tmp", file)
}
