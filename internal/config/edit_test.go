package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetKeepsTheRestOfTheSharedFileInOrder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)

	if err := Set(dir, Shared, []string{"archiveDoneAfter"}, "30d"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
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

func TestSetNestedKeyCreatesTheLocalFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)

	if err := Set(dir, Local, []string{"notifications", "turnDone"}, false); err != nil {
		t.Fatal(err)
	}
	board, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if board.Notifications.TurnDone || !board.Notifications.NeedsYou {
		t.Errorf("notifications=%+v", board.Notifications)
	}
	if !Sets(dir, Local, []string{"notifications", "turnDone"}) || Sets(dir, Local, []string{"notifications", "needsYou"}) {
		t.Errorf("Sets does not see the nested key")
	}
}

func TestUnsetFallsBackToTheDefault(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	if err := Set(dir, Local, []string{"autoName"}, false); err != nil {
		t.Fatal(err)
	}
	if err := Unset(dir, Local, []string{"autoName"}); err != nil {
		t.Fatal(err)
	}
	if Sets(dir, Local, []string{"autoName"}) {
		t.Fatal("override is still there")
	}
	board, _, _ := Load(dir)
	if !board.AutoName {
		t.Error("autoName did not fall back to true")
	}
}

func TestUnsetLastNestedKeyRemovesTheParent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	Set(dir, Local, []string{"notifications", "exited"}, false)
	if err := Unset(dir, Local, []string{"notifications", "exited"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, LocalFileName))
	if strings.Contains(string(data), "notifications") {
		t.Errorf("empty parent left behind:\n%s", data)
	}
}

func TestUnsetNestedKeyKeepsSiblings(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	Set(dir, Local, []string{"notifications", "turnDone"}, false)
	Set(dir, Local, []string{"notifications", "exited"}, false)
	if err := Unset(dir, Local, []string{"notifications", "exited"}); err != nil {
		t.Fatal(err)
	}
	if !Sets(dir, Local, []string{"notifications", "turnDone"}) {
		t.Error("unsetting one nested key removed its sibling")
	}
}

func TestUnsetWithoutLocalFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	if err := Unset(dir, Local, []string{"autoName"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, LocalFileName)); !os.IsNotExist(err) {
		t.Errorf("unset created drover.local.json: %v", err)
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
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, LocalFileName), []byte(tc.content), 0o644)
			if Sets(dir, Local, tc.path) {
				t.Errorf("Sets is true for %q", tc.content)
			}
		})
	}
}

func TestSetWritesAmpersandsReadably(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	if err := Set(dir, Shared, []string{"initScript"}, "make && make test"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(data), `"make && make test"`) {
		t.Errorf("value was HTML-escaped:\n%s", data)
	}
}

func TestSetKeepsTheFileMode(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	file := filepath.Join(dir, LocalFileName)
	os.WriteFile(file, []byte(`{}`), 0o600)
	if err := Set(dir, Local, []string{"autoName"}, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode=%v err=%v, want 0600", info.Mode().Perm(), err)
	}
}

func TestEditRejectsAnUnknownLayerAndAnEmptyPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	if err := Set(dir, Layer(7), []string{"autoName"}, false); err == nil {
		t.Error("expected an error for an unknown layer")
	}
	if err := Set(dir, Local, nil, false); err == nil {
		t.Error("expected an error for an empty path")
	}
	if Sets(dir, Local, nil) {
		t.Error("an empty path can't be set")
	}
	if _, err := os.Stat(filepath.Join(dir, LocalFileName)); !os.IsNotExist(err) {
		t.Errorf("a rejected edit created drover.local.json: %v", err)
	}
}

