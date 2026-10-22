package store

import (
	"path/filepath"
	"testing"
)

func TestDirIsPerRoot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	a, b := Dir("/x/repo"), Dir("/y/repo")
	if a == b || filepath.Dir(a) != "/state/drover" {
		t.Fatalf("Dir = %s, %s", a, b)
	}
}

func TestAgentSessionPrefersMainThenAnyRunning(t *testing.T) {
	main := &Session{Key: MainSession, Status: StatusSuspended}
	other := &Session{Key: "other", Status: StatusRunning}
	idle := &Session{Key: "idle", Status: StatusSuspended}

	if got := (&Task{Sessions: map[string]*Session{MainSession: main, "other": other}}).AgentSession(); got != main {
		t.Errorf("with a main session got %+v", got)
	}
	if got := (&Task{Sessions: map[string]*Session{"idle": idle, "other": other}}).AgentSession(); got != other {
		t.Errorf("without a main session got %+v, want the running one", got)
	}
	if got := (&Task{Sessions: map[string]*Session{"idle": idle}}).AgentSession(); got != nil {
		t.Errorf("with nothing running got %+v", got)
	}
}
