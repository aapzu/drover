package config

import (
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

func TestInvalidValueIsRejectedAndFileRestored(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	before, _ := os.ReadFile(filepath.Join(dir, FileName))

	if err := Set(dir, Shared, []string{"archiveDoneAfter"}, "soon"); err == nil {
		t.Fatal("expected an error for a bad duration")
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if string(before) != string(after) {
		t.Errorf("file changed after a rejected edit:\n%s", after)
	}
}

func TestInvalidLocalValueRemovesTheNewFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(sample), 0o644)
	if err := Set(dir, Local, []string{"maxConcurrent"}, "many"); err == nil {
		t.Fatal("expected an error for a string in a number field")
	}
	if _, err := os.Stat(filepath.Join(dir, LocalFileName)); !os.IsNotExist(err) {
		t.Errorf("rejected local file was left behind: %v", err)
	}
}

func TestNestedSetUnderAScalarIsRejected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(`{"notifications": 5}`), 0o644)
	if err := Set(dir, Local, []string{"notifications", "turnDone"}, false); err == nil {
		t.Fatal("expected an error when the parent is a scalar")
	}
	data, _ := os.ReadFile(filepath.Join(dir, LocalFileName))
	if string(data) != `{"notifications": 5}` {
		t.Errorf("scalar parent was replaced:\n%s", data)
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

func TestTruncatedFileIsRejectedAndUntouched(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, LocalFileName), []byte(`{"autoName":false`), 0o644)
	if err := Set(dir, Local, []string{"maxConcurrent"}, 4); err == nil {
		t.Fatal("expected an error for a truncated file")
	}
	data, _ := os.ReadFile(filepath.Join(dir, LocalFileName))
	if string(data) != `{"autoName":false` {
		t.Errorf("truncated file was rewritten:\n%s", data)
	}
}

func TestSharedEditNeedsADrover(t *testing.T) {
	dir := t.TempDir()
	if err := Set(dir, Shared, []string{"archiveDoneAfter"}, "30d"); err == nil {
		t.Fatal("expected an error without drover.json")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Errorf("drover.json was created: %v", err)
	}
}
