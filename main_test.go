package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectRoot(t *testing.T) {
	repo := realPath(t, t.TempDir())
	git(t, repo, "init", "-b", "main")
	git(t, repo, "commit", "--allow-empty", "-m", "init")
	sub := filepath.Join(repo, "a", "b")
	os.MkdirAll(sub, 0o755)
	worktree := filepath.Join(realPath(t, t.TempDir()), "wt")
	git(t, repo, "worktree", "add", "-b", "feature", worktree)
	plain := realPath(t, t.TempDir())

	for name, tc := range map[string]struct{ dir, want string }{
		"repo root":         {repo, repo},
		"subdirectory":      {sub, repo},
		"linked worktree":   {worktree, repo},
		"outside any repo":  {plain, plain},
		"missing directory": {filepath.Join(plain, "gone"), filepath.Join(plain, "gone")},
	} {
		got, err := projectRoot(tc.dir)
		if err != nil || got != tc.want {
			t.Errorf("%s: projectRoot = %q, %v, want %q", name, got, err, tc.want)
		}
	}

	t.Chdir(sub)
	if got, err := projectRoot("."); err != nil || got != repo {
		t.Errorf("projectRoot(.) from a subdirectory = %q, %v, want %q", got, err, repo)
	}
}

func TestRunNeedsTmuxAndClaude(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if err := run(t.TempDir()); err == nil || !strings.Contains(err.Error(), "tmux is required") {
		t.Errorf("run without tmux = %v", err)
	}
	os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"), 0o755)
	if err := run(t.TempDir()); err == nil || !strings.Contains(err.Error(), "claude CLI must be on PATH") {
		t.Errorf("run without claude = %v", err)
	}
}

// TestMainProcess is main run in a child process, since main exits on errors.
func TestMainProcess(t *testing.T) {
	args, ok := os.LookupEnv("DROVER_TEST_ARGS")
	if !ok {
		t.Skip("only runs as a child of runMain")
	}
	os.Args = append([]string{"drover"}, strings.Fields(args)...)
	main()
}

func runMain(t *testing.T, args string) (stdout, stderr string, exit int) {
	t.Helper()
	return runMainIn(t, t.TempDir(), args, "")
}

func runMainIn(t *testing.T, dir, args, stdin string) (stdout, stderr string, exit int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainProcess$")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = []string{"DROVER_TEST_ARGS=" + args, "XDG_STATE_HOME=" + t.TempDir(), "PATH=" + t.TempDir()}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("drover %s still running after 30s", args)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), exit
}

func TestMainDispatch(t *testing.T) {
	for name, tc := range map[string]struct {
		args       string
		exit       int
		out, errIn string
	}{
		"help word":                   {"help", 0, "Usage:", ""},
		"short help":                  {"-h", 0, "drover ask", ""},
		"long help":                   {"--help", 0, "Change the board from outside it", ""},
		"hook without a file":         {"hook", 0, "", ""},
		"cli command error":           {"move 1 nowhere", 1, "", `drover: unknown column "nowhere"`},
		"a directory opens the board": {"some-dir", 1, "", "drover: tmux is required"},
		"no arguments open the board": {"", 1, "", "drover: tmux is required"},
	} {
		t.Run(name, func(t *testing.T) {
			out, errOut, exit := runMain(t, tc.args)
			if exit != tc.exit || !strings.Contains(out, tc.out) || !strings.Contains(errOut, tc.errIn) {
				t.Errorf("exit = %d, stdout = %q, stderr = %q", exit, out, errOut)
			}
		})
	}
}

func TestMainPrintsTheVersion(t *testing.T) {
	want := "drover " + buildVersion() + "\n"
	for _, arg := range []string{"version", "--version", "-v"} {
		if out, _, exit := runMain(t, arg); exit != 0 || !strings.Contains(out, want) {
			t.Errorf("drover %s: exit = %d, stdout = %q, want it to contain %q", arg, exit, out, want)
		}
	}
}

func TestHookRecordsTheEventFromStdin(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "events.jsonl")

	runMainIn(t, dir, "hook "+events, "{\n  \"hook_event_name\": \"Stop\",\n  \"session_id\": \"s1\"\n}\n")
	runMainIn(t, dir, "hook "+events, `{"hook_event_name":"UserPromptSubmit"}`)

	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"hook_event_name":"Stop","session_id":"s1"}` + "\n" + `{"hook_event_name":"UserPromptSubmit"}` + "\n"
	if string(data) != want {
		t.Errorf("events file = %q, want one compact line per event: %q", data, want)
	}
}

func TestHookIgnoresWhatItCannotRecord(t *testing.T) {
	dir := t.TempDir()
	for name, args := range map[string]struct{ args, stdin string }{
		"not JSON":            {"hook events.jsonl", "not json"},
		"two arguments":       {"hook events.jsonl extra", `{"hook_event_name":"Stop"}`},
		"gate with arguments": {"hook-gate a b", ""},
	} {
		if out, errOut, exit := runMainIn(t, dir, args.args, args.stdin); exit != 0 || errOut != "" {
			t.Errorf("%s: exit = %d, stdout = %q, stderr = %q, want a silent success", name, exit, out, errOut)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("hooks that cannot record left %d files behind", len(entries))
	}
}

func TestHookGateReturnsAtOnceWithoutAGate(t *testing.T) {
	start := time.Now()
	if _, errOut, exit := runMainIn(t, t.TempDir(), "hook-gate "+filepath.Join(t.TempDir(), "gate"), ""); exit != 0 || errOut != "" {
		t.Errorf("exit = %d, stderr = %q", exit, errOut)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("hook-gate held the agent for %s though no gate was open", d)
	}
}
