package config

import "testing"

func TestQuickTasksRunInAWorktreeUnlessTheBoardSaysRoot(t *testing.T) {
	if got := (&Board{}).QuickDir(); got != QuickWorktree {
		t.Errorf("default quick dir = %q, want %q", got, QuickWorktree)
	}
	if got := (&Board{QuickWorkdir: QuickRoot}).QuickDir(); got != QuickRoot {
		t.Errorf("quick dir = %q, want %q", got, QuickRoot)
	}
}

func TestQuickColumnCarriesTheBoardDefaults(t *testing.T) {
	col := (&Board{DefaultModel: "opus", DefaultPermissionMode: "acceptEdits", QuickWorkdir: QuickRoot}).QuickColumn()
	if !col.AutoSpawn || !col.NoWorktree || col.ModelOverride != "opus" || col.ID != "" {
		t.Errorf("root quick column = %+v", col)
	}
	col = (&Board{}).QuickColumn()
	if col.NoWorktree || col.ModelOverride != DefaultAgentModel {
		t.Errorf("default quick column = %+v, want a worktree and the default model", col)
	}
}
