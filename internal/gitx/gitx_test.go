package gitx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func TestFetchReleaseAndContains(t *testing.T) {
	tmp := t.TempDir()
	root, origin := filepath.Join(tmp, "repo"), filepath.Join(tmp, "origin.git")
	git(t, tmp, "init", "-q", "-b", "main", root)
	git(t, tmp, "init", "-q", "--bare", origin)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, root, "remote", "add", "origin", origin)

	if _, err := FetchRelease(root, ""); err == nil {
		t.Fatal("fetched a release branch from an empty origin")
	}
	git(t, root, "push", "-q", "origin", "main")
	ref, err := FetchRelease(root, "")
	if err != nil || ref != "refs/remotes/origin/main" {
		t.Fatalf("FetchRelease = %q, %v; want origin/main as the fallback", ref, err)
	}

	head := git(t, root, "rev-parse", "HEAD")
	feature := git(t, root, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "feature")
	for _, c := range []struct {
		commit string
		want   bool
	}{{head, true}, {feature, false}, {"0123456789abcdef0123456789abcdef01234567", false}} {
		if got, err := Contains(root, ref, c.commit); got != c.want || err != nil {
			t.Errorf("Contains(%s) = %v, %v; want %v", c.commit[:7], got, err, c.want)
		}
	}
	if _, err := Contains(root, "refs/remotes/origin/nope", head); err == nil {
		t.Error("a missing release ref is not an error")
	}
}

func TestMainRootFromLinkedWorktree(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(tmp, "repo")
	git(t, tmp, "init", "-q", "-b", "main", root)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(root, ".claude", "worktrees", "x")
	git(t, root, "worktree", "add", "-q", "-b", "x", wt)

	for _, dir := range []string{root, wt} {
		if got, err := MainRoot(dir); err != nil || got != root {
			t.Fatalf("MainRoot(%s) = %s, %v; want %s", dir, got, err, root)
		}
	}
}

func TestCreateWorktreeInCustomDir(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(tmp, "repo")
	git(t, tmp, "init", "-q", "-b", "main", root)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "init")

	worktrees := filepath.Join(root, ".claude", "worktrees")
	path, err := CreateWorktree(root, worktrees, "1", "feature-1", "main")
	if err != nil || path != filepath.Join(worktrees, "1") {
		t.Fatalf("CreateWorktree = %q, %v", path, err)
	}
	if got := git(t, path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature-1" {
		t.Errorf("worktree branch = %q", got)
	}
	if err := RemoveWorktree(root, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree still exists: %v", err)
	}
}

func TestEnsureExcluded(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(tmp, "repo")
	git(t, tmp, "init", "-q", "-b", "main", root)

	for range 2 {
		if err := EnsureExcluded(root, filepath.Join(root, ".claude", "worktrees")); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureExcluded(root, filepath.Join(tmp, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	for _, pattern := range []string{"/.drover/\n", "/.claude/worktrees/\n"} {
		if n := strings.Count(string(data), pattern); n != 1 {
			t.Errorf("%q appears %d times in info/exclude:\n%s", pattern, n, data)
		}
	}
	if strings.Contains(string(data), "elsewhere") {
		t.Errorf("a directory outside the repo was excluded:\n%s", data)
	}
}

func TestCopyFilesAndDirtyIgnoresThem(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(tmp, "repo")
	git(t, tmp, "init", "-q", "-b", "main", root)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	os.WriteFile(filepath.Join(root, ".env.shared"), []byte("SECRET=1"), 0o600)
	os.Symlink(".env.shared", filepath.Join(root, ".env"))
	os.MkdirAll(filepath.Join(root, "config", "local"), 0o755)
	os.WriteFile(filepath.Join(root, "config", "tracked.json"), []byte("{}"), 0o644)
	git(t, root, "add", "config/tracked.json")
	git(t, root, "commit", "-q", "-m", "tracked")
	os.WriteFile(filepath.Join(root, "config", "local", "dev.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(tmp, "outside"), []byte("x"), 0o644)
	wt, err := CreateWorktree(root, filepath.Join(root, ".drover", "worktrees"), "1", "b", "main")
	if err != nil {
		t.Fatal(err)
	}

	paths := []string{".env", "config", ".claude/settings.local.json", "missing", "../outside"}
	if err := CopyFiles(root, wt, paths); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, ".env")); string(b) != "SECRET=1" {
		t.Errorf(".env = %q", b)
	}
	if info, err := os.Stat(filepath.Join(wt, ".env")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "config", "local", "dev.json")); err != nil {
		t.Errorf("directory not copied: %v", err)
	}
	for _, skipped := range []string{".claude/settings.local.json", "../outside"} {
		if _, err := os.Stat(filepath.Join(wt, skipped)); err == nil {
			t.Errorf("%s was copied", skipped)
		}
	}

	if Dirty(wt, paths...) {
		t.Error("copied files count as uncommitted changes")
	}
	os.WriteFile(filepath.Join(wt, "config", "tracked.json"), []byte("{\"edited\":1}"), 0o644)
	if !Dirty(wt, paths...) {
		t.Error("an edit to a tracked file under a copied path is not dirty")
	}
	git(t, wt, "checkout", "--", "config/tracked.json")
	os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("x"), 0o644)
	if !Dirty(wt, paths...) {
		t.Error("a new file outside the copied paths is not dirty")
	}
}

func TestFastForward(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(tmp, "repo")
	git(t, tmp, "init", "-q", "-b", "main", root)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	wt, err := CreateWorktree(root, filepath.Join(root, ".drover", "worktrees"), "1", "feature", "main")
	if err != nil {
		t.Fatal(err)
	}

	if n, err := FastForward(wt, "main"); n != 0 || err != nil {
		t.Fatalf("up to date: %d, %v", n, err)
	}
	git(t, root, "commit", "-q", "--allow-empty", "-m", "two")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "three")
	if n, err := FastForward(wt, "main"); n != 2 || err != nil {
		t.Fatalf("behind by two: %d, %v", n, err)
	}
	if got, want := git(t, wt, "rev-parse", "HEAD"), git(t, root, "rev-parse", "main"); got != want {
		t.Errorf("HEAD = %s, want main's %s", got, want)
	}
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "mine")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "four")
	if _, err := FastForward(wt, "main"); !errors.Is(err, ErrDiverged) {
		t.Errorf("diverged: %v", err)
	}
}
