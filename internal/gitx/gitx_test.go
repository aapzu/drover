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

func linkRepo(t *testing.T) (root, wt string) {
	t.Helper()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root = filepath.Join(tmp, "repo")
	git(t, tmp, "init", "-q", "-b", "main", root)
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write(".gitignore", "dist/\n-cache/\n.env\nnode_modules/\n")
	write("config/tracked.json", "{}")
	write("q", "1")
	git(t, root, "add", ".")
	git(t, root, "commit", "-q", "-m", "init")
	write(".env", "SECRET=1")
	write("config/local/dev.json", "{}")
	write("notes.txt", "n")
	write("dist/app.js", "x")
	write("dist/cache/big.bin", "x")
	write("node_modules/pkg/index.js", "x")
	write("tools/node_modules/pkg/index.js", "x")
	write("tools/run.sh", "x")
	write("trees/1/file", "x")
	write("-cache/a", "x")
	write(".kangentic/config.json", "x")
	write(".kangentic/worktrees/3/f.txt", "x")
	write("vendor-repo/.env", "x")
	write("vendor-repo/.git", "gitdir: elsewhere")
	write(".kangentic/worktrees/3/.git", "gitdir: elsewhere")
	write("my file.txt", "x")
	write("a b/c d.txt", "x")
	write(".claude/settings.local.json", "{}")
	write(".drover/state.json", "{}")
	write("../outside", "x")
	wt, err := CreateWorktree(root, filepath.Join(root, ".drover", "worktrees"), "1", "b", "main")
	if err != nil {
		t.Fatal(err)
	}
	return root, wt
}

func linked(t *testing.T, root, wt, rel string) bool {
	t.Helper()
	target, err := os.Readlink(filepath.Join(wt, rel))
	if err != nil {
		return false
	}
	if target != filepath.Join(root, rel) {
		t.Errorf("%s links to %s", rel, target)
	}
	return true
}

func TestLinkFilesEverythingExceptSkipped(t *testing.T) {
	root, wt := linkRepo(t)
	if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".env", "notes.txt", "dist/app.js", "dist/cache", "-cache/a", ".kangentic/config.json", "config/local", "tools/run.sh"} {
		if !linked(t, root, wt, rel) {
			t.Errorf("%s was not linked", rel)
		}
	}
	for _, rel := range []string{"dist", "-cache"} {
		if linked(t, root, wt, rel) {
			t.Errorf("gitignored %s was linked whole, so its ignore rule no longer matches it", rel)
		}
	}
	if out := git(t, wt, "status", "--porcelain", "--untracked-files=all"); strings.Contains(out, "dist") {
		t.Errorf("git status in the worktree lists the links under an ignored directory:\n%s", out)
	}
	for _, rel := range []string{"node_modules", "tools/node_modules", ".claude", ".drover", ".kangentic/worktrees", "vendor-repo", "config/tracked.json", "../outside"} {
		if fi, err := os.Lstat(filepath.Join(wt, rel)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s was linked", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(wt, "config", "tracked.json")); err != nil {
		t.Errorf("tracked file gone: %v", err)
	}
}

func TestLinkFilesExclude(t *testing.T) {
	root, wt := linkRepo(t)
	if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), nil, []string{".env", "dist/cache", "missing"}); err != nil {
		t.Fatal(err)
	}
	if linked(t, root, wt, ".env") {
		t.Error("excluded .env was linked")
	}
	if linked(t, root, wt, "dist") {
		t.Error("dist was linked whole despite an excluded path inside")
	}
	if !linked(t, root, wt, "dist/app.js") {
		t.Error("dist/app.js was not linked")
	}
	if _, err := os.Lstat(filepath.Join(wt, "dist", "cache")); err == nil {
		t.Error("excluded dist/cache exists in the worktree")
	}
	if !linked(t, root, wt, "notes.txt") {
		t.Error("notes.txt was not linked")
	}
}

func TestLinkFilesWhitelistWinsOverExclude(t *testing.T) {
	root, wt := linkRepo(t)
	only := []string{".env", "config", "tools/run.sh", "node_modules", "config/tracked.json", ".claude/settings.local.json", "missing", "../outside"}
	if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), only, []string{".env", "config/local"}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".env", "config/local", "tools/run.sh"} {
		if !linked(t, root, wt, rel) {
			t.Errorf("%s was not linked", rel)
		}
	}
	for _, rel := range []string{"notes.txt", "dist", "node_modules", ".claude"} {
		if _, err := os.Lstat(filepath.Join(wt, rel)); err == nil {
			t.Errorf("%s exists in the worktree", rel)
		}
	}
	if fi, err := os.Lstat(filepath.Join(wt, "config", "tracked.json")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("tracked.json = %v, %v", fi, err)
	}
}

func TestDirtyKeepsAOneLetterFirstEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	git(t, filepath.Dir(root), "init", "-q", "-b", "main", root)
	os.WriteFile(filepath.Join(root, "q"), []byte("1"), 0o644)
	git(t, root, "add", "q")
	git(t, root, "commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(root, "q"), []byte("2"), 0o644)
	if !Dirty(root, root) {
		t.Error("an edit to a tracked file named q is not dirty")
	}
}

