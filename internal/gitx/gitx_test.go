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

func TestLinkFilesExcludeAndWhitelist(t *testing.T) {
	for _, tc := range []struct {
		name         string
		only, except []string
		linked       []string
		notLinked    []string
		absent       []string
		regular      []string
	}{
		{
			name:      "exclude skips the listed paths and splits their ignored directory",
			except:    []string{".env", "dist/cache", "missing"},
			linked:    []string{"dist/app.js", "notes.txt"},
			notLinked: []string{".env", "dist"},
			absent:    []string{"dist/cache"},
		},
		{
			name:    "whitelist wins over exclude",
			only:    []string{".env", "config", "tools/run.sh", "node_modules", "config/tracked.json", ".claude/settings.local.json", "missing", "../outside"},
			except:  []string{".env", "config/local"},
			linked:  []string{".env", "config/local", "tools/run.sh"},
			absent:  []string{"notes.txt", "dist", "node_modules", ".claude"},
			regular: []string{"config/tracked.json"},
		},
		{
			name:   "an unusable whitelist links nothing",
			only:   []string{"../outside", "missing"},
			absent: []string{".env", "notes.txt", "dist"},
		},
		{
			name:      "an ignored directory is a real directory of links",
			only:      []string{"dist"},
			linked:    []string{"dist/app.js", "dist/cache"},
			notLinked: []string{"dist"},
		},
		{
			name:      "a path inside an ignored directory keeps its siblings out",
			only:      []string{"dist/cache"},
			linked:    []string{"dist/cache/big.bin"},
			notLinked: []string{"dist"},
			absent:    []string{"dist/app.js"},
		},
		{
			name:   "a nested repo stays out even by name",
			only:   []string{"vendor-repo/.env", ".kangentic/worktrees/3/f.txt"},
			absent: []string{"vendor-repo", ".kangentic"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, wt := linkRepo(t)
			if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), tc.only, tc.except); err != nil {
				t.Fatal(err)
			}
			for _, rel := range tc.linked {
				if !linked(t, root, wt, rel) {
					t.Errorf("%s was not linked", rel)
				}
			}
			for _, rel := range tc.notLinked {
				if linked(t, root, wt, rel) {
					t.Errorf("%s was linked", rel)
				}
			}
			for _, rel := range tc.absent {
				if _, err := os.Lstat(filepath.Join(wt, rel)); err == nil {
					t.Errorf("%s exists in the worktree", rel)
				}
			}
			for _, rel := range tc.regular {
				if fi, err := os.Lstat(filepath.Join(wt, rel)); err != nil || fi.Mode()&os.ModeSymlink != 0 {
					t.Errorf("%s = %v, %v; want a tracked regular file", rel, fi, err)
				}
			}
		})
	}
}

func TestLinkFilesKeepsExistingFiles(t *testing.T) {
	root, wt := linkRepo(t)
	os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("mine"), 0o644)
	if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "notes.txt")); string(b) != "mine" {
		t.Errorf("notes.txt = %q, want it kept", b)
	}
}

