// Package config loads the board definition from kangentic.json, the same
// file the Kangentic desktop app keeps at the repo root.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const FileName = "kangentic.json"

type Board struct {
	Version               int      `json:"version"`
	DefaultBaseBranch     string   `json:"defaultBaseBranch,omitempty"`
	DefaultPermissionMode string   `json:"defaultPermissionMode,omitempty"`
	Columns               []Column `json:"columns"`
	PRFlow                *PRFlow  `json:"prFlow,omitempty"`
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
	ID                   string      `json:"id"`
	Name                 string      `json:"name"`
	Role                 string      `json:"role,omitempty"`
	Color                string      `json:"color,omitempty"`
	AutoSpawn            bool        `json:"autoSpawn,omitempty"`
	PermissionMode       string      `json:"permissionMode,omitempty"`
	ModelOverride        string      `json:"modelOverride,omitempty"`
	EffortOverride       string      `json:"effortOverride,omitempty"`
	PlanExitTarget       string      `json:"planExitTarget,omitempty"`
	SessionTarget        string      `json:"sessionTarget,omitempty"`
	SessionSpawnStrategy string      `json:"sessionSpawnStrategy,omitempty"`
	Automations          Automations `json:"automations,omitzero"`
}

type Automations struct {
	OnEnter   []Automation `json:"onEnter,omitempty"`
	OnTurnEnd []Automation `json:"onTurnEnd,omitempty"`
	OnExit    []Automation `json:"onExit,omitempty"`
}

type Automation struct {
	Name           string            `json:"name,omitempty"`
	Type           string            `json:"type"`
	Message        string            `json:"message,omitempty"`
	Mode           string            `json:"mode,omitempty"`
	TargetColumn   string            `json:"targetColumn,omitempty"`
	FinalLine      string            `json:"finalLine,omitempty"`
	Condition      string            `json:"condition,omitempty"`
	MaxRuns        int               `json:"maxRuns,omitempty"`
	Script         string            `json:"script,omitempty"`
	TimeoutMinutes int               `json:"timeoutMinutes,omitempty"`
	URL            string            `json:"url,omitempty"`
	Method         string            `json:"method,omitempty"`
	Body           string            `json:"body,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Title          string            `json:"title,omitempty"`
}

func (c Column) IsTodo() bool { return c.Role == "todo" }
func (c Column) IsDone() bool { return c.Role == "done" }

// IsAgent columns run an agent for the cards in them.
func (c Column) IsAgent() bool { return c.AutoSpawn && !c.IsTodo() && !c.IsDone() }

// Isolated columns run their own agent session instead of the task's main one.
func (c Column) Isolated() bool { return c.SessionTarget == "isolated" }

func (c Column) AlwaysSpawnNew() bool { return c.SessionSpawnStrategy == "always_spawn_new" }

// Load reads kangentic.json and applies kangentic.local.json on top of it.
// A missing file yields the default board and found=false.
func Load(root string) (board *Board, found bool, err error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return Default(), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	board = &Board{}
	if err := json.Unmarshal(data, board); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if local, err := os.ReadFile(filepath.Join(root, "kangentic.local.json")); err == nil {
		var override struct {
			DefaultBaseBranch string `json:"defaultBaseBranch"`
		}
		if json.Unmarshal(local, &override) == nil && override.DefaultBaseBranch != "" {
			board.DefaultBaseBranch = override.DefaultBaseBranch
		}
	}
	if err := board.validate(); err != nil {
		return nil, true, err
	}
	return board, true, nil
}

func (b *Board) validate() error {
	if len(b.Columns) == 0 {
		return fmt.Errorf("%s has no columns", FileName)
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
		for _, ref := range []string{f.Requested, f.Opened, f.ChangesRequested, f.Merged, f.Released} {
			if _, ok := b.Find(ref); ref != "" && !ok {
				return fmt.Errorf("%s: prFlow names unknown column %q", FileName, ref)
			}
		}
		if c, _ := b.Find(f.Requested); f.Requested != "" && !c.IsAgent() {
			return fmt.Errorf("%s: prFlow.requested must be an autoSpawn column", FileName)
		}
	}
	return nil
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

// PermissionMode is the column's own mode, else the board default, else auto.
func (b *Board) PermissionMode(c Column) string {
	switch {
	case c.PermissionMode != "":
		return c.PermissionMode
	case b.DefaultPermissionMode != "":
		return b.DefaultPermissionMode
	default:
		return "auto"
	}
}

func (b *Board) TodoColumn() Column {
	for _, c := range b.Columns {
		if c.IsTodo() {
			return c
		}
	}
	return b.Columns[0]
}

func Default() *Board {
	return &Board{
		Version: 1,
		Columns: []Column{
			{ID: "todo", Name: "Sessions", Role: "todo", Color: "#6b7280"},
			{ID: "planning", Name: "Planning", Color: "#8b5cf6", AutoSpawn: true, PermissionMode: "plan", PlanExitTarget: "Executing"},
			{ID: "executing", Name: "Executing", Color: "#3b82f6", AutoSpawn: true},
			{ID: "self-review", Name: "Self-review", Color: "#06b6d4", AutoSpawn: true,
				Automations: Automations{OnEnter: []Automation{{Name: "Review", Type: "send_message", Message: "/pr-review-toolkit:review-pr"}}}},
			{ID: "in-review", Name: "In review", Color: "#f59e0b"},
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
