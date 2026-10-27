# drover

A kanban board for AI coding agents, in your terminal. Move a card, an agent
starts.

```
 ▦ drover     my-app on main                                              ⠧ 1 working  ◆ 1 need you
 1 Planning 1            2 Executing 1           3 Code Review 1
 plan · opus · xhigh     auto · opus · xhigh · ⚡2 auto · ⚡2
╭──────────────────────╮╭──────────────────────╮╭──────────────────────╮
│ #3 Upgrade React     ││ #1 Fix the login bug ││ #2 Add dark mode     │
│ ○ not started        ││ ◆ needs you fix-the… ││ ⠧ working add-dark…  │
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
| `ctrl+k` | search and run a command |
| `←→` / `h l`, `↑↓` / `j k` | select column / card |
| `H` / `L` (or shift+←→) | move card to previous / next column |
| `m`, then ↑↓ and enter, or `1`–`9` | move card to a column |
| `n`, `e`, `x` | new card with an agent in the first column (attaches right away), edit, delete task |
| `I` | import Claude Code agents and sessions, or Linear issues (`tab` switches) |
| `A` / `C` | archive or restore the selected card in Done / archive every card in Done |
| `v` | show or hide archived cards |
| `b` / `B` | hide or unhide the selected card in any column / show or hide hidden cards |
| `t` | show or hide column settings under column names |
| `enter` | attach to the agent's terminal, starting it if needed (`Ctrl-q` returns) |
| `tab` | type into the agent from the preview (`Ctrl-q` returns) |
| `s` | send the agent a message |
| `r` / `S` | start or resume / suspend the agent |
| `c` | open a shell in the card's worktree, or the main checkout before it has one (`Ctrl-q` returns) |
| `u` | update the worktree from its base branch |
| `:` / `N` | open the assistant panel and type into it (`Ctrl-q` returns to the board and hides it) / start a new assistant conversation |
| `/` | filter cards by number, title, branch, issue or PR (`esc` clears) |
| `+` / `-`, `<` / `>` | taller / shorter preview, narrower / wider column |
| `z`, `0` | cycle card size (normal, compact, tall with the last reply), reset layout |
| `p`, `i`, `?` | toggle preview, task details and log, help |
| `M` | toggle mouse capture (off lets the terminal select text) |
| `q` | quit (agents keep running) |
| `Q` | suspend every agent and quit, printing a `claude --resume` command for each |

With the mouse on, drag a card onto another column to move it, double-click
a card to attach, drag a column's right edge or the preview's top border to
resize, click the preview to type into the agent, and click the assistant
panel to type into it. Layout is saved in `ui.json` in the project's state
directory.

In live mode (`tab` or a click on the preview) every key goes to the agent,
`esc` and `ctrl+c` included, except `ctrl+z`, which is ignored so it can't stop
the agent. `ctrl+q` returns to the board.

The mouse wheel over the preview scrolls the agent's history, as do `pgup` /
`pgdown` on the board. Typing in live mode jumps back to the bottom.

## Columns

A board is a row of columns defined in `drover.json`. Each card sits in one of
them, and where it sits decides what happens to its agent and worktree. Columns
live in `drover.json` only; `drover.local.json` can't change them.

Every column is one of three kinds:

| Kind | Set with | Agent | Enter automations | Worktree |
| --- | --- | --- | --- | --- |
| **Agent** | `"autoSpawn": true` | started or resumed with the column's settings | every row runs | created on entry if missing |
| **Plain** | neither | none; a running agent is suspended, unless `keepAgent` is set | `send_message` is skipped, `run_script` and `notify` run | kept |
| **Done** | `"role": "done"` | suspended | none | kept until the card is archived |

- **Agent** columns do the work. Moving a card in starts its agent, and a
  card going to Done or to a plain column suspends it. Turn-end automations
  run after each of its turns. A move never removes a worktree or resets the
  conversation; deleting or archiving a card does.
- **The first column** is where new cards land: `drover add`, the assistant,
  Linear issues and imported past sessions. They start no agent. In the default
  board that is Planning, where a card reads `○ not started` until you press
  `enter` or `r` on it, or move it to another agent column. A card whose agent
  was stopped reads `⏸ suspended` or `■ exited` instead, and resumes its
  conversation when started. A card that hasn't started holds no slot toward
  `maxConcurrent`; starting it takes one, suspending the longest-idle agent
  if all are busy or queueing behind them.
- **Plain** columns are for waiting, such as "In review" or "Verify in
  develop". Their automations can ping you with `notify` or run a check with
  `run_script`, for example `go test ./...` as a `conditionScript` on a
  `move_to_column` row. With `keepAgent` set, a running agent stays running when
  a card moves in; the column starts none, and its agent keeps its model, effort
  and permission mode, unless a move into its worktree is already pending. The
  idle timeout and slot evictions skip that agent, though it still counts toward
  `maxConcurrent`.
- **Done** cards can be archived; `archiveDoneAfter` does it automatically.
  See [How it works](#how-it-works).

Each column takes these fields:

| Field | Meaning |
| --- | --- |
| `id` | Unique. Used in `drover.json` targets such as `prFlow` and `planExitTarget`. |
| `name` | Shown on the board. Targets and the CLI also accept it, case-insensitively. |
| `role` | `done`. Leave it out for other columns. A `todo` role from older versions is dropped when the board loads, and its cards move to the first column. |
| `color` | Header color, such as `"#3b82f6"`. |
| `autoSpawn` | Makes an agent column. |
| `keepAgent` | On a plain column: keeps a running agent when a card moves in, instead of suspending it. Not allowed with `autoSpawn` or Done. |
| `permissionMode` | Passed as `claude --permission-mode`. Without it, the board's `defaultPermissionMode`, or Claude Code's own default. |
| `modelOverride` | Passed as `claude --model`. Without it, the board's `defaultModel`, which defaults to `default`: Claude Code's own default model, as in `/model`. |
| `effortOverride` | Passed as `claude --effort`. |
| `planExitTarget` | Where a card goes once its plan is approved. Only meaningful in a column with `permissionMode` `plan`. |
| `automations` | `onEnter` and `onTurnEnd` rows; see [Automations](#automations). |

A model or effort change respawns the agent with `--resume`. A permission
change switches it in place while it is idle, and respawns it when that fails
or the new mode is left to Claude Code. A card keeps one conversation across
every column.

```json
{
  "columns": [
    { "id": "planning", "name": "Planning", "autoSpawn": true, "permissionMode": "plan", "planExitTarget": "Executing" },
    { "id": "executing", "name": "Executing", "autoSpawn": true },
    { "id": "in-review", "name": "In review",
      "automations": { "onEnter": [ { "name": "Ping", "type": "notify", "body": "PR #{{prNumber}} is in review" } ] } },
    { "id": "done", "name": "Done", "role": "done" }
  ]
}
```

With no `drover.json`, the board uses this default, with `prFlow` moving a card
to Waiting for me when an agent is asked to open a PR, to In review once it is
open, and to Done once it merges:

| Column | Kind |
| --- | --- |
| Planning | Agent, in `plan` mode; approved plans go to Executing |
| Executing | Agent |
| Waiting for me | Agent |
| In review | Agent; on entry it runs `/pr-review-toolkit:review-pr` |
| Done | Done |

When an Executing agent finishes a turn and its reply asks you for a decision or input, the card moves to Waiting for me. Each turn end on Executing costs one short Haiku check. Custom boards need the `onTurnEnd` automation on their Executing column.

## How it works

- **Shared board file.** Columns come from `drover.json` at the repo root, so
  you and your teammates share one board. With
  no file it uses a built-in default (Planning, Executing,
  Waiting for me, In review, Done); press `W` to write it out.
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
  of them are archived. `A` asks before deleting uncommitted
  changes, while `C` and the automatic archive keep such a worktree.
- **New cards.** `n` adds a card to the first column and starts its agent in
  the card's own worktree, on that column's settings (the default board uses
  Planning in `plan` mode). Your first message becomes the card's title, which
  Claude Haiku then shortens to a few words unless `autoName` is `false`. The
  branch is named from the card, so a card started with `n` gets `task-<id8>`.
  Leaving the session without typing anything deletes the card, its worktree
  and that unused branch.
- **Worktree per task.** Entering an agent column creates
  `.drover/worktrees/drover-<N>` on branch `<slug>-<id8>` from the base branch.
  Moving a card never removes it: Done suspends the agent and keeps the
  worktree, and archiving the card removes it, asking first if there are
  uncommitted changes. Branches are never deleted, except the unused one of a
  new session left empty.
- **Worktree setup.** `copyFiles` lists paths git doesn't track, such as
  `.env`, to copy from the main checkout into each new worktree. They never
  count as uncommitted changes. `initScript` then runs with `sh -c` in the new
  worktree before the agent starts, for example `pnpm install`. If it fails,
  so does the move, and the next start runs it again.
- **Keeping up with the base branch.** `u` fetches the card's base branch and
  fast-forwards the worktree to it. When the branch has commits of its own,
  a running agent is asked to merge it instead. `u` refuses while the agent
  is working or the worktree has uncommitted changes.
- **Column settings.** Each column's agent settings become Claude Code flags.
  See [Columns](#columns) for what each one does.
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
  except that a start you make suspends the agent that has been idle longest,
  but never one in a `keepAgent` column.
- **`idleTimeoutMinutes`** suspends an agent once it has been idle that long,
  except one in a `keepAgent` column.
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
the rest; `move_to_column` ends the list either way. Nothing runs when a card enters Done.

| Type | Does | Fields |
| --- | --- | --- |
| `send_message` | types `message` into the agent, or queues it until the agent is idle | `message` |
| `run_script` | runs `script` with `sh -c` in the worktree, killed after `timeoutMinutes` (default 10) | `script`, `timeoutMinutes` |
| `notify` | shows a desktop notification and a notice on the board | `title` (default `{{title}}`), `body` (default `{{toColumn}}`) |
| `move_to_column` | moves the card, `onTurnEnd` only | `targetColumn` (default: the next column), `message` (sent after the move) |

- **Columns without an agent** run `run_script` and `notify` rows and skip
  `send_message`.
- **A fresh agent** starts with an `onEnter` group's first `send_message`,
  if it has no `condition` or `conditionScript`, instead of the task prompt, so a
  review column can open with `/code-review`.
- **`condition`** on any row asks `claude -p --model haiku` whether it holds
  for the agent's last reply. The row is skipped unless the answer is yes, and
  if the check itself fails a skipped `move_to_column` raises a notice.
- **`conditionScript`** on any row runs with `sh -c` in the worktree, with the
  same placeholders, `$DROVER_*` variables and `timeoutMinutes` (default 10) as
  `run_script`. The row is skipped unless it exits 0. Exits 1 to 125 are logged
  as not met. Anything else, such as 127 for a missing command, a script that
  can't start or a timeout, is logged as an error, and a skipped
  `move_to_column` then also raises a notice. A row with both a
  `condition` and a `conditionScript` needs both, and the script runs first. It
  runs before a `move_to_column` moves the card, so there `{{toColumn}}` is the
  current column, and only the row's `message` sees the target. To move on only
  when tests pass:
  `{ "type": "move_to_column", "conditionScript": "go test ./... >/dev/null 2>&1" }`.
- **Placeholders** in `message`, `script`, `conditionScript`, `title` and
  `body`: `{{title}}`, `{{description}}`, `{{taskNumber}}`, `{{taskId}}`, `{{branchName}}`,
  `{{baseBranch}}`, `{{worktreePath}}`, `{{projectPath}}`, `{{projectName}}`,
  `{{prUrl}}`, `{{prNumber}}`, `{{prTitle}}`, `{{prBranch}}`, `{{issueKey}}`,
  `{{issueUrl}}`, `{{fromColumn}}`, `{{toColumn}}`, `{{trigger}}` and
  `{{agentReply}}`. In a `script` or `conditionScript` they are shell-quoted,
  and each is also set as an environment variable such as `$DROVER_TASKNUMBER`.

This repo's own board cleans up, simplifies, reviews and builds on entering
In review. At the end of each agent turn it asks for fixes if the review
reported issues, or has the agent open the PR once none remain (only on a card
with no PR yet). The In review column notifies you when the PR is in review.
The column's automations, trimmed to the review and PR steps:

```json
"automations": {
  "onEnter": [
    { "name": "Remove extra comments", "type": "send_message", "message": "Run comment-destroyer subagent unless you already ran it on the current diff." },
    { "name": "Simplify", "type": "send_message", "message": "Run /simplify unless you already ran it on the current diff." },
    { "name": "Review", "type": "send_message", "message": "Run /pr-review-toolkit:review-pr unless you already ran it on the current diff." },
    { "name": "Build", "type": "run_script", "script": "go build ./... && go vet ./..." },
    { "name": "Ping", "type": "notify", "title": "{{title}}", "body": "PR #{{prNumber}} is in review" }
  ],
  "onTurnEnd": [
    { "name": "Apply fixes", "type": "send_message",
      "message": "Apply the fixes the review found, then run go test ./...",
      "condition": "The message lists at least one specific review finding that is still open. Answer NO if it says nothing is left to apply, or lists findings only as skipped, declined, deferred or already fixed" },
    { "name": "Open PR", "type": "send_message",
      "message": "Commit the changes, push the branch and open the PR with gh pr create, then watch its checks.",
      "condition": "The message is a reply to the code review or to applying its fixes, and lists no review findings left to fix. Missing tests and uncommitted changes don't count as findings",
      "conditionScript": "test -z \"$DROVER_PRNUMBER\"" }
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
  worktree goes once every card is archived.
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
  "requested": "waiting-for-me",
  "opened": "in-review",
  "changesRequested": "executing",
  "changesRequestedMode": "propose",
  "merged": "done",
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
  `opened` here and sends the agent a message about the review. In `fix` mode,
  once the agent has pushed a new head and is idle, the card goes back to
  `opened`.
- **changesRequestedMode:** `propose` (the default) has the agent list each
  review issue with the fix it would make and ask you which to apply, without
  changing, pushing or replying. Turn-end automations such as moving to
  Waiting for me run as usual, and the card stays where you leave it. `fix`
  has it address the comments, push and reply on its own. **changesRequestedMessage** replaces the built-in message
  in either mode; a board with a message and no mode keeps fixing.
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

A session that is still in the first column takes the PR on its own card. One
already working on something gets a separate card for it.

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
  confirmed the PR isn't yours, from an agent column.
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
  it, and moves to Done are refused. Once you exit it there, press
  `r` and the board resumes the conversation with `claude --resume` in the
  agent's original directory.
- **Past sessions** land in the first column. Press `enter` or `r` on one, or
  move it into an agent column, to resume it.
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

- Imported issues land in the first column, with no agent. The issue description becomes the prompt.
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
drover ask "which cards are waiting on review?"
```

- Cards are addressed by the number `list` shows (`4`; quote it as `'#4'` if you
  write the `#`). Columns are ids or names. A prompt of `-` is read from stdin.
- Without `--force`, `rm` fails when the card's worktree has uncommitted
  changes. `--force` discards them. The branch is kept.
- `add` puts the card in the first column and starts no agent. `add -c` into
  another agent column starts the agent, like a move. If that fails the card
  stays in the first column and the command says why.
- `add --linear` imports the issue, so the board needs `LINEAR_API_KEY`. The
  title and prompt come from the issue, and one already on the board is refused.
- The open board applies requests one at a time and the command prints the
  result. If the board doesn't answer within two minutes, the command fails but
  the change may still be applied, so run `list` before retrying.
- With no board open, the change is queued and applied the next time one opens.
  Errors then show up on the board, not in your shell. `list` reads the saved
  state, so it shows applied changes only.
- `list`, `add`, `move`, `edit`, `rm` and `ask` are commands, so a directory
  with one of those names needs `./`.
- `list --json` also gives each card's `branch`, `worktree` and `agent` state
  (`not started`, `working`, `idle`, `needs you`, `starting`, `queued`,
  `suspended`, `exited`), left out when empty. Cards that share one agent report the same three.
- `list --json` gives a card's pull request as `pr`: its `url`, `number`,
  `state` (`open`, `merged`, `closed`, or `unknown` until gh has answered),
  `reviewDecision` (`approved` or `changes requested`), `released`, `reviewing`
  for a PR you were asked to review, and `error` when a lookup failed. It is the state the board last saved: it polls open
  pull requests about once a minute while it runs, and never polls a merged or
  closed one again, or a card in Done.

### Asking the board

`:` opens a side panel next to the board that runs the real Claude Code
interface as the board's assistant. It knows the repo, the board's columns, and
every card's column, branch, worktree, agent state and pull request state, and
can move, add, edit and delete cards when you tell it to, and answer questions
about the code and its history. When a card in the column your PRs wait in (In
review by default) has a merged or closed pull request of its own, it offers to move the
card on, to the merged column of your `prFlow` or to Done, and waits for you to
agree. It runs the commands above, so changes go through the open board like
any other.
It takes no agent slot and isn't a card.

- The panel needs a terminal at least 100 columns wide and 8 rows tall. `:`
  focuses it, so every key, `esc` and `ctrl+c` included, goes to Claude.
  `ctrl+q` returns to the board and hides the panel. You can also click the
  panel to focus it, and use the mouse wheel to scroll it.
- It is the `claude --model sonnet` TUI in a session on the private tmux server
  (`tmux -L drover ls`), started in the repo. It stops when the board that
  opened it quits or its terminal closes, and the conversation resumes the next
  time you open the panel.
- One conversation per project. `N` (or the palette's "Start a new assistant
  conversation") opens the panel on a new conversation. Claude keeps the
  conversation itself; drover keeps only its id, in `assistant-session` in the
  project's state directory, so a conversation switched inside Claude with
  `/clear` or `/resume` isn't tracked.

What the assistant may do is the same in the panel and in `drover ask`:

- Besides read-only git (below), it may only run `drover list`, `move`, `add`,
  `edit` and `rm`, and never `--force`. Anything else is denied without asking
  (`dontAsk` mode in the panel). It is told to ask you before deleting a card,
  which removes its worktree. In a one-off `drover ask` you answer that in the
  next question.
- The repo is read-only to it, enforced in the tool permissions: Read, Grep and
  Glob, which Claude limits to the repo, and the git subcommands `status`,
  `log`, `show`, `diff`, `blame`, `ls-files`, `ls-tree`, `shortlog`,
  `rev-parse`, `describe` and `cat-file`. Everything else is refused: no edit
  or write tool, no other shell commands, and no git subcommand that changes
  the repo, a branch or a worktree.
- Asked for a change, it offers a card with a title and prompt and runs
  `drover add` once you agree. That is an instruction to it, not a restriction.
- It ignores your user, project and local Claude settings, so none of your allow
  rules or `env` apply, but CLAUDE.md files still load. Managed settings still
  apply. If they auto-allow sandboxed Bash commands, the permission rules no
  longer limit what it runs; the sandbox does, and drover denies it writes to
  the repo there.
- These limits are on the assistant's tools. In the panel you are typing into a
  real Claude Code session, so what you run yourself (`!` commands, slash
  commands) isn't limited by them.

`drover ask <question>` asks the same assistant from your shell, as a headless
`claude -p` session on the same conversation:

```sh
drover ask "what needs me right now?"
drover ask "move #4 and #7 to In review"
drover ask "where does the card state get saved?"
drover ask --new "start over: what's in Planning?"
```

- It refuses while the board's assistant session is running, which is from the
  first time you open the panel until the board that opened it quits or Claude
  exits (hiding the panel doesn't stop it), since two Claudes must not share a
  conversation. The error names the `tmux -L drover kill-session` command that
  ends the session, for when a board died without quitting. `--new` forgets the
  conversation, but is refused too while the session runs (press `N` on the
  board instead).
- A question of `-` is read from stdin. A question that gets no answer within
  ten minutes fails, and the board may have been partly changed. A saved
  conversation whose transcript is gone starts over by itself. If `claude`
  still can't resume one that has a transcript, every question fails until you
  use `--new`.

## Notes

- Tasks live in `$XDG_STATE_HOME/drover/<repo>-<hash>/state.json`
  (default `~/.local/state`), outside the repo so they can't be committed.
  Every worktree of a repo opens the same board.
- Agents run in a private tmux server (`tmux -L drover ls`) and keep
  running after you quit; the session the panel opened does not, once that
  board quits or its terminal closes. Automations only run while the board is
  open. Events that arrive while it's closed are processed when you reopen it.
- Only Claude Code is supported. Issues import from Linear only.
