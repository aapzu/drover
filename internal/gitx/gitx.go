// Package gitx creates and removes the per-task worktrees under
// <repo>/.drover/worktrees/<task number>.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func Toplevel(dir string) (string, error) { return run(dir, "rev-parse", "--show-toplevel") }

// MainRoot is the main checkout of the repository dir belongs to, so every
// linked worktree resolves to the same project.
func MainRoot(dir string) (string, error) {
	out, err := run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	main, _, _ := strings.Cut(out, "\n\n")
	lines := strings.Split(main, "\n")
	path, ok := strings.CutPrefix(lines[0], "worktree ")
	if !ok || slices.Contains(lines, "bare") {
		return Toplevel(dir)
	}
	return path, nil
}

func HasCommits(root string) bool {
	_, err := run(root, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func refExists(root, ref string) bool {
	_, err := run(root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// ResolveBase picks the start point: the task's own base must exist, otherwise
// the configured default, main and master are tried in order.
func ResolveBase(root, taskBase, defaultBase string) (string, error) {
	if taskBase != "" {
		if refExists(root, taskBase) {
			return taskBase, nil
		}
		return "", fmt.Errorf("base branch %q not found", taskBase)
	}
	for _, candidate := range []string{defaultBase, "main", "master"} {
		if candidate != "" && refExists(root, candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("no base branch found (tried the configured default, main and master)")
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func BranchName(title, taskID string) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		slug = "task"
	}
	return slug + "-" + taskID[:8]
}

func WorktreesDir(root string) string { return filepath.Join(root, ".drover", "worktrees") }

// EnsureExcluded keeps .drover/ out of git status without touching the
// tracked .gitignore.
func EnsureExcluded(root string) error {
	commonDir, err := run(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	exclude := filepath.Join(commonDir, "info", "exclude")
	data, _ := os.ReadFile(exclude)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "/.drover/" || strings.TrimSpace(line) == ".drover/" {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		f.WriteString("\n")
	}
	_, err = f.WriteString("/.drover/\n")
	return err
}

// CreateWorktree checks out branch (creating it from base if needed) into
// .drover/worktrees/<folder>. An existing worktree is reused.
func CreateWorktree(root, folder, branch, base string) (string, error) {
	path := filepath.Join(WorktreesDir(root), folder)
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(WorktreesDir(root), 0o755); err != nil {
		return "", err
	}
	if entries, err := os.ReadDir(path); err == nil {
		if len(entries) > 0 {
			return "", fmt.Errorf("%s exists and is not empty", path)
		}
		os.Remove(path)
	}
	run(root, "worktree", "prune")

	args := []string{"worktree", "add"}
	if !refExists(root, "refs/heads/"+branch) {
		args = append(args, "-b", branch, path, base)
	} else {
		args = append(args, path, branch)
	}
	if _, err := run(root, args...); err != nil {
		return "", err
	}
	// Claude finds .claude/commands by walking up to the main checkout, so
	// keeping a copy in the worktree would list every command twice.
	if _, err := os.Stat(filepath.Join(path, ".claude", "commands")); err == nil {
		run(path, "sparse-checkout", "set", "--no-cone", "/*", "!/.claude/commands/")
	}
	linkNodeModules(root, path)
	return path, nil
}

func linkNodeModules(root, worktree string) {
	src := filepath.Join(root, "node_modules")
	dst := filepath.Join(worktree, "node_modules")
	if _, err := os.Stat(src); err != nil {
		return
	}
	if _, err := os.Lstat(dst); err == nil {
		return
	}
	os.Symlink(src, dst)
}

func RemoveWorktree(root, path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		run(root, "worktree", "prune")
		return nil
	}
	if _, err := run(root, "worktree", "remove", "--force", path); err != nil {
		if rmErr := os.RemoveAll(path); rmErr != nil {
			return fmt.Errorf("%w; fallback removal failed: %v", err, rmErr)
		}
		run(root, "worktree", "prune")
	}
	return nil
}

// Dirty reports uncommitted changes in the worktree, ignoring the
// node_modules link this package creates.
func Dirty(path string) bool {
	if path == "" {
		return false
	}
	out, err := run(path, "status", "--porcelain")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasSuffix(line, " node_modules") {
			return true
		}
	}
	return false
}

func CurrentBranch(dir string) string {
	out, _ := run(dir, "branch", "--show-current")
	return out
}

func DeleteBranch(root, branch string) error {
	_, err := run(root, "branch", "-D", branch)
	return err
}

// FetchRelease fetches the release branch from origin and returns its
// remote-tracking ref. An empty branch tries master, then main.
func FetchRelease(root, branch string) (string, error) {
	candidates := []string{branch}
	if branch == "" {
		candidates = []string{"master", "main"}
	}
	var errs []error
	for _, b := range candidates {
		_, err := run(root, "fetch", "--quiet", "origin", b)
		if err == nil {
			return "refs/remotes/origin/" + b, nil
		}
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}

// Contains reports whether commit is reachable from ref. A commit git doesn't
// have is not reachable.
func Contains(root, ref, commit string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", commit, ref)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1,
		strings.Contains(string(out), "Not a valid commit name"):
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %s", commit, ref, strings.TrimSpace(string(out)))
}
