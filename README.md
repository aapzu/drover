# drover

A kanban board for AI coding agents, in your terminal. Move a card, an agent
starts.

```
 ▦ drover     my-app on main                                              ⠧ 1 working  ◆ 1 need you
 1 Sessions 2           2 Planning 1             3 Executing 1            4 Code Review 0
 sessions · main check… plan · opus · xhigh      auto · opus · xhigh · ⚡2 auto · ⚡2
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
| `A` / `C` | archive or restore the selected card in Done / archive every card in Done |
| `v` | show or hide archived cards |
| `enter` | attach to the agent's terminal, starting it if needed (`Ctrl-q` returns) |
| `tab` | type into the agent from the preview (`Ctrl-q` returns) |
| `s` | send the agent a message |
| `r` / `S` | start or resume / suspend the agent |
| `c` | open a shell in the card's worktree, or the main checkout before it has one (`Ctrl-q` returns) |
| `u` | update the worktree from its base branch |
| `/` | filter cards by number, title, branch, issue or PR (`esc` clears) |
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

## How it works

- **Shared board file.** Columns come from `drover.json` at the repo root, so
  you and your teammates share one board. With
  no file it uses a built-in default (Sessions, Planning, Executing,
  Self-review, In review, Verify in develop, Done); press `W` to write it out.
  `drover.local.json` holds your own settings on top of it: `defaultBaseBranch`,
  `worktreesDir` and those under [Agents and notifications](#agents-and-notifications).
  `worktreesDir` (default `.drover/worktrees`) is where worktrees go, relative
  to the repo root or absolute, e.g. `.claude/worktrees`.
- **Archiving Done.** `archiveDoneAfter` (e.g. `"7d"`, `"36h"`) hides cards
  that have sat in Done that long. Archived cards stay in the state file and
  the column header shows how many are hidden (`Done 4 +37`). The built-in
  board uses `7d`; with the setting missing from your `drover.json` nothing is
  archived automatically. `drover.local.json` doesn't read it. Archiving
  removes the card's worktree; a worktree shared by several cards goes once all
  of them are archived or in To Do. `A` asks before deleting uncommitted
  changes, while `C` and the automatic archive keep such a worktree.
- **Sessions first, branches later.** `n` opens a Claude session in the main
  checkout, on a card in the first (To Do role) column. Your first message
  becomes the card's title, which Claude Haiku then shortens to a few words
  unless `autoName` is `false`. The card then moves to the first agent column
  (Planning by default), which creates its worktree and branch and resumes the
  same conversation there; if you're attached, you follow it. On boards with a
  plan column, a small Claude Haiku check of that message sends tasks that
  need no plan, such as a small fix, a question or "just do it", to the first
  agent column that isn't a plan column instead. When it's unsure or fails,
  the card plans. Move the card yourself to go elsewhere sooner.
- **Worktree per task.** Entering an agent column creates
  `.drover/worktrees/drover-<N>` on branch `<slug>-<id8>` from the base branch.
  To Do removes the worktree and resets the conversation. Done suspends the
  agent and keeps the worktree; archiving the card removes it, asking first if
  there are uncommitted changes. Branches are never deleted.
- **Worktree setup.** `copyFiles` lists paths git doesn't track, such as
  `.env`, to copy from the main checkout into each new worktree. They never
  count as uncommitted changes. `initScript` then runs with `sh -c` in the new
  worktree before the agent starts, for example `pnpm install`. If it fails,
  so does the move, and the next start runs it again.
- **Keeping up with the base branch.** `u` fetches the card's base branch and
  fast-forwards the worktree to it. When the branch has commits of its own,
  a running agent is asked to merge it instead. `u` refuses while the agent
  is working or the worktree has uncommitted changes.
- **Column settings.** `autoSpawn`, `permissionMode`, `modelOverride` and
  `effortOverride` become `claude --permission-mode/--model/--effort`. Columns
  without `permissionMode` use the board's `defaultPermissionMode`, or Claude
  Code's own default when that is unset. A model or effort change respawns the
  agent with `--resume`. A permission change switches it in place while it is
  idle, and respawns it when that fails or the target leaves the mode to Claude
  Code. A card keeps one conversation across every column.
- **Automations.** Columns run rows when a card enters them or an agent's
  turn ends; see [Automations](#automations). `planExitTarget` moves a card
  once its plan is approved.
- **Context and cost.** After each turn the board reads the conversation's
  transcript. Cards show how full the agent's context is and an estimate of
  what the conversation has cost at list prices, like `42% · $1.20`, and the
  header shows the board's total.
- **Agents know the board.** Each agent gets `--append-system-prompt` telling
  it that it runs on a drover board, that its card number is in its
  `$DROVER_TASK_NUMBER` environment variable, and how to use `drover move` and
  `drover list`, so "move this card to In review" works in a conversation.
  Moves behave as when you make them: a column without an agent suspends it.
  This is not the `$DROVER_TASKNUMBER` that automation scripts get.
- **Activity.** A `--settings` file adds Claude Code hooks that append events
  to `sessions/…/events.jsonl` in the state directory. The board reads them
  to show working, idle and needs-you states and to run turn-end automations.

## Agents and notifications

These settings work in `drover.json` and, for you alone, in
`drover.local.json`. Both files are reloaded while the board runs.

```json
{
  "maxConcurrent": 8,
  "idleTimeoutMinutes": 0,
  "notifications": { "turnDone": true, "needsYou": true, "planReady": true, "exited": true, "cooldownSeconds": 10 },
  "autoName": true
}
```

- **`maxConcurrent`** (default 8) caps running agents. Further starts queue,
  except that a start you make suspends the agent that has been idle longest.
- **`idleTimeoutMinutes`** suspends an agent once it has been idle that long.
  `r` resumes it. 0, the default, never does.
- **`notifications`** shows a desktop notification and a notice on the board
  when an agent finishes a turn and stays idle, asks for a permission, has a
  plan for you to approve, or exits by itself. Each is on by default and fires
  at most once per `cooldownSeconds` for each agent.
- The agent you're attached to or typing into neither notifies you nor times
  out.
- **`autoName`** titles a new session's card with a short summary of your
  first message, written with `claude -p --model haiku`. A title you edit is
  never replaced.

## Automations

A column's `automations` has two lists of rows. `onEnter` runs when a card
enters the column, `onTurnEnd` each time the agent there finishes a turn. Rows
run top to bottom, and a failing row is logged on the card without stopping
the rest; `move_to_column` ends the list either way. Nothing runs when a card enters To Do or Done.

| Type | Does | Fields |
| --- | --- | --- |
| `send_message` | types `message` into the agent, or queues it until the agent is idle | `message` |
| `run_script` | runs `script` with `sh -c` in the worktree, killed after `timeoutMinutes` (default 10) | `script`, `timeoutMinutes` |
| `notify` | shows a desktop notification and a notice on the board | `title` (default `{{title}}`), `body` (default `{{toColumn}}`) |
| `move_to_column` | moves the card, `onTurnEnd` only | `targetColumn` (default: the next column), `message` (sent after the move) |

- **Columns without an agent** run `run_script` and `notify` rows and skip
  `send_message`.
- **A fresh agent** starts with an `onEnter` group's first `send_message`,
  if it has no `condition`, instead of the task prompt, so a review column can open with
  `/code-review`.
- **`condition`** on any row asks `claude -p --model haiku` whether it holds
  for the agent's last reply. The row is skipped unless the answer is yes.
- **Placeholders** in `message`, `script`, `title` and `body`: `{{title}}`,
  `{{description}}`, `{{taskNumber}}`, `{{taskId}}`, `{{branchName}}`,
  `{{baseBranch}}`, `{{worktreePath}}`, `{{projectPath}}`, `{{projectName}}`,
  `{{prUrl}}`, `{{prNumber}}`, `{{prTitle}}`, `{{prBranch}}`, `{{issueKey}}`,
  `{{issueUrl}}`, `{{fromColumn}}`, `{{toColumn}}`, `{{trigger}}` and
  `{{agentReply}}`. In a `script` they are shell-quoted, and each is also set
  as an environment variable such as `$DROVER_TASKNUMBER`.

This repo's own board reviews and builds on entering Self-review, asks for
fixes if the review reported issues, and notifies you once a PR is in
review:

```json
"automations": {
  "onEnter": [
    { "name": "Review", "type": "send_message", "message": "/pr-review-toolkit:review-pr" },
    { "name": "Build", "type": "run_script", "script": "go build ./... && go vet ./..." }
  ],
  "onTurnEnd": [
    { "name": "Apply fixes", "type": "send_message",
      "message": "Apply the fixes the review found, then run go test ./...",
      "condition": "The message lists specific review findings that are still unfixed" }
  ]
}
```

## One card per pull request

The first pull request an agent opens (`gh pr create` or `gh stack`) stays on
the session's own card, which moves to the `prFlow` opened column once the
agent's turn ends. Every further PR gets its own card, so one session can ship
several. The cards are marked `⧉ 1/3`, `⧉ 2/3` and so on, and the marker has
the same color on every card of the group.

- The cards share one agent, worktree and conversation. `enter`, `tab`, `s`
  and the preview all reach the same agent from any of them.
- Moving a card into an agent column points the agent at that card: it takes
  that column's settings and runs its automations with the card's PR. Turn-end
  automations act only on the card the agent is working for.
- The agent stops once no card in the group is in an agent column. The
  worktree goes once every card is archived or in To Do, and the conversation
  resets once every card is back in To Do.
- Deleting a card removes only that card while others remain.
- `{{prUrl}}`, `{{prNumber}}`, `{{prTitle}}` and `{{prBranch}}` work in
  automations. `{{branchName}}` is the PR's branch when known.
- `gh pr view` refreshes every open PR's card once a minute. Cards
  show `✓ approved`, `✗ changes`, `merged` and `released`.

### Following a PR to release

`prFlow` in `drover.json` moves PR cards as their PR progresses. Targets are
column ids or names; leave one out to keep cards where they are. The default
board uses:

```json
"prFlow": {
  "requested": "self-review",
  "opened": "in-review",
  "changesRequested": "executing",
  "changesRequestedMessage": "PR #{{prNumber}} got review comments…",
  "merged": "verify",
  "released": "done"
}
```

- **requested:** a prompt asking the agent to open a PR, like "open a PR" or
  "create pull requests", moves its card here from an earlier agent column.
  Negated asks ("don't open a PR yet") are ignored. The column must have
  `autoSpawn`. Its enter automations are skipped, as the agent is already on
  it, but its turn-end automations run when that turn ends.
- **opened:** where the session card goes once its first PR is open, and where
  each further PR's card starts.
- **changesRequested:** a new review requesting changes moves a card from
  `opened` here and sends the agent `changesRequestedMessage`. Once the agent
  has pushed a new head and is idle, the card goes back to `opened`.
- **merged:** where a merged PR card goes, for example to verify it in a
  develop environment.
- **released:** once the merge commit reaches `releaseBranch` on origin
  (default `master`, else `main`), a card in `merged` moves here.

Moves only happen when the PR changes, so a card you move by hand stays put,
including before the session card's move to `opened`. You get a notice once all
the session's PRs are released.

## Reviewing other people's PRs

Ask an agent to review a PR by its URL, for example
`/code-review https://github.com/o/r/pull/12`, and its card tracks that PR.
Any URL after a slash command with `review` in its name counts. In a plain
prompt a URL counts when "review" or "reviewing" comes shortly before it in the
same sentence, unless it is negated ("don't review") or reads "review
comments". `gh` then confirms the PR isn't yours. If it is, drover forgets it
again.

