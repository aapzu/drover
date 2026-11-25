package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testRoot returns an empty repo root whose global config and state
// directories belong to the test.
func testRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return t.TempDir()
}

func writeLayer(t *testing.T, root string, layer Layer, content string) string {
	t.Helper()
	file := layer.Path(root)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func readLayer(root string, layer Layer) string {
	data, _ := os.ReadFile(layer.Path(root))
	return string(data)
}

func TestSetKeepsTheRestOfTheFileInOrder(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)

	if err := Set(dir, Global, []string{"archiveDoneAfter"}, "30d"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Global.Path(dir))
	if !strings.Contains(string(data), `"_modifiedBy": "7ef4fd0cb484"`) {
		t.Errorf("lost an unrelated key:\n%s", data)
	}
	if strings.Index(string(data), `"columns"`) > strings.Index(string(data), `"archiveDoneAfter"`) {
		t.Errorf("reordered the file:\n%s", data)
	}
	board, _, err := Load(dir)
	if err != nil || board.ArchiveDoneAfter != "30d" {
		t.Fatalf("archiveDoneAfter=%q err=%v", board.ArchiveDoneAfter, err)
	}
}

func TestSetNestedKeyCreatesTheProjectFile(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)

	if err := Set(dir, Project, []string{"notifications", "turnDone"}, false); err != nil {
		t.Fatal(err)
	}
	board, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if board.Notifications.TurnDone || !board.Notifications.NeedsYou {
		t.Errorf("notifications=%+v", board.Notifications)
	}
	if !Sets(dir, Project, []string{"notifications", "turnDone"}) || Sets(dir, Project, []string{"notifications", "needsYou"}) {
		t.Errorf("Sets does not see the nested key")
	}
}

func TestUnsetFallsBackToTheDefault(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	if err := Set(dir, Project, []string{"autoName"}, false); err != nil {
		t.Fatal(err)
	}
	if err := Unset(dir, Project, []string{"autoName"}); err != nil {
		t.Fatal(err)
	}
	if Sets(dir, Project, []string{"autoName"}) {
		t.Fatal("override is still there")
	}
	board, _, _ := Load(dir)
	if !board.AutoName {
		t.Error("autoName did not fall back to true")
	}
}

func TestUnsetLastNestedKeyRemovesTheParent(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	Set(dir, Project, []string{"notifications", "exited"}, false)
	if err := Unset(dir, Project, []string{"notifications", "exited"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Project.Path(dir))
	if strings.Contains(string(data), "notifications") {
		t.Errorf("empty parent left behind:\n%s", data)
	}
}

func TestUnsetNestedKeyKeepsSiblings(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	Set(dir, Project, []string{"notifications", "turnDone"}, false)
	Set(dir, Project, []string{"notifications", "exited"}, false)
	if err := Unset(dir, Project, []string{"notifications", "exited"}); err != nil {
		t.Fatal(err)
	}
	if !Sets(dir, Project, []string{"notifications", "turnDone"}) {
		t.Error("unsetting one nested key removed its sibling")
	}
}

func TestUnsetWithoutProjectFileIsNoOp(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	if err := Unset(dir, Project, []string{"autoName"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Project.Path(dir)); !os.IsNotExist(err) {
		t.Errorf("unset created the project config: %v", err)
	}
}

func TestSetsIsFalseForUnusableFiles(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		path          []string
	}{
		{"truncated", `{"autoName":`, []string{"autoName"}},
		{"trailing data", `{"autoName":false} x`, []string{"autoName"}},
		{"array root", `[]`, []string{"autoName"}},
		{"empty", ``, []string{"autoName"}},
		{"scalar parent", `{"notifications":5}`, []string{"notifications", "turnDone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := testRoot(t)
			writeLayer(t, dir, Project, tc.content)
			if Sets(dir, Project, tc.path) {
				t.Errorf("Sets is true for %q", tc.content)
			}
		})
	}
}

func TestSetWritesAmpersandsReadably(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	if err := Set(dir, Global, []string{"initScript"}, "make && make test"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Global.Path(dir))
	if !strings.Contains(string(data), `"make && make test"`) {
		t.Errorf("value was HTML-escaped:\n%s", data)
	}
}

func TestSetKeepsTheFileMode(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	file := writeLayer(t, dir, Project, `{}`)
	os.Chmod(file, 0o600)
	if err := Set(dir, Project, []string{"autoName"}, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode=%v err=%v, want 0600", info.Mode().Perm(), err)
	}
}

func TestSetThroughASymlinkedFileUpdatesTheTarget(t *testing.T) {
	dir := testRoot(t)
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	os.WriteFile(target, []byte(sample), 0o644)
	link := Global.Path(dir)
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Set(dir, Global, []string{"archiveDoneAfter"}, "30d"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced by a regular file: %v, %v", info, err)
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), `"archiveDoneAfter": "30d"`) {
		t.Errorf("the link target was not updated:\n%s", data)
	}
}