func TestLinkFilesWhitelist(t *testing.T) {
	worktrees := func(root string) string { return filepath.Join(root, ".drover", "worktrees") }
	exists := func(wt, rel string) bool {
		_, err := os.Lstat(filepath.Join(wt, rel))
		return err == nil
	}

	t.Run("an unusable whitelist links nothing", func(t *testing.T) {
		root, wt := linkRepo(t)
		if err := LinkFiles(root, wt, worktrees(root), []string{"../outside", "missing"}, nil); err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{".env", "notes.txt", "dist"} {
			if exists(wt, rel) {
				t.Errorf("%s was linked", rel)
			}
		}
	})

	t.Run("an ignored directory is a real directory of links", func(t *testing.T) {
		root, wt := linkRepo(t)
		if err := LinkFiles(root, wt, worktrees(root), []string{"dist"}, nil); err != nil {
			t.Fatal(err)
		}
		if linked(t, root, wt, "dist") || !linked(t, root, wt, "dist/app.js") || !linked(t, root, wt, "dist/cache") {
			t.Error("dist was not recreated as a directory of links")
		}
	})

	t.Run("a path inside an ignored directory keeps its siblings out", func(t *testing.T) {
		root, wt := linkRepo(t)
		if err := LinkFiles(root, wt, worktrees(root), []string{"dist/cache"}, nil); err != nil {
			t.Fatal(err)
		}
		if linked(t, root, wt, "dist") || exists(wt, "dist/app.js") || !linked(t, root, wt, "dist/cache/big.bin") {
			t.Error("only dist/cache/big.bin should be linked")
		}
	})

	t.Run("a nested repo stays out even by name", func(t *testing.T) {
		root, wt := linkRepo(t)
		if err := LinkFiles(root, wt, worktrees(root), []string{"vendor-repo/.env", ".kangentic/worktrees/3/f.txt"}, nil); err != nil {
			t.Fatal(err)
		}
		if exists(wt, "vendor-repo") || exists(wt, ".kangentic") {
			t.Error("a path inside a nested repo was linked")
		}
	})
}

func TestRemovingALinkedWorktreeKeepsTheSources(t *testing.T) {
	root, wt := linkRepo(t)
	if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(root, wt); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".env", "dist/app.js", "config/local/dev.json", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s was removed with the worktree: %v", rel, err)
		}
	}
}

func TestDirtyIgnoresAnOldNodeModulesLink(t *testing.T) {
	root, wt := linkRepo(t)
	os.Symlink(filepath.Join(root, "node_modules"), filepath.Join(wt, "node_modules"))
	if Dirty(wt, root) {
		t.Error("the node_modules link older drover versions made counts as a change")
	}
}

func TestLinkFilesSkipsWorktreesDirAndTakenPaths(t *testing.T) {
	root, wt := linkRepo(t)
	os.WriteFile(filepath.Join(wt, "dist"), []byte("tracked file"), 0o644)
	if err := LinkFiles(root, wt, filepath.Join(root, "trees"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(wt, "trees")); err == nil {
		t.Error("the worktrees directory was linked")
	}
	if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), []string{"dist/app.js"}, nil); err != nil {
		t.Errorf("a path under a file in the worktree is an error: %v", err)
	}
}

func TestDirtyHandlesSpacesAndForeignLinks(t *testing.T) {
	root, wt := linkRepo(t)
	if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"my file.txt", "a b"} {
		if !linked(t, root, wt, rel) {
			t.Errorf("%s was not linked", rel)
		}
	}
	if Dirty(wt, root) {
		t.Error("linked paths with spaces count as uncommitted changes")
	}
	os.Symlink(filepath.Join(filepath.Dir(root), "outside"), filepath.Join(wt, "elsewhere"))
	if !Dirty(wt, root) {
		t.Error("a symlink out of the checkout is not dirty")
	}
	os.Remove(filepath.Join(wt, "elsewhere"))
	os.Symlink("notes.txt", filepath.Join(wt, "relative"))
	if !Dirty(wt, root) {
		t.Error("a relative symlink is not dirty")
	}
}

func TestLinkFilesKeepsExistingAndDirtyIgnoresLinks(t *testing.T) {
	root, wt := linkRepo(t)
	os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("mine"), 0o644)
	if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "notes.txt")); string(b) != "mine" {
		t.Errorf("notes.txt = %q, want it kept", b)
	}
	os.Remove(filepath.Join(wt, "notes.txt"))
	if Dirty(wt, root) {
		t.Error("links count as uncommitted changes")
	}
	os.WriteFile(filepath.Join(wt, "config", "tracked.json"), []byte("{\"edited\":1}"), 0o644)
	if !Dirty(wt, root) {
		t.Error("an edit to a tracked file next to a link is not dirty")
	}
	git(t, wt, "checkout", "--", "config/tracked.json")
	os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x"), 0o644)
	if !Dirty(wt, root) {
		t.Error("a new real file is not dirty")
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