A session that is still in To Do takes the PR on its own card. One already
working on something gets a separate card for it.

drover itself never posts a review. Nothing stops the agent from posting if you
ask it to, for example with `--comment`.

The card shows `review PR #12 @author` once gh has confirmed it, and
`✓ you approved`, `✗ you requested changes` or `✓ you reviewed` once you have
reviewed it. `↻ new commits` replaces that when the author has pushed since.
`reviewFlow` in `drover.json` moves the card as the PR changes, with the
same column targets as `prFlow`:

```json
"reviewFlow": {
  "started": "reviewing",
  "reviewed": "waiting-on-author",
  "updated": "reviewing",
  "done": "done"
}
```

- **started:** where the card goes once the agent's turn ends and gh has
  confirmed the PR isn't yours, from To Do or an agent column.
- **reviewed:** where it goes, from any column, when a review of yours shows up
  on the PR once the agent is idle. A review already there when drover first
  sees the PR is only recorded.
- **updated:** you get a notice when the author pushes after your review. A
  card in `reviewed` also moves here.
- **done:** where it goes once the PR merges or closes. A PR that was already
  merged or closed when drover first sees it moves nothing.

Your own review is never sent to the agent as feedback to address, and review
cards don't follow a PR to release.

## Importing Claude Code agents

