package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/store"
)

var testState string

func TestStateStaysInTestDir(t *testing.T) {
	if dir := store.Dir("/repo"); !strings.HasPrefix(dir, testState+string(filepath.Separator)) {
		t.Fatalf("state dir %q is not under the test state dir %q", dir, testState)
	}
}

// TestMain keeps the tests' state out of the real state directory.
func TestMain(m *testing.M) {
	var err error
	testState, err = os.MkdirTemp("", "drover-ui-state")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_STATE_HOME", testState); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(testState)
	os.Exit(code)
}