func TestDirty(t *testing.T) {
	linkAll := func(t *testing.T, root, wt string) {
		t.Helper()
		if err := LinkFiles(root, wt, filepath.Join(root, ".drover", "worktrees"), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root, wt string)
		want  bool
	}{
		{"links alone", func(t *testing.T, root, wt string) {
			os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("mine"), 0o644)
			linkAll(t, root, wt)
			os.Remove(filepath.Join(wt, "notes.txt"))
		}, false},
		{"links of paths with spaces", func(t *testing.T, root, wt string) {
			linkAll(t, root, wt)
			for _, rel := range []string{"my file.txt", "a b"} {
				if !linked(t, root, wt, rel) {
					t.Errorf("%s was not linked", rel)
				}
			}
		}, false},
		{"an old node_modules link", func(t *testing.T, root, wt string) {
			os.Symlink(filepath.Join(root, "node_modules"), filepath.Join(wt, "node_modules"))
		}, false},
		{"a symlink out of the checkout", func(t *testing.T, root, wt string) {
			linkAll(t, root, wt)
			os.Symlink(filepath.Join(filepath.Dir(root), "outside"), filepath.Join(wt, "elsewhere"))
		}, true},
		{"a relative symlink", func(t *testing.T, root, wt string) {
			linkAll(t, root, wt)
			os.Symlink("notes.txt", filepath.Join(wt, "relative"))
		}, true},
		{"an edit to a tracked file next to a link", func(t *testing.T, root, wt string) {
			linkAll(t, root, wt)
			os.WriteFile(filepath.Join(wt, "config", "tracked.json"), []byte("{\"edited\":1}"), 0o644)
		}, true},
		{"a new real file", func(t *testing.T, root, wt string) {
			linkAll(t, root, wt)
			os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x"), 0o644)
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, wt := linkRepo(t)
			tc.setup(t, root, wt)
			if got := Dirty(wt, root); got != tc.want {
				t.Errorf("Dirty = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("an edit to a tracked file named q", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "repo")
		git(t, filepath.Dir(root), "init", "-q", "-b", "main", root)
		os.WriteFile(filepath.Join(root, "q"), []byte("1"), 0o644)
		git(t, root, "add", "q")
		git(t, root, "commit", "-q", "-m", "init")
		os.WriteFile(filepath.Join(root, "q"), []byte("2"), 0o644)
		if !Dirty(root, root) {
			t.Error("an edit to a tracked file named q is not dirty")
		}
	})

	t.Run("no path", func(t *testing.T) {
		if Dirty("", "/root") {
			t.Error("an empty path is dirty")
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

func newRepo(t *testing.T) string {
	t.Helper()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(tmp, "repo")
	git(t, tmp, "init", "-q", "-b", "main", root)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	return root
}

func TestBranchName(t *testing.T) {
	const id = "0123456789abcdef"
	for _, tc := range []struct{ title, want string }{
		{"Fix the login bug", "fix-the-login-bug-01234567"},
		{"  --Hello,   World!--  ", "hello-world-01234567"},
		{"", "task-01234567"},
		{"!!!", "task-01234567"},
		{"Ünïcode only: ñ", "n-code-only-01234567"},
		{strings.Repeat("a", 60), strings.Repeat("a", 40) + "-01234567"},
		{strings.Repeat("a", 39) + " tail", strings.Repeat("a", 39) + "-01234567"},
	} {
		if got := BranchName(tc.title, id); got != tc.want {
			t.Errorf("BranchName(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestResolveBase(t *testing.T) {
	root := newRepo(t)
	git(t, root, "branch", "develop")
	for _, tc := range []struct {
		name, taskBase, defaultBase string
		want, wantErr               string
	}{
		{"the task's own base wins", "develop", "main", "develop", ""},
		{"a missing task base is an error", "nope", "main", "", `base branch "nope" not found`},
		{"the configured default", "", "develop", "develop", ""},
		{"a missing default falls back to main", "", "nope", "main", ""},
		{"no default falls back to main", "", "", "main", ""},
	} {
		got, err := ResolveBase(root, tc.taskBase, tc.defaultBase)
		if got != tc.want || (err == nil) != (tc.wantErr == "") || (err != nil && err.Error() != tc.wantErr) {
			t.Errorf("%s: ResolveBase = %q, %v; want %q, %q", tc.name, got, err, tc.want, tc.wantErr)
		}
	}

	git(t, root, "branch", "-m", "main", "master")
	if got, err := ResolveBase(root, "", "nope"); got != "master" || err != nil {
		t.Errorf("master fallback: %q, %v", got, err)
	}
	git(t, root, "branch", "-m", "master", "trunk")
	if _, err := ResolveBase(root, "", "nope"); err == nil {
		t.Error("no main, master or default is not an error")
	}
}

func TestToplevelCurrentBranchAndHasCommits(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(tmp, "repo")
	git(t, tmp, "init", "-q", "-b", "main", root)
	sub := filepath.Join(root, "sub")
	os.Mkdir(sub, 0o755)

	if got, err := Toplevel(sub); err != nil || got != root {
		t.Errorf("Toplevel = %q, %v; want %q", got, err, root)
	}
	if _, err := Toplevel(tmp); err == nil {
		t.Error("Toplevel outside a repository is not an error")
	}
	if HasCommits(root) {
		t.Error("a fresh repository has commits")
	}
	git(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	if !HasCommits(root) {
		t.Error("HasCommits is false after a commit")
	}
	if got := CurrentBranch(root); got != "main" {
		t.Errorf("CurrentBranch = %q", got)
	}
	git(t, root, "checkout", "-q", "--detach")
	if got := CurrentBranch(root); got != "" {
		t.Errorf("detached CurrentBranch = %q, want empty", got)
	}
	if got := CurrentBranch(tmp); got != "" {
		t.Errorf("CurrentBranch outside a repository = %q", got)
	}
}

func TestHasOrigin(t *testing.T) {
	root := newRepo(t)
	if HasOrigin(root) {
		t.Error("HasOrigin without a remote")
	}
	git(t, root, "remote", "add", "origin", filepath.Join(filepath.Dir(root), "origin.git"))
	if !HasOrigin(root) {
		t.Error("HasOrigin is false with an origin")
	}
}

func TestDeleteUnusedBranch(t *testing.T) {
	root := newRepo(t)
	git(t, root, "branch", "merged")
	git(t, root, "checkout", "-q", "-b", "ahead")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "work")
	git(t, root, "checkout", "-q", "main")

	if err := DeleteUnusedBranch(root, "ahead", "main"); err == nil || !strings.Contains(err.Error(), "not in main") {
		t.Errorf("a branch with new commits: %v", err)
	}
	if !refExists(root, "refs/heads/ahead") {
		t.Error("the branch with unmerged commits was deleted")
	}
	if err := DeleteUnusedBranch(root, "merged", "main"); err != nil {
		t.Fatal(err)
	}
	if refExists(root, "refs/heads/merged") {
		t.Error("the merged branch was kept")
	}
	if err := DeleteUnusedBranch(root, "gone", "main"); err == nil {
		t.Error("deleting a missing branch is not an error")
	}
}

func TestRemoveWorktreeFallbacks(t *testing.T) {
	root := newRepo(t)
	if err := RemoveWorktree(root, ""); err != nil {
		t.Errorf("empty path: %v", err)
	}
	if err := RemoveWorktree(root, filepath.Join(root, "missing")); err != nil {
		t.Errorf("missing path: %v", err)
	}

	stray := filepath.Join(root, "stray")
	os.MkdirAll(filepath.Join(stray, "nested"), 0o755)
	os.WriteFile(filepath.Join(stray, "nested", "f"), []byte("x"), 0o644)
	if err := RemoveWorktree(root, stray); err != nil {
		t.Errorf("a directory git doesn't know: %v", err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("the directory git refused to remove is still there: %v", err)
	}
}
