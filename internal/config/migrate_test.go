package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateCopiesTheSharedFileAndReplaysTheLocalOne(t *testing.T) {
	root := testRoot(t)
	writeRepoFile(t, root, legacySharedFile, `{
  "worktreesDir": ".claude/worktrees",
  "notifications": {"turnDone": false},
  "columns": [{"id": "a", "name": "A", "modelOverride": "haiku"}, {"id": "b", "name": "B"}],
  "_extra": 1
}`)
	writeRepoFile(t, root, legacyLocalFile, `{
  "maxConcurrent": 16,
  "notifications": {"exited": false},
  "columns": [{"id": "a", "color": "#222", "modelOverride": "opus"}, {"id": "gone", "color": "#333"}]
}`)

	dest, err := MigrateRepoFiles(root)
	if err != nil || dest != ProjectPath(root) {
		t.Fatalf("dest=%q err=%v", dest, err)
	}
	board, found, err := Load(root)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	a, _, _ := board.Column("a")
	want := DefaultNotifications()
	want.TurnDone, want.Exited = false, false
	if board.Limit() != 16 || board.Notifications != want || board.WorktreesDir != ".claude/worktrees" ||
		a.Color != "#222" || a.ModelOverride != "opus" || len(board.Columns) != 2 {
		t.Errorf("limit=%d notifications=%+v dir=%q a=%+v columns=%d", board.Limit(), board.Notifications, board.WorktreesDir, a, len(board.Columns))
	}
	if !strings.Contains(readLayer(root, Project), `"_extra": 1`) {
		t.Errorf("unknown key lost:\n%s", readLayer(root, Project))
	}
	if _, err := os.Stat(GlobalPath()); !os.IsNotExist(err) {
		t.Errorf("migration wrote the global config: %v", err)
	}
	for _, name := range []string{legacySharedFile, legacyLocalFile} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s was removed from the repo: %v", name, err)
		}
	}
}

func TestMigrateSharedFileOnly(t *testing.T) {
	root := testRoot(t)
	writeRepoFile(t, root, legacySharedFile, sample)
	if dest, err := MigrateRepoFiles(root); err != nil || dest == "" {
		t.Fatalf("dest=%q err=%v", dest, err)
	}
	if got := readLayer(root, Project); !strings.Contains(got, `"Apply findings"`) {
		t.Errorf("drover.json not copied:\n%s", got)
	}
}

func TestMigrateLocalFileOnlyOverridesTheDefaultBoard(t *testing.T) {
	root := testRoot(t)
	writeRepoFile(t, root, legacyLocalFile, `{"disableMouse":true,"columns":[{"id":"executing","modelOverride":"opus"}]}`)
	if _, err := MigrateRepoFiles(root); err != nil {
		t.Fatal(err)
	}
	board, found, err := Load(root)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if c, _, _ := board.Column("executing"); c.ModelOverride != "opus" || !board.DisableMouse || len(board.Columns) != len(Default().Columns) {
		t.Errorf("executing=%+v mouse=%v columns=%d", c, board.DisableMouse, len(board.Columns))
	}
}

func TestMigrateIsANoOpWhenThereIsNothingToMoveOrAProjectFileExists(t *testing.T) {
	root := testRoot(t)
	if dest, err := MigrateRepoFiles(root); err != nil || dest != "" {
		t.Fatalf("without repo files: dest=%q err=%v", dest, err)
	}
	if _, err := os.Stat(ProjectPath(root)); !os.IsNotExist(err) {
		t.Errorf("created an empty project config: %v", err)
	}

	writeRepoFile(t, root, legacySharedFile, sample)
	writeLayer(t, root, Project, `{"maxConcurrent":3}`)
	if dest, err := MigrateRepoFiles(root); err != nil || dest != "" {
		t.Fatalf("with a project file: dest=%q err=%v", dest, err)
	}
	if got := readLayer(root, Project); got != `{"maxConcurrent":3}` {
		t.Errorf("project file overwritten:\n%s", got)
	}
}

func TestLoadDoesNotReadTheRepoFiles(t *testing.T) {
	root := testRoot(t)
	writeRepoFile(t, root, legacySharedFile, sample)
	writeRepoFile(t, root, legacyLocalFile, `{"maxConcurrent":5}`)
	board, found, err := Load(root)
	if err != nil || found || board.Limit() != DefaultMaxConcurrent {
		t.Fatalf("found=%v limit=%d err=%v", found, board.Limit(), err)
	}
	if _, err := os.Stat(ProjectPath(root)); !os.IsNotExist(err) {
		t.Errorf("Load wrote the project config: %v", err)
	}
}

func TestMigrateRefusesLocalSettingsOfTheWrongShape(t *testing.T) {
	for _, local := range []string{
		`{"columns":{}}`,
		`{"columns":[{"color":"#fff"}]}`,
		`{"notifications":5}`,
	} {
		root := testRoot(t)
		writeRepoFile(t, root, legacySharedFile, `{"columns":[{"id":"a","name":"A"}]}`)
		writeRepoFile(t, root, legacyLocalFile, local)
		if _, err := MigrateRepoFiles(root); err == nil {
			t.Errorf("%s: expected an error", local)
		}
		if _, err := os.Stat(ProjectPath(root)); !os.IsNotExist(err) {
			t.Errorf("%s: wrote a project config anyway: %v", local, err)
		}
	}
}

func TestMigrateDoesNotValidateTheResult(t *testing.T) {
	root := testRoot(t)
	writeRepoFile(t, root, legacySharedFile, `{"columns":[{"id":"a","name":"A"}]}`)
	writeRepoFile(t, root, legacyLocalFile, `{"maxConcurrent":"lots"}`)
	if dest, err := MigrateRepoFiles(root); err != nil || dest == "" {
		t.Fatalf("dest=%q err=%v", dest, err)
	}
	if _, _, err := Load(root); err == nil || !strings.Contains(err.Error(), ProjectPath(root)) {
		t.Errorf("err=%v, want Load to name the project config", err)
	}
}

func TestMigrateRefusesBrokenRepoFiles(t *testing.T) {
	root := testRoot(t)
	writeRepoFile(t, root, legacySharedFile, `{"columns":`)
	if _, err := MigrateRepoFiles(root); err == nil {
		t.Error("expected an error for a broken drover.json")
	}
	if _, err := os.Stat(ProjectPath(root)); !os.IsNotExist(err) {
		t.Errorf("wrote a project config from a broken file: %v", err)
	}
}

func TestMigrateIgnoresEmptyLocalValues(t *testing.T) {
	root := testRoot(t)
	writeRepoFile(t, root, legacySharedFile, `{"worktreesDir":"a","columns":[{"id":"a","name":"A","modelOverride":"haiku"}]}`)
	writeRepoFile(t, root, legacyLocalFile, `{"worktreesDir":"","columns":[{"id":"a","modelOverride":"","color":""}]}`)
	if _, err := MigrateRepoFiles(root); err != nil {
		t.Fatal(err)
	}
	board, _, err := Load(root)
	if a, _, _ := board.Column("a"); err != nil || a.ModelOverride != "haiku" || a.Color != "" || board.WorktreesDir != "a" {
		t.Errorf("a=%+v dir=%q err=%v", a, board.WorktreesDir, err)
	}
}
