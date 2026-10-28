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
