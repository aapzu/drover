package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/store"
	"github.com/aapzu/drover/internal/tmux"
)

func TestStateStaysInTempDir(t *testing.T) {
	state := os.Getenv("XDG_STATE_HOME")
	if !strings.HasPrefix(state, filepath.Clean(os.TempDir())+string(filepath.Separator)) || !strings.HasPrefix(store.Dir("/repo"), state) {
		t.Fatalf("state for /repo is not under XDG_STATE_HOME %q", state)
	}
}

// TestMain keeps the tests' state and tmux server out of the real ones.
func TestMain(m *testing.M) {
	state, err := os.MkdirTemp("", "drover-ui-state")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_STATE_HOME", state); err != nil {
		panic(err)
	}
	tmux.Socket = fmt.Sprintf("kt-ui-test-%d", os.Getpid())
	code := m.Run()
	exec.Command("tmux", "-L", tmux.Socket, "kill-server").Run()
	os.RemoveAll(state)
	os.Exit(code)
}
