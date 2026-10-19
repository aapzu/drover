# drover

A kanban board for AI coding agents, in your terminal. Move a card, an agent
starts. It began as a terminal version of
[Kangentic](https://github.com/Kangentic/kangentic) and still reads its
`kangentic.json`.

```
 ▦ drover     my-app on main                                              ⠧ 1 working  ◆ 1 need you
 1 Sessions 2           2 Planning 1             3 Executing 1            4 Code Review 0
 sessions · main check… plan · opus · xhigh      auto · opus · xhigh · ⚡2 auto · isolated · ⚡2
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
go install github.com/aapzu/drover@latest
# or, from a checkout:
go build -o ~/.local/bin/drover .
```

Install the binary somewhere permanent. Claude's hooks call it to report
activity, so don't run it with `go run`.

## Use

```sh
cd your-repo
drover
```

| Key | Action |
| --- | --- |
| `←→` / `h l`, `↑↓` / `j k` | select column / card |
| `H` / `L` (or shift+←→) | move card to previous / next column |
| `m` then `1`–`9` | move card to a column |
| `n`, `e`, `x` | new Claude session (attaches right away), edit, delete task |
| `I` | import Claude Code agents and sessions, or Linear issues (`tab` switches) |
| `enter` | attach to the agent's terminal (`Ctrl-q` returns) |
| `tab` | type into the agent from the preview (`Ctrl-q` returns) |
| `s` | send the agent a message |
| `r` / `S` | start or resume / suspend the agent |
| `+` / `-`, `<` / `>` | taller / shorter preview, narrower / wider column |
| `z`, `0` | cycle card size (normal, compact, tall with the last reply), reset layout |
| `p`, `i`, `?` | toggle preview, task details and log, help |
| `M` | toggle mouse capture (off lets the terminal select text) |
| `q` | quit (agents keep running) |
| `Q` | suspend every agent and quit, printing a `claude --resume` command for each |

With the mouse on, drag a card onto another column to move it, double-click
a card to attach, drag a column's right edge or the preview's top border to
resize, and click the preview to type into the agent. Layout is saved in
`ui.json` in the project's state directory.

In live mode (`tab` or a click on the preview) every key goes to the agent,
`esc` and `ctrl+c` included. `ctrl+q` returns to the board.

The mouse wheel over the preview scrolls the agent's history, as do `pgup` /
`pgdown` on the board. Typing in live mode jumps back to the bottom.

## How it maps to Kangentic

- **Same board file.** Columns come from `kangentic.json` at the repo root,
  in the desktop app's format, so you and your teammates share one board. With
  no file it uses a built-in default (Sessions, Planning, Executing,
  Self-review, In review, Verify in develop, Done); press `W` to write it out.
  `kangentic.local.json` can override `defaultBaseBranch`.
- **Sessions first, branches later.** `n` opens a Claude session in the main
  checkout, on a card in the first (To Do role) column. Your first message
  becomes the card's title. When that turn ends, the card moves to the first
  agent column that is neither in plan mode nor isolated (Executing by
  default), which creates its worktree and branch and resumes the same
  conversation there; if you're attached, you follow it. Move it yourself to
  go elsewhere sooner.
- **Worktree per task.** Entering an agent column creates
  `.kangentic/worktrees/<N>` on branch `<slug>-<id8>` from the base branch.
  To Do removes the worktree and resets the conversation. Done suspends the
  agent and removes the worktree, asking first if there are uncommitted
  changes. Branches are never deleted.
- **Column settings.** `autoSpawn`, `permissionMode`, `modelOverride` and
  `effortOverride` become `claude --permission-mode/--model/--effort`. Columns
  without `permissionMode` use the board's `defaultPermissionMode`, or `auto`
  when that is unset. A model
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
  to `sessions/…/events.jsonl` in the state directory. The board reads them
  to show working, idle and needs-you states and to run turn-end automations.

## One card per pull request

Every pull request an agent opens (`gh pr create` or `gh stack`) gets its own
card. The session's card stays where the work is, so one session can ship
several PRs. The cards are marked `⧉ 1/3`, `⧉ 2/3` and so on, and the marker
has the same color on every card of the group.

- The cards share one agent, worktree and conversation. `enter`, `tab`, `s`
  and the preview all reach the same agent from any of them.
- Moving a card into an agent column points the agent at that card: it takes
  that column's settings and runs its automations with the card's PR. Turn-end
  automations act only on the card the agent is working for.
- The agent stops once no card in the group is in an agent column. The
  worktree goes once every card is in Done or To Do, and the conversation
  resets once every card is back in To Do.
- Deleting a card removes only that card while others remain.
- `{{prUrl}}`, `{{prNumber}}`, `{{prTitle}}` and `{{prBranch}}` work in
  automations. `{{branchName}}` is the PR's branch when known.
- `gh pr view` refreshes every open PR's card once a minute. Cards
  show `✓ approved`, `✗ changes`, `merged` and `released`.

### Following a PR to release

`prFlow` in `kangentic.json` moves PR cards as their PR progresses. Targets are
column ids or names; leave one out to keep cards where they are. The default
board uses:

```json
"prFlow": {
  "opened": "in-review",
  "changesRequested": "executing",
  "changesRequestedMessage": "PR #{{prNumber}} got review comments…",
  "merged": "verify",
  "released": "done"
}
```

- **opened:** where a new PR card goes.
- **changesRequested:** a new review requesting changes moves a card from
  `opened` here and sends the agent `changesRequestedMessage`. Once the agent
  has pushed a new head and is idle, the card goes back to `opened`.
- **merged:** where a merged PR card goes, for example to verify it in a
  develop environment.
- **released:** once the merge commit reaches `releaseBranch` on origin
  (default `master`, else `main`), a card in `merged` moves here.

Moves only happen when the PR changes, so a card you move by hand stays put.
The session card never moves by itself; you get a notice once all its PRs are
released.

## Importing Claude Code agents

`I` lists Claude Code conversations started in this repo or any directory
below it, such as worktrees. Running agents come first and are preselected.
Past sessions follow, newest first.

- **Running agents** land in the first agent column. The card mirrors the
  agent's working or idle status, and the preview shows its latest reply.
  While it runs in its own terminal, the board won't start a second copy of
  it, and moves to To Do or Done are refused. Once you exit it there, press
  `r` and the board resumes the conversation with `claude --resume` in the
  agent's original directory.
- **Past sessions** land in To Do. Moving one into an agent column resumes it.
- **Board conversations you resume yourself**, for example with the commands
  `Q` prints, are tracked the same way. The board won't start them again
  until you exit them.
- The board never deletes a directory it didn't create. Imported sessions
  that live in the main checkout or in another tool's worktree keep their
  directory.

## Linear

Set `LINEAR_API_KEY` to a personal API key (Linear → Settings → Security &
access). Then `I`, followed by `tab`, lists your open assigned issues. `/`
fetches any issue by key or URL.

- Imported issues land in To Do. The issue description becomes the prompt.
- The worktree uses Linear's branch name (for example
  `you/dx-123-fix-login`), so pull requests link back to the issue.
- `{{issueKey}}` and `{{issueUrl}}` work in automations.
- With `DROVER_LINEAR_SYNC=1`, starting an agent on an unstarted issue
  moves it to the team's first "started" state, such as In Progress.

## Differences from the desktop app

- Tasks live in `$XDG_STATE_HOME/drover/<repo>-<hash>/state.json`
  (default `~/.local/state`), outside the repo so they can't be committed.
  Every worktree of a repo opens the same board. The desktop app keeps tasks
  in its own database, so the two apps don't share tasks. They do share the
  board. State from older versions in `.kangentic/tui/` is moved there on
  first launch.
- Agents run in a private tmux server (`tmux -L drover ls`) and keep
  running after you quit. Automations only run while the board is open.
  Events that arrive while it's closed are processed when you reopen it.
- Only Claude Code is supported. Issues import from Linear only. Not yet
  supported: workflows, the MCP board tools and multi-project view.

## Migrating from kangentic-tui

- Running sessions call the old binary from their hooks, so keep
  `kangentic-tui` installed until they have restarted.
- Agents started by kangentic-tui are on its tmux server. Run
  `DROVER_TMUX_SOCKET=kangentic-tui drover` until they finish, or suspend and
  resume them.
