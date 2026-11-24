package ui

import (
	"io"
	"reflect"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	extendedKeysOn = "\x1b[>4;1m"
	// ExtendedKeysOff is written once the program exits.
	ExtendedKeysOff = "\x1b[>4;0m"
)

// enableExtendedKeys is also needed after an attach: tmux resets the mode when
// its client exits.
func enableExtendedKeys() tea.Msg {
	io.WriteString(terminalOut, extendedKeysOn)
	return nil
}

// FilterKeys turns the shift+enter sequences Bubble Tea doesn't parse into a
// key press. Live mode and the assistant forward it as alt+enter, which Claude
// reads as a newline; everywhere else it stays a plain enter.
func FilterKeys(model tea.Model, msg tea.Msg) tea.Msg {
	if !isShiftEnter(msg) {
		return msg
	}
	m, _ := model.(Model)
	return tea.KeyMsg{Type: tea.KeyEnter, Alt: m.mode == modeLive || m.mode == modeAssistant}
}

// isShiftEnter matches by kind because Bubble Tea's unknown-CSI message type
// is unexported.
func isShiftEnter(msg tea.Msg) bool {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Uint8 {
		return false
	}
	switch string(v.Bytes()) {
	case "\x1b[27;2;13~", "\x1b[13;2u":
		return true
	}
	return false
}
