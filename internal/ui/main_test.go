package ui

import (
	"os"
	"testing"
)

// TestMain keeps the tests' state out of the real state directory.
func TestMain(m *testing.M) {
	state, err := os.MkdirTemp("", "drover-ui-state")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", state)
	code := m.Run()
	os.RemoveAll(state)
	os.Exit(code)
}
