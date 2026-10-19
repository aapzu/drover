package gitx

import (
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
