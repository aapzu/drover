# kangentic-tui

A terminal version of [Kangentic](https://github.com/Kangentic/kangentic): a
kanban board for AI coding agents. Move a card, an agent starts.

```
 ▦ kangentic  my-app on main                                              ⠧ 1 working  ◆ 1 need you
 1 To Do 2              2 Planning 1             3 Executing 1            4 Code Review 0
 backlog                plan · opus · xhigh      auto · opus · xhigh · ⚡2 auto · isolated · ⚡2
╭──────────────────────╮╭──────────────────────╮╭──────────────────────╮
│ #3 Upgrade React     ││ #1 Fix the login bug ││ #2 Add dark mode     │
│                      ││                      ││ to settings page     │
│                      ││ ◆ needs you fix-the… ││ ⠧ working add-dark…  │
╰──────────────────────╯╰──────────────────────╯╰──────────────────────╯
╭─ #1 Fix the login bug · enter to attach, Ctrl-q inside to return ───────────────────────────╮
│ live view of the agent's terminal …                                                          │
```

## Requirements

- `tmux` and `git`
- [Claude Code](https://claude.com/claude-code) (`claude`) on your `PATH`, signed in

## Install

```sh
go install github.com/aapelihaanpuu/kangentic-tui@latest
# or, from a checkout:
go build -o ~/.local/bin/kangentic-tui .
```

Install the binary somewhere permanent. Claude's hooks call it to report
activity, so don't run it with `go run`.

## Use

```sh
cd your-repo
kangentic-tui
```

| Key | Action |
| --- | --- |
| `←→` / `h l`, `↑↓` / `j k` | select column / card |
| `H` / `L` (or shift+←→) | move card to previous / next column |
| `m` then `1`–`9` | move card to a column |
| `n`, `e`, `x` | new, edit, delete task |
| `enter` | attach to the agent's terminal (`Ctrl-q` returns) |
| `s` | send the agent a message |
| `r` / `S` | start or resume / suspend the agent |
| `p`, `i`, `?` | toggle preview, task details and log, help |
| `q` | quit (agents keep running) |

## How it maps to Kangentic

- **Same board file.** Columns come from `kangentic.json` at the repo root,
  in the desktop app's format, so you and your teammates share one board. With
  no file it uses a built-in default; press `W` to write it out.
  `kangentic.local.json` can override `defaultBaseBranch`.
- **Worktree per task.** Entering an agent column creates
  `.kangentic/worktrees/<N>` on branch `<slug>-<id8>` from the base branch.
  To Do removes the worktree and resets the conversation. Done suspends the
  agent and removes the worktree, asking first if there are uncommitted
  changes. Branches are never deleted.
- **Column settings.** `autoSpawn`, `permissionMode`, `modelOverride` and
  `effortOverride` become `claude --permission-mode/--model/--effort`. A model
  or effort change respawns the agent with `--resume`. So does a permission
  change, but only while the agent is idle. `sessionTarget: "isolated"` and
  `sessionSpawnStrategy: "always_spawn_new"` give a column its own
  conversation.
- **Automations.** `onEnter`, `onTurnEnd` and `onExit` rows of type
  `send_message` (`immediate` or deferred), `move_to_column`, `run_script`,
  `webhook` and `notify`, with `finalLine`, `maxRuns`, `condition` (checked
  with `claude -p --model haiku`) and `{{placeholders}}` such as
  `{{baseBranch}}` and `{{agentReply}}`. `planExitTarget` moves a card once
  its plan is approved.
- **Activity.** A `--settings` file adds Claude Code hooks that append events
  to `.kangentic/tui/sessions/…/events.jsonl`. The board reads them to show
  working, idle and needs-you states and to run turn-end automations.

## Differences from the desktop app

- Tasks live in `.kangentic/tui/state.json`, not in the desktop app's
  database, so the two apps don't share tasks. They do share the board.
- Agents run in a private tmux server (`tmux -L kangentic-tui ls`) and keep
  running after you quit. Automations only run while the board is open.
  Events that arrive while it's closed are processed when you reopen it.
- Only Claude Code is supported. Not yet supported: issue imports,
  workflows, the MCP board tools, PR status, and multi-project view.
