// Package gitx creates and removes the per-task worktrees under
// <worktrees dir>/drover-<task number>.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

func run(dir string, args ...string) (string, error) {
	out, err := runRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// runRaw is run without trimming, for NUL-separated output whose first entry
// may start with a space.
func runRaw(dir string, args ...string) (string, error) {
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
	return string(out), nil
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
	return path, nil
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

// LinkFiles symlinks files and directories that git doesn't track, gitignored
// ones included, from root into a new worktree. With only set it links just
// those paths and ignores except, otherwise everything except the except
// paths. Tracked files, missing paths and paths outside root are skipped, and
// so are .claude, which Claude already finds in the main checkout, .drover,
// the worktrees directory, node_modules and any directory holding a .git, such
// as another tool's worktree. Untracked files inside a listed tracked
// directory are linked. A gitignored directory becomes a real directory of
// links, since an ignore rule ending in a slash no longer matches a symlink and
// git add -A would stage it.
func LinkFiles(root, worktree, worktreesDir string, only, except []string) error {
	out, err := runRaw(root, "ls-files", "--others", "--directory", "--no-empty-directory", "-z")
	if err != nil {
		return err
	}
	var candidates []string
	for c := range strings.SplitSeq(out, "\x00") {
		if c = strings.TrimSuffix(c, "/"); c != "" {
			candidates = append(candidates, c)
		}
	}
	l := linker{root: root, worktree: worktree, skip: []string{".claude", ".drover"}}
	if rel, err := filepath.Rel(root, worktreesDir); err == nil && rel != "." && filepath.IsLocal(rel) {
		l.skip = append(l.skip, filepath.ToSlash(rel))
	}
	targets := candidates
	if len(only) == 0 {
		l.skip = append(l.skip, cleanPaths(except)...)
	} else {
		targets = nil
		for _, p := range cleanPaths(only) {
			for _, c := range candidates {
				switch {
				case c == p || under(p, c):
					targets = append(targets, p)
				case under(c, p):
					targets = append(targets, c)
				}
			}
		}
	}
	for _, t := range targets {
		if l.inNestedRepo(t) {
			continue
		}
		if err := l.link(t, false); err != nil {
			return fmt.Errorf("link %s: %w", t, err)
		}
	}
	return nil
}

// under reports whether rel lies inside dir.
func under(rel, dir string) bool { return strings.HasPrefix(rel, dir+"/") }

func cleanPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p = filepath.Clean(p); filepath.IsLocal(p) {
			out = append(out, filepath.ToSlash(p))
		}
	}
	return out
}

type linker struct {
	root, worktree string
	skip           []string
}

func (l *linker) skipped(rel string) bool {
	if parts := strings.Split(rel, "/"); slices.Contains(parts, "node_modules") || slices.Contains(parts, ".git") {
		return true
	}
	return slices.ContainsFunc(l.skip, func(p string) bool { return rel == p || under(rel, p) })
}

// link symlinks rel into the worktree unless it is skipped, missing, already
// there or a directory holding a .git. A directory holding a skipped path is
// not linked whole; its entries are linked one by one instead, and so are the
// entries of a gitignored one, unless it is itself inside such a directory.
func (l *linker) link(rel string, underIgnored bool) error {
	if l.skipped(rel) {
		return nil
	}
	src := filepath.Join(l.root, rel)
	info, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if info.IsDir() {
		if _, err := os.Lstat(filepath.Join(src, ".git")); err == nil {
			return nil
		}
		ignored := false
		if !underIgnored {
			if ignored, err = l.ignored(rel); err != nil {
				return err
			}
		}
		if ignored || l.holdsSkipped(src, rel) {
			entries, err := os.ReadDir(src)
			if errors.Is(err, fs.ErrPermission) {
				return nil
			} else if err != nil {
				return err
			}
			for _, e := range entries {
				if err := l.link(rel+"/"+e.Name(), underIgnored || ignored); err != nil {
					return err
				}
			}
			return nil
		}
	}
	dst := filepath.Join(l.worktree, rel)
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return skipIfTaken(err)
	}
	return skipIfTaken(os.Symlink(src, dst))
}

// ignored reports whether git ignores rel in root. check-ignore exits 1 for a
// path that isn't, and anything else is a failure.
func (l *linker) ignored(rel string) (bool, error) {
	cmd := exec.Command("git", "check-ignore", "-q", "--", rel)
	cmd.Dir = l.root
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("git check-ignore %s: %w", rel, err)
	}
	return true, nil
}

// inNestedRepo reports whether a directory above rel holds a .git.
func (l *linker) inNestedRepo(rel string) bool {
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(l.root, dir, ".git")); err == nil {
			return true
		}
	}
	return false
}

// skipIfTaken drops the error of a path the worktree's branch already uses for
// something else, such as a tracked file where the main checkout has a directory.
func skipIfTaken(err error) error {
	if errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	return err
}

func (l *linker) holdsSkipped(src, rel string) bool {
	found := false
	filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if sub, _ := filepath.Rel(src, path); err != nil || found || (sub != "." && l.skipped(rel+"/"+filepath.ToSlash(sub))) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// Dirty reports uncommitted changes in the worktree, ignoring untracked
// symlinks whose target is the absolute path of the same file under root, such
// as the ones LinkFiles creates.
func Dirty(path, root string) bool {
	if path == "" {
		return false
	}
	out, err := runRaw(path, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return false
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		if len(entry) > 3 && !(strings.HasPrefix(entry, "?? ") && linksTo(filepath.Join(path, entry[3:]), filepath.Join(root, entry[3:]))) {
			return true
		}
	}
	return false
}

func linksTo(path, target string) bool {
	got, err := os.Readlink(path)
	return err == nil && got == target
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
