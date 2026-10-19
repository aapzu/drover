package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMovesLegacyStateOutOfRepo(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	root := filepath.Join(tmp, "repo")
	legacy := legacyDir(root)
	os.MkdirAll(filepath.Join(legacy, "sessions", "t1", MainSession), 0o755)
	old := &State{NextNumber: 2, Tasks: []*Task{{ID: "t1", Number: 1, Sessions: map[string]*Session{
		MainSession: {Key: MainSession, Dir: filepath.Join(legacy, "sessions", "t1", MainSession)},
	}}}}
	data, _ := json.Marshal(old)
	os.WriteFile(filepath.Join(legacy, "state.json"), data, 0o644)

	s, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks) != 1 || s.NextNumber != 2 {
		t.Fatalf("state = %+v", s)
	}
	want := filepath.Join(Dir(root), "sessions", "t1", MainSession)
	if got := s.Tasks[0].Sessions[MainSession].Dir; got != want {
		t.Fatalf("session dir = %s, want %s", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("session dir not moved: %v", err)
	}
	if target, err := os.Readlink(legacy); err != nil || target != Dir(root) {
		t.Fatalf("legacy dir should link to the new one: %s, %v", target, err)
	}
	if again, err := Load(root); err != nil || len(again.Tasks) != 1 {
		t.Fatalf("reload after migration: %+v, %v", again, err)
	}
}

func TestDirIsPerRoot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	a, b := Dir("/x/repo"), Dir("/y/repo")
	if a == b || filepath.Dir(a) != "/state/drover" {
		t.Fatalf("Dir = %s, %s", a, b)
	}
}