`I` lists Claude Code conversations started in this repo or any directory
below it, such as worktrees. Sessions are grouped by directory, directories
with running agents first. Within a directory, running agents come first and
are preselected, then past sessions, newest first. Only past sessions that
`claude agents` still lists are shown; terminal sessions it never tracked and
agents you dismissed there are hidden. The list loads the 40 newest past
sessions; `m` loads 40 more while older ones remain.

`space` on a directory header selects its sessions, or clears them if all are
selected. `a` does the same for every loaded session.

- **Running agents** land in the first agent column. The card mirrors the
  agent's working or idle status, and the preview shows its latest reply.
  While it runs in its own terminal, the board won't start a second copy of
  it, and moves to To Do or Done are refused. Once you exit it there, press
  `r` and the board resumes the conversation with `claude --resume` in the
  agent's original directory.
- **Past sessions** land in To Do. Moving one into an agent column resumes it.
- **Sessions that opened several PRs** are split into a card per PR as they
  are imported; the first PR stays on the session's own card.
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

## Command line

Scripts, hooks and agents can change the board from a shell. Run these from
the repo or any of its worktrees:

```sh
drover list [--json]
drover add "Fix the login bug" -p "Steps to reproduce…" -c planning
drover add --linear DX-123
drover move 4 "In review"
drover edit 4 --title "Fix signup" --prompt "…"
drover rm 4
```

