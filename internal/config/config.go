// Package config loads the board definition from drover.json at the repo root.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const FileName = "drover.json"

const (
	LocalFileName        = "drover.local.json"
	DefaultWorktreesDir  = ".drover/worktrees"
	DefaultMaxConcurrent = 8
	DefaultTodoModel     = "haiku"
)

// maxArchiveDays keeps a day count inside what a Duration can hold.
const maxArchiveDays = 100 * 365

type Board struct {
	DefaultBaseBranch     string        `json:"defaultBaseBranch,omitempty"`
	DefaultPermissionMode string        `json:"defaultPermissionMode,omitempty"`
	WorktreesDir          string        `json:"worktreesDir,omitempty"`
	ArchiveDoneAfter      string        `json:"archiveDoneAfter,omitempty"`
	CopyFiles             []string      `json:"copyFiles,omitempty"`
	InitScript            string        `json:"initScript,omitempty"`
	MaxConcurrent         int           `json:"maxConcurrent,omitempty"`
	IdleTimeoutMinutes    int           `json:"idleTimeoutMinutes,omitempty"`
	Notifications         Notifications `json:"notifications"`
	AutoName              bool          `json:"autoName"`
	Columns               []Column      `json:"columns"`
	PRFlow                *PRFlow       `json:"prFlow,omitempty"`
	ReviewFlow            *ReviewFlow   `json:"reviewFlow,omitempty"`
}

// Notifications picks the agent events that raise a desktop notification and
// a notice on the board. Each kind repeats at most once per cooldown per agent.
type Notifications struct {
	TurnDone        bool `json:"turnDone"`
	NeedsYou        bool `json:"needsYou"`
	PlanReady       bool `json:"planReady"`
	Exited          bool `json:"exited"`
	CooldownSeconds int  `json:"cooldownSeconds"`
}

func DefaultNotifications() Notifications {
	return Notifications{TurnDone: true, NeedsYou: true, PlanReady: true, Exited: true, CooldownSeconds: 10}
}

// ReviewFlow moves the cards of pull requests you review. Started is where a
// card goes once the agent's first review turn ends, Reviewed once you have
// submitted a review, Updated when the author pushes after it, and Done when
// the PR merges or closes. Targets are column ids or names; an empty one
// leaves cards where they are.
type ReviewFlow struct {
	Started  string `json:"started,omitempty"`
	Reviewed string `json:"reviewed,omitempty"`
	Updated  string `json:"updated,omitempty"`
	Done     string `json:"done,omitempty"`
}

// PRFlow moves the cards of pull requests as they progress on GitHub. Each
// target is a column id or name; an empty one leaves cards where they are.
type PRFlow struct {
	Requested               string `json:"requested,omitempty"`
	Opened                  string `json:"opened,omitempty"`
	ChangesRequested        string `json:"changesRequested,omitempty"`
	ChangesRequestedMessage string `json:"changesRequestedMessage,omitempty"`
	Merged                  string `json:"merged,omitempty"`
	Released                string `json:"released,omitempty"`
	ReleaseBranch           string `json:"releaseBranch,omitempty"`
}

type Column struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Role           string      `json:"role,omitempty"`
	Color          string      `json:"color,omitempty"`
	AutoSpawn      bool        `json:"autoSpawn,omitempty"`
	PermissionMode string      `json:"permissionMode,omitempty"`
	ModelOverride  string      `json:"modelOverride,omitempty"`
	EffortOverride string      `json:"effortOverride,omitempty"`
	PlanExitTarget string      `json:"planExitTarget,omitempty"`
	Automations    Automations `json:"automations,omitzero"`
}

type Automations struct {
	OnEnter   []Automation `json:"onEnter,omitempty"`
	OnTurnEnd []Automation `json:"onTurnEnd,omitempty"`
}

type Automation struct {
	Name            string `json:"name,omitempty"`
	Type            string `json:"type"`
	Message         string `json:"message,omitempty"`
	TargetColumn    string `json:"targetColumn,omitempty"`
	Condition       string `json:"condition,omitempty"`
	ConditionScript string `json:"conditionScript,omitempty"`
	Script          string `json:"script,omitempty"`
	TimeoutMinutes  int    `json:"timeoutMinutes,omitempty"`
	Body            string `json:"body,omitempty"`
	Title           string `json:"title,omitempty"`
}

// Conditional reports whether the row is gated.
func (a Automation) Conditional() bool { return a.Condition != "" || a.ConditionScript != "" }

func (c Column) IsTodo() bool { return c.Role == "todo" }
func (c Column) IsDone() bool { return c.Role == "done" }

