package ui

import (
	"os"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type csiMsg []byte

func captureTerminal(t *testing.T) *strings.Builder {
	t.Helper()
	var term strings.Builder
	terminalOut = &term
	t.Cleanup(func() { terminalOut = os.Stdout })
	return &term
}

func TestFilterKeysShiftEnter(t *testing.T) {
	cases := []struct {
		name string
		mode mode
		seq  string
		alt  bool
	}{
		{"live modifyOtherKeys", modeLive, "\x1b[27;2;13~", true},
		{"live csi u", modeLive, "\x1b[13;2u", true},
		{"assistant", modeAssistant, "\x1b[27;2;13~", true},
		{"board", modeBoard, "\x1b[27;2;13~", false},
	}
	for _, c := range cases {
		m := testModel(120, 40, 2, 0)
		m.mode = c.mode
		got, ok := FilterKeys(m, csiMsg(c.seq)).(tea.KeyMsg)
		if !ok || got.Type != tea.KeyEnter || got.Alt != c.alt {
			t.Errorf("%s: got %#v", c.name, got)
		}
	}
}

func TestFilterKeysPassesOtherMessages(t *testing.T) {
	m := testModel(120, 40, 2, 0)
	m.mode = modeLive
	if _, ok := FilterKeys(m, csiMsg("\x1b[27;5;13~")).(csiMsg); !ok {
		t.Error("other CSI sequence was rewritten")
	}
	for _, msg := range []tea.Msg{tea.KeyMsg{Type: tea.KeyEnter}, tea.WindowSizeMsg{Width: 1}, "text"} {
		if got := FilterKeys(m, msg); !reflect.DeepEqual(got, msg) {
			t.Errorf("%#v changed to %#v", msg, got)
		}
	}
}