func TestEditRejectsAnUnknownLayerAndAnEmptyPath(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	if err := Set(dir, Layer(7), []string{"autoName"}, false); err == nil {
		t.Error("expected an error for an unknown layer")
	}
	if err := Set(dir, Project, nil, false); err == nil {
		t.Error("expected an error for an empty path")
	}
	if Sets(dir, Project, nil) {
		t.Error("an empty path can't be set")
	}
	if _, err := os.Stat(Project.Path(dir)); !os.IsNotExist(err) {
		t.Errorf("a rejected edit created the project config: %v", err)
	}
}

func TestEditCreatesAMissingFile(t *testing.T) {
	dir := testRoot(t)
	if err := Set(dir, Global, []string{"archiveDoneAfter"}, "30d"); err != nil {
		t.Fatal(err)
	}
	board, found, err := Load(dir)
	if err != nil || found || board.ArchiveDoneAfter != "30d" {
		t.Fatalf("found=%v archive=%q err=%v", found, board.ArchiveDoneAfter, err)
	}
}

func TestEditingTheDefaultBoardWritesItToTheLayerFirst(t *testing.T) {
	for _, tc := range []struct {
		name string
		path []string
		val  any
	}{
		{"column", ColumnPath("executing", "modelOverride"), "haiku"},
		{"flow", []string{"prFlow", "changesRequestedMode"}, ChangesModeFix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := testRoot(t)
			if err := Set(dir, Global, tc.path, tc.val); err != nil {
				t.Fatal(err)
			}
			board, found, err := Load(dir)
			if err != nil || !found {
				t.Fatalf("found=%v err=%v", found, err)
			}
			def := Default()
			if len(board.Columns) != len(def.Columns) || board.PRFlow.Opened != def.PRFlow.Opened || board.ReviewFlow.Started != def.ReviewFlow.Started {
				t.Errorf("the default board was not copied whole: %d columns, prFlow=%+v", len(board.Columns), board.PRFlow)
			}
		})
	}
}

func TestEditingColumnsOfALayerBelowTheBoardIsRejected(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	err := Set(dir, Project, ColumnPath("dc9f2b38", "color"), "#fff")
	if err == nil || !strings.Contains(err.Error(), "global") {
		t.Fatalf("err=%v, want a pointer to the global config", err)
	}
	if _, err := os.Stat(Project.Path(dir)); !os.IsNotExist(err) {
		t.Errorf("a rejected edit created the project config: %v", err)
	}
}

func TestColumnLayerIsTheOneThatDefinesColumns(t *testing.T) {
	dir := testRoot(t)
	if got := ColumnLayer(dir); got != Global {
		t.Errorf("without columns anywhere = %v, want global", got)
	}
	writeLayer(t, dir, Global, sample)
	if got := ColumnLayer(dir); got != Global {
		t.Errorf("columns in global = %v", got)
	}
	writeLayer(t, dir, Project, `{"columns":[{"id":"a","name":"A","autoSpawn":true}]}`)
	if got := ColumnLayer(dir); got != Project {
		t.Errorf("columns in project = %v", got)
	}
}