- Cards are addressed by the number `list` shows (`4`; quote it as `'#4'` if you
  write the `#`). Columns are ids or names. A prompt of `-` is read from stdin.
- Without `--force`, `rm` fails when the card's worktree has uncommitted
  changes. `--force` discards them. The branch is kept.
- `add -c` into an agent column starts the agent, like a move. If that fails the
  card stays in To Do and the command says why.
- `add --linear` imports the issue, so the board needs `LINEAR_API_KEY`. The
  title and prompt come from the issue, and one already on the board is refused.
- The open board applies requests one at a time and the command prints the
  result. If the board doesn't answer within two minutes, the command fails but
  the change may still be applied, so run `list` before retrying.
- With no board open, the change is queued and applied the next time one opens.
  Errors then show up on the board, not in your shell. `list` reads the saved
  state, so it shows applied changes only.
- `list`, `add`, `move`, `edit` and `rm` are commands, so a directory with one
  of those names needs `./`.

## Notes

- Tasks live in `$XDG_STATE_HOME/drover/<repo>-<hash>/state.json`
  (default `~/.local/state`), outside the repo so they can't be committed.
  Every worktree of a repo opens the same board.
- Agents run in a private tmux server (`tmux -L drover ls`) and keep
  running after you quit. Automations only run while the board is open.
  Events that arrive while it's closed are processed when you reopen it.
- Only Claude Code is supported. Issues import from Linear only.
