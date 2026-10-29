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

func TestSessionMovedFrom(t *testing.T) {
	for _, tc := range []struct {
		cwd, dir string
		want     bool
	}{
		{"", "/wt", false},
		{"/wt", "/wt", false},
		{"/root", "/wt", true},
	} {
		if got := (&Session{Cwd: tc.cwd}).MovedFrom(tc.dir); got != tc.want {
			t.Errorf("Cwd %q MovedFrom(%q) = %v, want %v", tc.cwd, tc.dir, got, tc.want)
		}
	}
}

func TestCloneDoesNotShareBackground(t *testing.T) {
	task := &Task{Sessions: map[string]*Session{MainSession: {Background: []string{"a", "b"}}}}
	clone := task.Clone()
	clone.Sessions[MainSession].Background[0] = "x"
	if got := task.Sessions[MainSession].Background[0]; got != "a" {
		t.Errorf("clone wrote through to the original: %q", got)
	}
}
