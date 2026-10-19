package gitx

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchReleaseAndContains(t *testing.T) {
	tmp := t.TempDir()
	root, origin := filepath.Join(tmp, "repo"), filepath.Join(tmp, "origin.git")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = tmp
		if len(args) > 0 && args[0] != "init" {
			cmd.Dir = root
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main", root)
	git("init", "-q", "--bare", origin)
	git("commit", "-q", "--allow-empty", "-m", "init")
	git("remote", "add", "origin", origin)

	if _, err := FetchRelease(root, ""); err == nil {
		t.Fatal("fetched a release branch from an empty origin")
	}
	git("push", "-q", "origin", "main")
	ref, err := FetchRelease(root, "")
	if err != nil || ref != "refs/remotes/origin/main" {
		t.Fatalf("FetchRelease = %q, %v; want origin/main as the fallback", ref, err)
	}

	head := git("rev-parse", "HEAD")
	feature := git("commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "feature")
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
