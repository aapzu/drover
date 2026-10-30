package claude

import (
	"slices"
	"testing"
)

func TestPermissionModesAreTheOnesClaudeCodeTakes(t *testing.T) {
	want := []string{"plan", "acceptEdits", "auto", "bypassPermissions", "dontAsk", "default"}
	got := PermissionModes()
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("PermissionModes() = %v, want %v", got, want)
	}
}
