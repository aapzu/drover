package store

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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

// populate fills every field with a non-zero value, so a field Clone forgets to
// copy deeply shows up in TestCloneSharesNoMemory without being listed here.
func populate(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		populate(v.Elem())
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Unix(1700000000, 0)))
			return
		}
		for i := range v.NumField() {
			populate(v.Field(i))
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		populate(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key, val := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		populate(key)
		populate(val)
		v.SetMapIndex(key, val)
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	default:
		panic("populate: unsupported kind " + v.Kind().String())
	}
}

// shared returns the path of the first piece of memory a and b both point at.
// a and b must be deeply equal.
func shared(path string, a, b reflect.Value) string {
	switch a.Kind() {
	case reflect.Pointer:
		if a.Pointer() == b.Pointer() {
			return path
		}
		return shared("*"+path, a.Elem(), b.Elem())
	case reflect.Struct:
		if a.Type() == reflect.TypeFor[time.Time]() {
			return ""
		}
		for i := range a.NumField() {
			if p := shared(path+"."+a.Type().Field(i).Name, a.Field(i), b.Field(i)); p != "" {
				return p
			}
		}
	case reflect.Slice:
		if a.Len() > 0 && a.Index(0).Addr().UnsafePointer() == b.Index(0).Addr().UnsafePointer() {
			return path
		}
		for i := range min(a.Len(), b.Len()) {
			if p := shared(path+"[]", a.Index(i), b.Index(i)); p != "" {
				return p
			}
		}
	case reflect.Map:
		if a.Pointer() == b.Pointer() {
			return path
		}
		for _, k := range a.MapKeys() {
			if p := shared(path+"{}", a.MapIndex(k), b.MapIndex(k)); p != "" {
				return p
			}
		}
	}
	return ""
}

func TestCloneSharesNoMemory(t *testing.T) {
	orig := &Task{}
	populate(reflect.ValueOf(orig).Elem())
	c := orig.Clone()
	if !reflect.DeepEqual(orig, c) {
		t.Fatalf("Clone changed values:\n got %+v\nwant %+v", c, orig)
	}
	if p := shared("Task", reflect.ValueOf(orig).Elem(), reflect.ValueOf(c).Elem()); p != "" {
		t.Errorf("Clone still shares memory with the original at %s", p)
	}
}

func TestCloneOfAnEmptyTaskStaysEmpty(t *testing.T) {
	c := (&Task{}).Clone()
	if c.Issue != nil || c.PR != nil || len(c.Sessions) != 0 || len(c.Log) != 0 {
		t.Errorf("Clone invented data: %+v", c)
	}
}

func TestFailedSaveKeepsThePreviousState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := (&State{NextNumber: 2}).Save("/x/repo"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateFile(Dir("/x/repo"))+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (&State{NextNumber: 9}).Save("/x/repo"); err == nil {
		t.Fatal("Save succeeded despite the blocked temp file")
	}
	got, err := Load("/x/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got.NextNumber != 2 {
		t.Errorf("NextNumber = %d, want the previously saved 2", got.NextNumber)
	}
}

func TestDirFallsBackToTheHomeStateDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, xdg := range []string{"", "relative/state"} {
		t.Setenv("XDG_STATE_HOME", xdg)
		if got := filepath.Dir(Dir("/x/repo")); got != filepath.Join(home, ".local", "state", "drover") {
			t.Errorf("XDG_STATE_HOME=%q: state dir under %s", xdg, got)
		}
	}
}

func TestByNumber(t *testing.T) {
	two := &Task{Number: 2}
	s := &State{Tasks: []*Task{{Number: 1}, two, {Number: 3, Quick: true}}}
	if got := s.ByNumber(3); got != nil {
		t.Errorf("ByNumber(3) = %+v, want nil for a quick task", got)
	}
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
