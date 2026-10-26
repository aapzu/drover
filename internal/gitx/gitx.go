// Package gitx creates and removes the per-task worktrees under
// <worktrees dir>/drover-<task number>.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

// EnsureExcluded keeps .drover/ and the worktrees directory, when it is
// inside the repo, out of git status.
func EnsureExcluded(root, worktrees string) error {
	patterns := []string{"/.drover/"}
	if rel, err := filepath.Rel(root, worktrees); err == nil && rel != "." && filepath.IsLocal(rel) {
		patterns = append(patterns, "/"+filepath.ToSlash(rel)+"/")
	}
	commonDir, err := run(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	excludeFile := filepath.Join(commonDir, "info", "exclude")
	for _, pattern := range patterns {
		if err := appendExclude(excludeFile, pattern); err != nil {
			return err
		}
	}
	return nil
}

func appendExclude(exclude, pattern string) error {
	data, _ := os.ReadFile(exclude)
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line == pattern || line == strings.TrimPrefix(pattern, "/") {
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
	_, err = f.WriteString(pattern + "\n")
	return err
}

// CreateWorktree checks out branch (creating it from base if needed) into
// <worktrees>/<folder>. An existing worktree is reused.
func CreateWorktree(root, worktrees, folder, branch, base string) (string, error) {
	path := filepath.Join(worktrees, folder)
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(worktrees, 0o755); err != nil {
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

// CopyFiles copies files and directories that git doesn't track, such as
// .env, from root into a new worktree. Symlinks are copied as the files they
// point to. Missing paths and paths outside root are skipped, and so is
// .claude/, which Claude already finds in the main checkout.
func CopyFiles(root, worktree string, paths []string) error {
	for _, p := range paths {
		p = filepath.Clean(p)
		if !filepath.IsLocal(p) || p == ".claude" || strings.HasPrefix(p, ".claude"+string(filepath.Separator)) {
			continue
		}
		src := filepath.Join(root, p)
		if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			return copyFile(path, filepath.Join(worktree, rel))
		})
		if err != nil {
			return fmt.Errorf("copy %s: %w", p, err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Dirty reports uncommitted changes in the worktree, ignoring untracked
// files under the node_modules link this package creates and the given
// paths, such as the files CopyFiles brought in.
func Dirty(path string, ignore ...string) bool {
	if path == "" {
		return false
	}
	out, err := run(path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false
	}
	ignore = append(slices.Clip(ignore), "node_modules")
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 3 && !(strings.HasPrefix(line, "?? ") && ignored(line[3:], ignore)) {
			return true
		}
	}
	return false
}

func ignored(path string, ignore []string) bool {
	path = strings.TrimSuffix(path, "/")
	for _, p := range ignore {
		p = filepath.ToSlash(filepath.Clean(p))
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func CurrentBranch(dir string) string {
	out, _ := run(dir, "branch", "--show-current")
	return out
}

// DeleteUnusedBranch deletes branch when every commit on it is already in
// base, so no work is lost whatever the main checkout has checked out.
func DeleteUnusedBranch(root, branch, base string) error {
	if _, err := run(root, "merge-base", "--is-ancestor", branch, base); err != nil {
		return fmt.Errorf("it has commits that are not in %s", base)
	}
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
		ref, err := Fetch(root, b)
		if err == nil {
			return ref, nil
		}
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}

func HasOrigin(root string) bool {
	_, err := run(root, "remote", "get-url", "origin")
	return err == nil
}

// Fetch fetches branch from origin and returns its remote-tracking ref.
func Fetch(root, branch string) (string, error) {
	if _, err := run(root, "fetch", "--quiet", "origin", branch); err != nil {
		return "", err
	}
	return "refs/remotes/origin/" + branch, nil
}

var ErrDiverged = errors.New("branch has diverged")

// FastForward moves the worktree's branch up to ref and returns how many
// commits it gained.
func FastForward(dir, ref string) (int, error) {
	out, err := run(dir, "rev-list", "--count", "HEAD.."+ref)
	if err != nil {
		return 0, err
	}
	behind, _ := strconv.Atoi(out)
	if behind == 0 {
		return 0, nil
	}
	if ok, err := Contains(dir, ref, "HEAD"); err != nil {
		return 0, err
	} else if !ok {
		return 0, ErrDiverged
	}
	if _, err := run(dir, "merge", "--ff-only", "--quiet", ref); err != nil {
		return 0, err
	}
	return behind, nil
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