func TestRejectedEditLeavesTheFileAsItWas(t *testing.T) {
	cases := []struct {
		name   string
		layer  Layer
		before string
		path   []string
		value  any
	}{
		{"bad global value", Global, sample, []string{"archiveDoneAfter"}, "soon"},
		{"bad project value leaves no new file", Project, "", []string{"maxConcurrent"}, "many"},
		{"trailing data", Project, `{"autoName":false} junk`, []string{"maxConcurrent"}, 4},
		{"truncated file", Project, `{"autoName":false`, []string{"maxConcurrent"}, 4},
		{"scalar parent", Project, `{"notifications": 5}`, []string{"notifications", "turnDone"}, false},
	}
	for _, tc := range cases {
		dir := testRoot(t)
		writeLayer(t, dir, Global, sample)
		file := tc.layer.Path(dir)
		if tc.before != "" {
			writeLayer(t, dir, tc.layer, tc.before)
		}
		if err := Set(dir, tc.layer, tc.path, tc.value); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
		data, err := os.ReadFile(file)
		if tc.before == "" {
			if !os.IsNotExist(err) {
				t.Errorf("%s: rejected file was left behind: %v", tc.name, err)
			}
		} else if string(data) != tc.before {
			t.Errorf("%s: file changed after a rejected edit:\n%s", tc.name, data)
		}
	}
}

func TestSetColumnSettingKeepsTheRest(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)

	if err := Set(dir, Global, ColumnPath("dc9f2b38", "effortOverride"), "high"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Global.Path(dir))
	if !strings.Contains(string(data), `"_modifiedBy": "7ef4fd0cb484"`) || !strings.Contains(string(data), `"Apply findings"`) {
		t.Errorf("lost an unrelated key:\n%s", data)
	}
	board, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c, _, _ := board.Column("dc9f2b38"); c.EffortOverride != "high" || c.PermissionMode != "auto" {
		t.Errorf("executing = %+v", c)
	}
	if c, _, _ := board.Column("4873bb89"); c.EffortOverride != "xhigh" {
		t.Errorf("planning = %+v", c)
	}
	if !Sets(dir, Global, ColumnPath("dc9f2b38", "effortOverride")) || Sets(dir, Global, ColumnPath("dc9f2b38", "color")) {
		t.Error("Sets does not see the column key")
	}
	if err := Unset(dir, Global, ColumnPath("dc9f2b38", "effortOverride")); err != nil {
		t.Fatal(err)
	}
	if Sets(dir, Global, ColumnPath("dc9f2b38", "effortOverride")) {
		t.Error("effortOverride is still set")
	}
}

func TestSetColumnSettingWritesNamesReadably(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)

	if err := Set(dir, Global, ColumnPath("dc9f2b38", "name"), `Build & "ship" <it>`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Global.Path(dir))
	if !strings.Contains(string(data), `"Build & \"ship\" <it>"`) || !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("name was escaped or the file reformatted oddly:\n%s", data)
	}
	if board, _, err := Load(dir); err != nil {
		t.Fatal(err)
	} else if c, _, _ := board.Column("dc9f2b38"); c.Name != `Build & "ship" <it>` {
		t.Errorf("name = %q", c.Name)
	}
}

func TestSetColumnSettingNeverAddsOrDropsAColumn(t *testing.T) {
	cases := []struct {
		name  string
		board string
		run   func(dir string) error
	}{
		{"unknown column", sample, func(dir string) error { return Set(dir, Global, ColumnPath("no-such", "color"), "#fff") }},
		{"no columns key", `{"autoName":false}`, func(dir string) error { return Set(dir, Global, ColumnPath("x", "name"), "X") }},
		{"unsetting the name", sample, func(dir string) error { return Unset(dir, Global, ColumnPath("64f9ef9a", "name")) }},
		{"keepAgent on an agent column", sample, func(dir string) error { return Set(dir, Global, ColumnPath("dc9f2b38", "keepAgent"), true) }},
		{"a whole column", sample, func(dir string) error { return Set(dir, Global, ColumnPath("dc9f2b38"), "x") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := testRoot(t)
			writeLayer(t, dir, Global, tc.board)
			if err := tc.run(dir); err == nil {
				t.Error("expected an error")
			}
			if data, _ := os.ReadFile(Global.Path(dir)); string(data) != tc.board {
				t.Errorf("file changed after a rejected edit:\n%s", data)
			}
		})
	}
}