// IsAgent columns run an agent for the cards in them.
func (c Column) IsAgent() bool { return c.AutoSpawn && !c.IsTodo() && !c.IsDone() }

// Load reads drover.json and applies drover.local.json on top of it.
// A missing drover.json yields the default board and found=false.
func Load(root string) (board *Board, found bool, err error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		board = Default()
	case err != nil:
		return nil, false, err
	default:
		found = true
		board = &Board{Notifications: DefaultNotifications(), AutoName: true}
		if err := json.Unmarshal(data, board); err != nil {
			return nil, true, fmt.Errorf("parse %s: %w", FileName, err)
		}
	}
	if err := board.applyLocal(root); err != nil {
		return nil, found, err
	}
	if err := board.validate(); err != nil {
		return nil, found, err
	}
	// A worktrees dir holding the checkout itself would make it look board-owned.
	if rel, err := filepath.Rel(board.WorktreesPath(root), root); err == nil && (rel == "." || filepath.IsLocal(rel)) {
		return nil, found, fmt.Errorf("worktreesDir %q must not contain the repo root", board.WorktreesDir)
	}
	board.defaultTodoModel()
	return board, found, nil
}

func (b *Board) defaultTodoModel() {
	if col := &b.Columns[b.todoIndex()]; col.ModelOverride == "" {
		col.ModelOverride = DefaultTodoModel
	}
}

// applyLocal overlays your personal settings from drover.local.json.
func (b *Board) applyLocal(root string) error {
	data, err := os.ReadFile(filepath.Join(root, LocalFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var local struct {
		DefaultBaseBranch  string          `json:"defaultBaseBranch"`
		WorktreesDir       string          `json:"worktreesDir"`
		MaxConcurrent      *int            `json:"maxConcurrent"`
		IdleTimeoutMinutes *int            `json:"idleTimeoutMinutes"`
		AutoName           *bool           `json:"autoName"`
		Notifications      json.RawMessage `json:"notifications"`
	}
	if err := json.Unmarshal(data, &local); err != nil {
		return fmt.Errorf("parse %s: %w", LocalFileName, err)
	}
	if local.DefaultBaseBranch != "" {
		b.DefaultBaseBranch = local.DefaultBaseBranch
	}
	if local.WorktreesDir != "" {
		b.WorktreesDir = local.WorktreesDir
	}
	if local.MaxConcurrent != nil {
		b.MaxConcurrent = *local.MaxConcurrent
	}
	if local.IdleTimeoutMinutes != nil {
		b.IdleTimeoutMinutes = *local.IdleTimeoutMinutes
	}
	if local.AutoName != nil {
		b.AutoName = *local.AutoName
	}
	if local.Notifications != nil {
		if err := json.Unmarshal(local.Notifications, &b.Notifications); err != nil {
			return fmt.Errorf("parse %s: notifications: %w", LocalFileName, err)
		}
	}
	return nil
}

// Stamp changes whenever drover.json or drover.local.json does.
func Stamp(root string) string {
	var stamp string
	for _, name := range []string{FileName, LocalFileName} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil {
			stamp += info.ModTime().String() + fmt.Sprint(info.Size())
		}
		stamp += "|"
	}
	return stamp
}

// Limit is how many agents may run at once.
func (b *Board) Limit() int {
	if b.MaxConcurrent > 0 {
		return b.MaxConcurrent
	}
	return DefaultMaxConcurrent
}

// parseAge reads a duration such as "36h" or "7d"; Go's own syntax has no
// days, so those are whole numbers.
func parseAge(s string) (time.Duration, error) {
	var d time.Duration
	var err error
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n int
		if n, err = strconv.Atoi(days); err == nil && n > 0 && n < maxArchiveDays {
			d = time.Duration(n) * 24 * time.Hour
		}
	} else {
		d, err = time.ParseDuration(s)
	}
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("%q is not a duration of a minute or more, like 7d or 36h", s)
	}
	return d, nil
}

// ArchiveAfter is how long a card may sit in Done before it is archived;
// zero when archiveDoneAfter is unset.
func (b *Board) ArchiveAfter() time.Duration {
	d, _ := parseAge(b.ArchiveDoneAfter)
	return d
}

