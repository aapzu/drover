# drover

A Go (Bubble Tea) TUI; see `README.md` for what it does. Code is `main.go` plus the packages under `internal/`.

## Rules

- **Always work in a git worktree; never edit files in the main checkout**, even for a one-line fix. If you're already in a worktree (e.g. `.kangentic/worktrees/<N>`), work there. Otherwise create one before editing. The main checkout may hold someone else's uncommitted work, so don't edit, stash, or reset it.
- **If something doesn't work in the sandbox, run it outside the sandbox.** Git worktree and branch operations, tmux, `gh`, the Go build cache, network access and writes outside the repo are the usual failures. Retry the failing command with the sandbox disabled instead of working around it or giving up. Don't skip a step because it failed once in the sandbox.

## Workflow

- Branch names: `<author_first_name>/<ticket>-short-description`, with the first name taken from `git config user.name`, lowercased, e.g. `aapeli/dx-1234-foo-bar`. If there's no ticket, use `no-ticket`.
- Before saying something works, run `go build ./...` and `go test ./...`.
- Never use `go run` for drover. Claude's hooks call the binary that started the session, so build and install it with `go build -o ~/.local/bin/drover .`. Replacing that binary affects the hooks of agents already running.
- After opening a PR, monitor its CI checks (`gh pr checks <pr> --watch`) until they finish. If a check fails, read its logs, fix the cause, push, and monitor again. If it reports no checks, wait briefly and retry; if there are still none, there's nothing to monitor.
- Match the surrounding code's naming, idioms and comment density. Don't add comments that restate the code.