func TestColumnPathsOnlyAddressTheColumnsArray(t *testing.T) {
	dir := testRoot(t)
	const local = `{"flows":[{"id":"a","x":1}]}`
	writeLayer(t, dir, Global, sample)
	writeLayer(t, dir, Project, local)

	if Sets(dir, Project, []string{"flows", "a", "x"}) {
		t.Error("Sets looked inside an array other than columns")
	}
	if err := Set(dir, Project, []string{"flows", "a", "x"}, 2); err == nil {
		t.Error("expected an error addressing another array by id")
	}
	if data, _ := os.ReadFile(Project.Path(dir)); string(data) != local {
		t.Errorf("file changed:\n%s", data)
	}
}

func TestSetColumnSettingRejectsMalformedColumns(t *testing.T) {
	dir := testRoot(t)
	const local = `{"columns":[{"id":5}]}`
	writeLayer(t, dir, Global, sample)
	writeLayer(t, dir, Project, local)

	if err := Set(dir, Project, ColumnPath("dc9f2b38", "color"), "#fff"); err == nil {
		t.Error("expected an error for a column whose id isn't a string")
	}
	if Sets(dir, Project, ColumnPath("dc9f2b38", "color")) {
		t.Error("Sets matched a malformed element")
	}
}

func TestGlobalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := t.TempDir()
	for _, tc := range []struct{ name, env, want string }{
		{"absolute XDG_CONFIG_HOME", xdg, filepath.Join(xdg, "drover", FileName)},
		{"unset", "", filepath.Join(home, ".config", "drover", FileName)},
		{"relative is ignored", "rel", filepath.Join(home, ".config", "drover", FileName)},
	} {
		t.Setenv("XDG_CONFIG_HOME", tc.env)
		if got := GlobalPath(); got != tc.want {
			t.Errorf("%s: GlobalPath = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWriteDefaultBoardKeepsWhatTheLayerHas(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Project, `{"maxConcurrent":3,"_extra":1}`)
	if err := WriteDefaultBoard(dir, Project); err != nil {
		t.Fatal(err)
	}
	got := readLayer(dir, Project)
	if !strings.Contains(got, `"_extra": 1`) || strings.Index(got, "maxConcurrent") > strings.Index(got, `"columns"`) || !strings.Contains(got, `"archiveDoneAfter": "7d"`) {
		t.Errorf("keys lost or reordered:\n%s", got)
	}

	custom := `{"columns":[{"id":"inbox","name":"Inbox"}]}`
	writeLayer(t, dir, Project, custom)
	if err := WriteDefaultBoard(dir, Project); err != nil || strings.Contains(readLayer(dir, Project), "prFlow") || strings.Contains(readLayer(dir, Project), "archiveDoneAfter") {
		t.Errorf("a layer with columns got the default flows: err=%v\n%s", err, readLayer(dir, Project))
	}
}

func TestWriteDefaultBoardRefusesBelowTheBoard(t *testing.T) {
	dir := testRoot(t)
	writeLayer(t, dir, Global, sample)
	if err := WriteDefaultBoard(dir, Project); err == nil || !strings.Contains(err.Error(), "global") {
		t.Fatalf("err=%v, want a pointer to the global config", err)
	}
	if _, err := os.Stat(Project.Path(dir)); !os.IsNotExist(err) {
		t.Errorf("a refused write created the project config: %v", err)
	}
}