func (b *Board) validate() error {
	if len(b.Columns) == 0 {
		return fmt.Errorf("%s has no columns", FileName)
	}
	if b.ArchiveDoneAfter != "" {
		if _, err := parseAge(b.ArchiveDoneAfter); err != nil {
			return fmt.Errorf("%s: archiveDoneAfter %w", FileName, err)
		}
	}
	seen := map[string]bool{}
	for _, c := range b.Columns {
		if c.ID == "" || c.Name == "" {
			return fmt.Errorf("%s: every column needs an id and a name", FileName)
		}
		if seen[c.ID] {
			return fmt.Errorf("%s: duplicate column id %s", FileName, c.ID)
		}
		seen[c.ID] = true
	}
	if f := b.PRFlow; f != nil {
		if err := b.checkColumns("prFlow", f.Requested, f.Opened, f.ChangesRequested, f.Merged, f.Released); err != nil {
			return err
		}
		if c, _ := b.Find(f.Requested); f.Requested != "" && !c.IsAgent() {
			return fmt.Errorf("%s: prFlow.requested must be an autoSpawn column", FileName)
		}
	}
	if f := b.ReviewFlow; f != nil {
		return b.checkColumns("reviewFlow", f.Started, f.Reviewed, f.Updated, f.Done)
	}
	return nil
}

// checkColumns rejects a flow target that names no column. Empty ones are fine.
func (b *Board) checkColumns(flow string, refs ...string) error {
	for _, ref := range refs {
		if _, ok := b.Find(ref); ref != "" && !ok {
			return fmt.Errorf("%s: %s names unknown column %q", FileName, flow, ref)
		}
	}
	return nil
}

// WorktreesPath is where task worktrees live: worktreesDir resolved against
// the repo root, or .drover/worktrees when unset.
func (b *Board) WorktreesPath(root string) string {
	dir := b.WorktreesDir
	if dir == "" {
		dir = DefaultWorktreesDir
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(root, dir)
}

func (b *Board) Save(root string) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, FileName), append(data, '\n'), 0o644)
}

func (b *Board) Column(id string) (Column, int, bool) {
	for i, c := range b.Columns {
		if c.ID == id {
			return c, i, true
		}
	}
	return Column{}, -1, false
}

// Find resolves a column by id or by case-insensitive name, as automation
// targets may use either.
func (b *Board) Find(ref string) (Column, bool) {
	if c, _, ok := b.Column(ref); ok {
		return c, true
	}
	for _, c := range b.Columns {
		if strings.EqualFold(c.Name, ref) {
			return c, true
		}
	}
	return Column{}, false
}

// PermissionMode is the column's own mode, else the board default, else ""
// to leave the choice to Claude Code.
func (b *Board) PermissionMode(c Column) string {
	switch {
	case c.PermissionMode != "":
		return c.PermissionMode
	case b.DefaultPermissionMode != "":
		return b.DefaultPermissionMode
	default:
		return ""
	}
}

func (b *Board) TodoColumn() Column { return b.Columns[b.todoIndex()] }

func (b *Board) todoIndex() int {
	for i, c := range b.Columns {
		if c.IsTodo() {
			return i
		}
	}
	return 0
}

func Default() *Board {
	return &Board{
		ArchiveDoneAfter: "7d",
		Notifications:    DefaultNotifications(),
		AutoName:         true,
		Columns: []Column{
			{ID: "todo", Name: "Sessions", Role: "todo", Color: "#6b7280"},
			{ID: "planning", Name: "Planning", Color: "#8b5cf6", AutoSpawn: true, PermissionMode: "plan", PlanExitTarget: "Executing"},
			{ID: "executing", Name: "Executing", Color: "#3b82f6", AutoSpawn: true, Automations: Automations{OnTurnEnd: []Automation{{
				Name: "Waiting for me", Type: "move_to_column", TargetColumn: "self-review",
				Condition: "The reply ends by asking me for a decision or input",
			}}}},
			{ID: "self-review", Name: "Waiting for me", Color: "#06b6d4", AutoSpawn: true},
			{ID: "in-review", Name: "In review", Color: "#f59e0b", AutoSpawn: true,
				Automations: Automations{OnEnter: []Automation{{Name: "Review", Type: "send_message", Message: "/pr-review-toolkit:review-pr"}}}},
			{ID: "verify", Name: "Verify in develop", Color: "#ec4899"},
			{ID: "done", Name: "Done", Role: "done", Color: "#10b981"},
		},
		PRFlow: &PRFlow{
			Requested:        "self-review",
			Opened:           "in-review",
			ChangesRequested: "executing",
			ChangesRequestedMessage: "PR #{{prNumber}} ({{prUrl}}) got review comments. Read them with `gh pr view {{prUrl}} --comments` " +
				"and the inline ones with `gh api repos/{owner}/{repo}/pulls/{{prNumber}}/comments`, address them, push, and reply to each.",
			Merged:   "verify",
			Released: "done",
		},
	}
}
