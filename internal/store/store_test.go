package store

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestLoadWithoutAStateFileStartsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, err := Load("/x/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s.NextNumber != 1 || len(s.Tasks) != 0 {
		t.Errorf("Load = %+v", s)
	}
}

func TestLoadRejectsCorruptState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := Dir("/x/repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("/x/repo"); err == nil {
		t.Fatal("Load accepted corrupt state")
	}
}

func TestLoadRaisesNextNumberToOne(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := (&State{}).Save("/x/repo"); err != nil {
		t.Fatal(err)
	}
	s, err := Load("/x/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s.NextNumber != 1 {
		t.Errorf("NextNumber = %d, want 1", s.NextNumber)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	want := &State{NextNumber: 3, Tasks: []*Task{{
		ID: "a", Number: 2, Title: "one", ColumnID: "todo",
		Sessions: map[string]*Session{MainSession: {Key: MainSession, Status: StatusRunning}},
	}}}
	if err := want.Save("/x/repo"); err != nil {
		t.Fatal(err)
	}
	got, err := Load("/x/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(stateFile(Dir("/x/repo")) + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

func TestCloneDeepCopies(t *testing.T) {
	orig := &Task{
		ID:       "a",
		Sessions: map[string]*Session{MainSession: {Key: MainSession, Pending: []Pending{{Text: "hi"}}}},
		Log:      []LogEntry{{Text: "started"}},
		SeenPRs:  []string{"u1"},
		Issue:    &Issue{Key: "DX-1"},
		PR:       &PR{Number: 7},
	}
	c := orig.Clone()
	c.Sessions[MainSession].Pending[0].Text = "changed"
	c.Sessions["extra"] = &Session{}
	c.Log[0].Text = "changed"
	c.SeenPRs[0] = "changed"
	c.Issue.Key = "changed"
	c.PR.Number = 8

	if orig.Sessions[MainSession].Pending[0].Text != "hi" || len(orig.Sessions) != 1 {
		t.Errorf("sessions shared: %+v", orig.Sessions)
	}
	if orig.Log[0].Text != "started" || orig.SeenPRs[0] != "u1" || orig.Issue.Key != "DX-1" || orig.PR.Number != 7 {
		t.Errorf("clone mutated the original: %+v", orig)
	}
}

func TestCloneKeepsNilIssueAndPR(t *testing.T) {
	c := (&Task{}).Clone()
	if c.Issue != nil || c.PR != nil {
		t.Errorf("Clone invented %+v / %+v", c.Issue, c.PR)
	}
}

func TestByNumber(t *testing.T) {
	two := &Task{Number: 2}
	s := &State{Tasks: []*Task{{Number: 1}, two}}
	if got := s.ByNumber(2); got != two {
		t.Errorf("ByNumber(2) = %+v", got)
	}
	if got := s.ByNumber(9); got != nil {
		t.Errorf("ByNumber(9) = %+v, want nil", got)
	}
}

func TestOwnerMembersAndDriver(t *testing.T) {
	owner := &Task{ID: "o"}
	a := &Task{ID: "a", GroupID: "o"}
	b := &Task{ID: "b", GroupID: "o"}
	other := &Task{ID: "x"}
	orphan := &Task{ID: "orphan", GroupID: "gone"}
	tasks := []*Task{owner, a, other, b, orphan}

	if got := Owner(tasks, a); got != owner {
		t.Errorf("Owner(a) = %+v", got)
	}
	if got := Owner(tasks, owner); got != owner {
		t.Errorf("Owner(owner) = %+v", got)
	}
	if got := Owner(tasks, orphan); got != orphan {
		t.Errorf("Owner of a card whose owner is gone = %+v, want itself", got)
	}
	if got := Members(tasks, owner); !reflect.DeepEqual(got, []*Task{owner, a, b}) {
		t.Errorf("Members = %+v", got)
	}

	for _, tc := range []struct {
		active string
		want   *Task
	}{
		{"", owner},
		{"b", b},
		{"o", owner},
		{"x", owner},
		{"gone", owner},
	} {
		owner.ActiveCard = tc.active
		if got := Driver(tasks, owner); got != tc.want {
			t.Errorf("ActiveCard %q: Driver = %s, want %s", tc.active, got.ID, tc.want.ID)
		}
	}
}

func TestAddLogKeepsTheNewestEntries(t *testing.T) {
	task := &Task{}
	for i := range maxLog + 5 {
		task.AddLog(fmt.Sprint(i))
	}
	if len(task.Log) != maxLog {
		t.Fatalf("len(Log) = %d, want %d", len(task.Log), maxLog)
	}
	if first, last := task.Log[0].Text, task.Log[maxLog-1].Text; first != "5" || last != fmt.Sprint(maxLog+4) {
		t.Errorf("kept %s..%s", first, last)
	}
}

func TestSetMode(t *testing.T) {
	s := &Session{}
	s.SetMode("plan")
	if s.PermissionMode != "plan" || s.ModeInherited {
		t.Errorf("SetMode(plan) = %q, inherited %v", s.PermissionMode, s.ModeInherited)
	}
	s.SetMode("")
	if s.PermissionMode != "" || !s.ModeInherited {
		t.Errorf("SetMode(\"\") = %q, inherited %v", s.PermissionMode, s.ModeInherited)
	}
}

func TestHasBackground(t *testing.T) {
	if (&Session{}).HasBackground() {
		t.Error("empty session has background tasks")
	}
	if !(&Session{Background: []string{"sleep 9"}}).HasBackground() {
		t.Error("session with a background task reports none")
	}
}

func TestPRIsReview(t *testing.T) {
	var none *PR
	for _, tc := range []struct {
		pr   *PR
		want bool
	}{{none, false}, {&PR{}, false}, {&PR{Review: true}, true}} {
		if got := tc.pr.IsReview(); got != tc.want {
			t.Errorf("%+v IsReview = %v, want %v", tc.pr, got, tc.want)
		}
	}
}
