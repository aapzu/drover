package ui

import (
	"strings"
	"testing"
)

func TestHelpTextListsTheHelpScreensRows(t *testing.T) {
	text := HelpText()
	for _, want := range []string{"## Keys", "## Live mode", "- `b`: hide or unhide a card"} {
		if !strings.Contains(text, want) {
			t.Errorf("help text lacks %q", want)
		}
	}
}