func TestSharedEditNeedsADrover(t *testing.T) {
	dir := t.TempDir()
	if err := Set(dir, Shared, []string{"archiveDoneAfter"}, "30d"); !errors.Is(err, ErrNoSharedFile) {
		t.Fatalf("err=%v, want ErrNoSharedFile", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Errorf("drover.json was created: %v", err)
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
		{"bad shared value", Shared, sample, []string{"archiveDoneAfter"}, "soon"},
		{"bad local value leaves no new file", Local, "", []string{"maxConcurrent"}, "many"},
		{"trailing data", Local, `{"autoName":false} junk`, []string{"maxConcurrent"}, 4},
		{"truncated file", Local, `{"autoName":false`, []string{"maxConcurrent"}, 4},
		{"scalar parent", Local, `{"notifications": 5}`, []string{"notifications", "turnDone"}, false},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
		file := filepath.Join(dir, tc.layer.FileName())
		if tc.before != "" {
			os.WriteFile(file, []byte(tc.before), 0o644)
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

func TestSetColumnSettingInTheSharedFileKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)

	if err := Set(dir, Shared, ColumnPath("dc9f2b38", "effortOverride"), "high"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
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
	if !Sets(dir, Shared, ColumnPath("dc9f2b38", "effortOverride")) || Sets(dir, Shared, ColumnPath("dc9f2b38", "color")) {
		t.Error("Sets does not see the column key")
	}
	if err := Unset(dir, Shared, ColumnPath("dc9f2b38", "effortOverride")); err != nil {
		t.Fatal(err)
	}
	if Sets(dir, Shared, ColumnPath("dc9f2b38", "effortOverride")) {
		t.Error("effortOverride is still set")
	}
}

func TestSetColumnSettingWritesNamesReadably(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)

	if err := Set(dir, Shared, ColumnPath("dc9f2b38", "name"), `Build & "ship" <it>`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(data), `"Build & \"ship\" <it>"`) || !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("name was escaped or the file reformatted oddly:\n%s", data)
	}
	if board, _, err := Load(dir); err != nil {
		t.Fatal(err)
	} else if c, _, _ := board.Column("dc9f2b38"); c.Name != `Build & "ship" <it>` {
		t.Errorf("name = %q", c.Name)
	}
}

func TestSetColumnSettingLocallyCreatesAndRemovesTheOverride(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(`{"maxConcurrent":3}`), 0o644)

	if err := Set(dir, Local, ColumnPath("dc9f2b38", "modelOverride"), "haiku"); err != nil {
		t.Fatal(err)
	}
	if err := Set(dir, Local, ColumnPath("4873bb89", "effortOverride"), "low"); err != nil {
		t.Fatal(err)
	}
	board, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if board.Limit() != 3 {
		t.Errorf("lost the other local keys, limit = %d", board.Limit())
	}
	if c, _, _ := board.Column("dc9f2b38"); c.ModelOverride != "haiku" {
		t.Errorf("executing model = %q", c.ModelOverride)
	}
	if c, _, _ := board.Column("4873bb89"); c.EffortOverride != "low" || c.ModelOverride != "opus" {
		t.Errorf("planning = %+v", c)
	}
	if err := Unset(dir, Local, ColumnPath("dc9f2b38", "modelOverride")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, LocalFileName))
	if strings.Contains(string(data), "dc9f2b38") || !strings.Contains(string(data), "4873bb89") {
		t.Errorf("unsetting the last key should drop only that element:\n%s", data)
	}
	if err := Unset(dir, Local, ColumnPath("4873bb89", "effortOverride")); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, LocalFileName))
	if strings.Contains(string(data), "columns") || !strings.Contains(string(data), "maxConcurrent") {
		t.Errorf("an empty columns array should go and the rest stay:\n%s", data)
	}
	if err := Unset(dir, Local, ColumnPath("no-such", "color")); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, LocalFileName)); string(after) != string(data) {
		t.Errorf("unsetting a missing override changed the file:\n%s", after)
	}
}

func TestSetColumnSettingInTheSharedFileNeverAddsOrDropsAColumn(t *testing.T) {
	cases := []struct {
		name   string
		shared string
		run    func(dir string) error
	}{
		{"unknown column", sample, func(dir string) error { return Set(dir, Shared, ColumnPath("no-such", "color"), "#fff") }},
		{"no columns key", `{"autoName":false}`, func(dir string) error { return Set(dir, Shared, ColumnPath("x", "name"), "X") }},
		{"unsetting the name", sample, func(dir string) error { return Unset(dir, Shared, ColumnPath("64f9ef9a", "name")) }},
		{"keepAgent on an agent column", sample, func(dir string) error { return Set(dir, Shared, ColumnPath("dc9f2b38", "keepAgent"), true) }},
		{"a whole column", sample, func(dir string) error { return Set(dir, Shared, ColumnPath("dc9f2b38"), "x") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, FileName), []byte(tc.shared), 0o644)
			if err := tc.run(dir); err == nil {
				t.Error("expected an error")
			}
			if data, _ := os.ReadFile(filepath.Join(dir, FileName)); string(data) != tc.shared {
				t.Errorf("file changed after a rejected edit:\n%s", data)
			}
		})
	}
}

func TestColumnPathsOnlyAddressTheColumnsArray(t *testing.T) {
	dir := t.TempDir()
	const local = `{"flows":[{"id":"a","x":1}]}`
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(local), 0o644)

	if Sets(dir, Local, []string{"flows", "a", "x"}) {
		t.Error("Sets looked inside an array other than columns")
	}
	if err := Set(dir, Local, []string{"flows", "a", "x"}, 2); err == nil {
		t.Error("expected an error addressing another array by id")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, LocalFileName)); string(data) != local {
		t.Errorf("file changed:\n%s", data)
	}
}

func TestSetColumnSettingRejectsMalformedColumns(t *testing.T) {
	dir := t.TempDir()
	const local = `{"columns":[{"id":5}]}`
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(local), 0o644)

	if err := Set(dir, Local, ColumnPath("dc9f2b38", "color"), "#fff"); err == nil {
		t.Error("expected an error for a column whose id isn't a string")
	}
	if Sets(dir, Local, ColumnPath("dc9f2b38", "color")) {
		t.Error("Sets matched a malformed element")
	}
}
