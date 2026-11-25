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

Or download a macOS or Linux binary from the
[Releases](https://github.com/aapzu/drover/releases) page. `drover --version`
prints the version.

Install the binary somewhere permanent. Claude's hooks call it to report
activity, so don't run it with `go run`.

### Updating

When the board opens it checks GitHub for a newer release, at most once a day,
and says so in the footer. Press `U` to download it, verify its checksum and
replace the binary, then restart the board to use it. `drover update` does the
same from the shell, and `drover update --check` only reports.

Local builds and `go install` of a commit never check; `go install ...@latest` does, and `U` replaces that binary too. Turn the check off with
`disableUpdateCheck` or `DROVER_NO_UPDATE_CHECK=1`.

## Use

```sh
cd your-repo
drover
```

Then bring in the Claude Code work you already have:

1. Press `I` to import your Claude Code agents and sessions. Running agents
   become cards in the first agent column, and past sessions land in the first
   column, ready to resume. See
   [Importing Claude Code agents](#importing-claude-code-agents). `tab`
   switches to [Linear](#linear) or [GitHub](#github-issues) issues and
   [PRs to review](#importing-prs-to-review).
2. Or press `n` to start a new card with an agent, or `ctrl+n` to start one
   in the [review lane](#the-review-lane).

Press `?` for every shortcut, or `ctrl+k` to search cards (enter opens one full screen) or run a command.

With the mouse on (`M` toggles it), drag a card onto another column to move it, double-click
a card to attach, drag a column's right edge, the preview's top border or the review lane's
divider to resize, click the preview to type into the agent, and click the assistant
panel to type into it. Dragging over the preview or the assistant panel
selects text and copies it, without the panes' borders; double click selects a
word, triple click a line, and dragging after either extends the selection by
words or lines. While you type into an agent that reads mouse reports
(`disableMouse` off), clicks go to it instead. To select text anywhere else with the
terminal's own selection, hold Option on macOS or Shift elsewhere while
dragging. Layout is saved in `ui.json` in the project's state
directory.

In live mode (`tab` or a click on the preview) every key goes to the agent,
`ctrl+c` included, except `ctrl+z`, which is ignored so it can't stop the agent.
`ctrl+q` and `esc` return to the board, and `ctrl+k` opens the command palette.
Interrupt the agent with `ctrl+c`, or attach with `enter` when it needs `esc`.
`shift+enter` inserts a newline in terminals that report it (iTerm2, xterm);
elsewhere use `alt+enter`.

The mouse wheel over the preview scrolls the agent's history, as do `pgup` /
`pgdown` on the board. When the agent has no history and doesn't read mouse
reports (for example with `disableMouse`), the wheel sends it `pgup` /
`pgdown`. Typing in live mode jumps back to the bottom.

## Columns

A board is a row of columns defined in your [config](#where-settings-live).
Each card sits in one of them, and where it sits decides what happens to its
agent and worktree. Each of `columns`, `prFlow` and
`reviewFlow` comes whole from the highest config that sets it. `O` edits column
settings in the config that holds the columns; see
[Settings view](#how-it-works).

Every column is one of three kinds:

| Kind | Set with | Agent | Enter automations | Worktree |
| --- | --- | --- | --- | --- |
| **Agent** | `"autoSpawn": true` | started or resumed with the column's settings | every row runs | created on entry if missing, unless `noWorktree` |
| **Plain** | neither | none; a running agent is suspended, unless `keepAgent` is set | `send_message` is skipped, `run_script` and `notify` run | kept |
| **Done** | `"role": "done"` | suspended | none | kept until the card is archived |

- **Agent** columns do the work. Moving a card in starts its agent, and a
  card going to Done or to a plain column suspends it. Turn-end automations
  run after each of its turns. A move never removes a worktree or resets the
  conversation; deleting or archiving a card does.
- **The first column** is where new cards land: `drover add`, the assistant,
  Linear or GitHub issues and imported past sessions. They start no agent. In the default
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
| `id` | Unique. Used in targets such as `prFlow` and `planExitTarget`. |
| `name` | Shown on the board. Targets and the CLI also accept it, case-insensitively. |
| `lane` | `review` puts the column in the [review lane](#the-review-lane). Not allowed on the first column. |
| `role` | `done`. Leave it out for other columns. A `todo` role from older versions is dropped when the board loads, and its cards move to the first column. |
| `color` | Header color, such as `"#3b82f6"`. |
| `autoSpawn` | Makes an agent column. |
| `keepAgent` | On a plain column: keeps a running agent when a card moves in, instead of suspending it. Not allowed with `autoSpawn` or Done. |
| `permissionMode` | Passed as `claude --permission-mode`. Without it, the board's `defaultPermissionMode`, or Claude Code's own default. |
| `modelOverride` | Passed as `claude --model`. Without it, the board's `defaultModel`, which defaults to `default`: Claude Code's own default model, as in `/model`. |
| `effortOverride` | Passed as `claude --effort`. |
| `planExitTarget` | Where a card goes once its plan is approved. Only meaningful in a column with `permissionMode` `plan`. |
| `noWorktree` | On an agent column: runs the agent in the project root until the card has a worktree. The default Planning column sets it, so a new session starts without waiting for one. Moving the card into an agent column without it creates the worktree and resumes the conversation there (for a review-lane column, in the background; see [The review lane](#the-review-lane)). Use it only for agents that don't edit files, such as plan mode. |
| `automations` | `onEnter` and `onTurnEnd` rows; see [Automations](#automations). |

A model or effort change respawns the agent with `--resume`. A permission
change switches it in place while it is idle, and respawns it when that fails
or the new mode is left to Claude Code. A card keeps one conversation across
every column.

```json
{
  "columns": [
    { "id": "planning", "name": "Planning", "autoSpawn": true, "permissionMode": "plan", "planExitTarget": "Executing", "noWorktree": true },
    { "id": "executing", "name": "Executing", "autoSpawn": true },
    { "id": "in-review", "name": "In review",
      "automations": { "onEnter": [ { "name": "Ping", "type": "notify", "body": "PR #{{prNumber}} is in review" } ] } },
    { "id": "done", "name": "Done", "role": "done" }
  ]
}
```

With no columns in either config, the board uses this default, with `prFlow`
moving a card to Waiting for me when an agent is asked to open a PR, to In
review once it is open, and to Done once it merges:

| Column | Kind |
| --- | --- |
| Planning | Agent, in `plan` mode; approved plans go to Executing |
| Executing | Agent |
| Waiting for me | Agent |
| In review | Agent; on entry it runs `/pr-review-toolkit:review-pr` |
| Done | Done |
| Review requested | Plain, in the review lane; [imported](#importing-prs-to-review) PRs awaiting your review land here |
| Reviewing | Agent, in the review lane, at `high` effort; on entry after a review of yours it asks for a re-review of the new commits |
| Triage findings | Agent, in the review lane |
| Waiting on author | Plain, in the review lane; the agent is suspended |
| Done | Done, in the review lane |

The last five are the [review lane](#the-review-lane) and come with a
`reviewFlow` that moves the cards of PRs you review through them.

When an Executing agent finishes a turn and its reply asks you for a decision or input, or hands the work back for you to review, the card moves to Waiting for me. Each turn end on Executing costs one short Haiku check. A custom board gets this row by default when its `executing` column has no `onTurnEnd` and the board has a column with id or name `waiting-for-me`, `Waiting for me` or `waiting`; set `"onTurnEnd": []` to turn it off.

## How it works

- **Settings live outside the repo.** Only the one-time import of old
  `drover.json` files reads the repo. See
  [Where settings live](#where-settings-live). With no columns in either
  config the board uses a built-in default (Planning, Executing, Waiting for
  me, In review, Done, and the review lane); press `W` to write it to the
  project config. `worktreesDir` (default `.drover/worktrees`) is where
  worktrees go, relative to the repo root or absolute, e.g. `.claude/worktrees`.
  `splitByPR`, `teamReviewRequests`, `reviewRepos` (see
  [Importing PRs to review](#importing-prs-to-review)) and those under
  [Agents and notifications](#agents-and-notifications) work in either config.
- **Archiving Done.** `archiveDoneAfter` (e.g. `"7d"`, `"36h"`) hides cards
  that have sat in Done that long. Archived cards stay in the state file and
  the column header shows how many are hidden (`Done 4 +37`). The built-in
  board uses `7d`; with the setting missing from a board you wrote nothing is
  archived automatically. Archiving
  removes the card's worktree; a worktree shared by several cards goes once all
  of them are archived. `A` asks before deleting uncommitted
  changes, while `C` and the automatic archive keep such a worktree.
- **Card order.** A column lists the card that entered it last first. `P` pins
  the selected card above the rest (`⚑`) and unpins it again. The pin stays
  when the card moves.
- **Settings view.** `o` changes the settings. Each row shows where its value
  comes from: `project`, `global` or `default`. Enter edits, Tab picks the
  global or the project config, and `x` removes the override. `/` searches the
  options by name or JSON key (esc clears the search). Per-repo options
  (`worktreesDir`, `defaultBaseBranch`, `initScript`, `linkUntracked`) default
  to the project config and the rest to the global one. `prFlow` options edit
  the config that holds the columns. The `Columns…` row at the bottom, or `c`
  (`O` on the board), opens a column's settings: pick a column, then its name,
  color, permission mode, model, effort, `autoSpawn`, `keepAgent`, opening
  prompt, `planExitTarget` or `noWorktree`. These save to the config that holds
  the columns. If neither does, the first edit copies the default board into
  the global config, so press `W` first to keep it per repo. The opening prompt
  is the first `onEnter` row, when it is a `send_message` without a condition:
  Enter edits it in a multi-line box (Ctrl+S saves, an empty prompt removes the
  row) and adds the row if the column has none. Columns can't be added, removed
  or reordered there, and other automations are edited by hand in that file.
- **New cards.** `n` adds a card to the first column and starts its agent on
  that column's settings (the default board uses Planning in `plan` mode), in
  the project root when the column sets `noWorktree`, as Planning does by
  default, and otherwise in the card's own worktree. Your first message becomes
  the card's title, which Claude Haiku then shortens to a few words unless
  `autoName` is `false`. The
  branch is named from the card when its worktree is created, so a card that
  still has no title then gets `task-<id8>`. Leaving the session without typing
  anything deletes the card, and its worktree and that unused branch if it has
  any.
- **Worktree per task.** Entering an agent column without `noWorktree` creates
  `.drover/worktrees/drover-<N>` on branch `<slug>-<id8>` from the base branch.
  Moving a card never removes it: Done suspends the agent and keeps the
  worktree, and archiving the card removes it, asking first if there are
  uncommitted changes. Branches are never deleted, except the unused one of a
  new session left empty.
- **Worktree setup.** With `"linkUntracked": true`, each new worktree gets
  symlinks to what git doesn't track in the main checkout, gitignored files
  such as `.env` included, so edits show in both places. `linkFiles` limits
  that to the paths it lists, and then `linkExclude` is ignored; otherwise
  everything is linked except the paths in `linkExclude`. `.claude`,
  `.drover`, the worktrees directory, every `node_modules` and any directory
  holding a `.git`, such as another tool's worktree, are never linked. The
  links never count as uncommitted changes, but links to files git doesn't
  ignore show as untracked there, so stage by path rather than with
  `git add -A`. Build output and caches are linked too, so a build in one
  worktree writes into the main checkout; list them in `linkExclude`.
  `initScript` then runs with `sh -c` in the new worktree before the agent
  starts, for example `pnpm i`. If it fails, so does the move, and the next
  start runs it again. Moving an agent that is already working in the project
  root into a review-lane column is the exception: the move doesn't wait for
  setup and succeeds even if it fails; see [The review lane](#the-review-lane).
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
  transcript. Cards show how full the agent's context is, like `42%`. Press
  `$` for each card's estimated cost at list prices and the board's total;
  prices are shown nowhere else.
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

These settings work in the global and the project config. Both files are
reloaded while the board runs.

```json
{
  "maxConcurrent": 8,
  "idleTimeoutMinutes": 0,
  "notifications": { "turnDone": true, "needsYou": true, "planReady": true, "exited": true, "cooldownSeconds": 10 },
  "autoName": true,
  "disableMouse": false,
  "disableUpdateCheck": false
}
```

- **`maxConcurrent`** (default 8) caps running agents. Further starts queue,
  except that a start you make suspends the agent that has been idle longest,
  but never one in a `keepAgent` column or one with runs scheduled.
- **`idleTimeoutMinutes`** suspends an agent once it has been idle that long,
  except one in a `keepAgent` column or one with runs scheduled (see
  [Polling agents](#polling-agents)). `r` resumes it. 0, the default, never does.
- **`notifications`** shows a desktop notification and a notice on the board
  when an agent finishes a turn and stays idle, asks for a permission, has a
  plan for you to approve, or exits by itself. Each is on by default and fires
  at most once per `cooldownSeconds` for each agent.
- The agent you're attached to or typing into neither notifies you nor times
  out.
- **`autoName`** titles a new session's card with a short summary of your
  first message, written with `claude -p --model haiku`. A title you edit is
  never replaced.
- `E` renames the selected card: the input starts with its current title and
  swaps in a suggestion made the same way as `autoName`'s, unless you've started
  typing. It works whether or not `autoName` is on.
- **`disableMouse`** (default `false`) starts agents and the assistant with
  `CLAUDE_CODE_DISABLE_MOUSE=1` and turns tmux's mouse mode off in their
  sessions, so your terminal handles selection and scrolling when you attach.
  It applies to sessions started or resumed after you change it.
- **`disableUpdateCheck`** (default `false`) stops the board from checking
  GitHub for a newer release when it opens. See [Updating](#updating).
- **`issueTracker`** (`"linear"`, the default, or `"github"`) picks the issue
  tracker the import view lists. See [Linear](#linear) and
  [GitHub issues](#github-issues).
- **`assistantPlugins`** and **`assistantTools`** (both empty by default) give
  the assistant MCP tools, such as Slack's, so you can ask it to read a source and
  turn what it finds into cards. `assistantPlugins` lists Claude Code plugin ids
  to enable, like `"slack@claude-plugins-official"`; `assistantTools` lists the
  tool patterns it may use without asking, each starting with `mcp__`, like
  `"mcp__plugin_slack_slack__slack_read_*"`. Allow only read tools unless you
  want it to write. A value in the project config replaces the global one.
  What those tools return can come from anyone, so the assistant is told to
  treat it as data and to ask before adding cards from it. An enabled plugin
  runs as a whole, hooks included, and those aren't limited by the
  assistant's tool rules, so list only plugins you trust.

### Polling agents

An agent that sets up its own runs, with `/loop`, `ScheduleWakeup` or
`CronCreate`, is polling. The board reads those calls from the agent's hooks and
shows the wait instead of plain idle:

- The card shows `⏲ in 4m12s`, the time to the agent's next run, and `+N` when
  it has more runs scheduled. The header counts polling agents apart from idle
  ones.
- `i` lists the card's runs: when, wakeup or cron, how often it ran, and why.
- `T` opens a timeline with a row for each polling card and a mark where each
  run falls in the next hour, six hours or day (`←` `→` change the span, `enter`
  goes to the card).
- An agent with runs scheduled is not suspended by `idleTimeoutMinutes` or to
  free a slot, because Claude Code loses its runs when the agent stops. They are
  dropped from the board when the agent stops running.

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
  `{{prUrl}}`, `{{prNumber}}`, `{{prTitle}}`, `{{prBranch}}`,
  `{{prReviewedSha}}` (the commit your latest review of the PR was on, empty
  before you review it), `{{issueKey}}`,
  `{{issueUrl}}`, `{{fromColumn}}`, `{{toColumn}}`, `{{trigger}}` and
  `{{agentReply}}`. In a `script` or `conditionScript` they are shell-quoted,
  and each is also set as an environment variable such as `$DROVER_TASKNUMBER`.

This repo's own board cleans up, simplifies, reviews and builds on entering
In review. At the end of each agent turn it asks for fixes if the review
reported issues, or has the agent open the PR once none remain (only on a card
with no PR yet). It also asks the agent to merge the base branch and resolve
the conflicts when the PR has any. The In review column notifies you when the
PR is in review.
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
    { "name": "Fix conflicts", "type": "send_message",
      "message": "PR #{{prNumber}} has merge conflicts with its base branch. Fetch the base branch, merge it into this branch, resolve the conflicts, run go build ./... and go test ./..., then push (no force push).",
      "conditionScript": "test -n \"$DROVER_PRNUMBER\" && test \"$(gh pr view \"$DROVER_PRNUMBER\" --json mergeable --jq .mergeable)\" = CONFLICTING" },
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
agent's turn ends. Every further PR gets its own card (unless `splitByPR` is
off), so one session can ship several. The cards are marked `⧉ 1/3`, `⧉ 2/3`
and so on, and the marker has the same color on every card of the group. `f`
folds the current column to one card per session (`⧉ 1/3 +2` stands for two
more in that column), so a pile of merged PRs doesn't fill Done.

- The cards share one agent, worktree and conversation. `enter`, `tab`, `s`
  and the preview all reach the same agent from any of them.
- Moving a card into an agent column points the agent at that card: it takes
  that column's settings and runs its automations with the card's PR. Turn-end
  automations act only on the card the agent is working for.
- The agent stops once no card in the group is in an agent column. The
  worktree goes once every card is archived.
- Deleting a card removes only that card while others remain.
- **`splitByPR`** (default `true`): `false` in either config stops further
  PRs, opened or sent for review, from getting cards. Only the first stays on the session's own card; the rest are noted in
  its log and not tracked. A review you ask for in a session that has moved on
  from the first column gets no card at all. Existing split cards stay.
- `{{prUrl}}`, `{{prNumber}}`, `{{prTitle}}` and `{{prBranch}}` work in
  automations. `{{branchName}}` is the PR's branch when known.
- `gh pr view` refreshes every open PR's card once a minute. Cards
  show `✓ approved`, `✗ changes`, `⇉ queued` (in the merge queue), `merged`
  and `released`.

### Following a PR to release

`prFlow` moves PR cards as their PR progresses. Targets are
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
  has it address the comments, push and reply on its own. `off` leaves the
  card and the agent alone; the review is only logged and notified. The
  Settings view (`o`) cycles the mode.
  **changesRequestedMessage** replaces the built-in message in either mode; a
  board with a message and no mode keeps fixing.
- **merged:** where a merged PR card goes, for example to verify it in a
  develop environment.
- A PR closed without merging moves a card in `opened` to Done, if the board
  has a Done column.
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

A card still in the first column whose agent is told to review with a slash
command that names no PR, like `/code-review`, moves to `reviewFlow`'s
`started` column, else the review lane's first column, as soon as the prompt is
sent.

A session that is still in the first column takes the PR on its own card. One
already working on something gets a separate card for it.

drover itself never posts a review. Nothing stops the agent from posting if you
ask it to, for example with `--comment`.

The card shows `review PR #12 @author` once gh has confirmed it, and
`✓ you approved`, `✗ you requested changes` or `✓ you reviewed` once you have
reviewed it. `↻ new commits` replaces that when the author has pushed since.
`reviewFlow` moves the card as the PR changes, with the
same column targets as `prFlow`:

```json
"reviewFlow": {
  "requested": "review-requested",
  "started": "reviewing",
  "reviewed": "waiting-on-author",
  "approved": "review-done",
  "updated": "reviewing",
  "done": "review-done"
}
```

- **requested:** where an [imported](#importing-prs-to-review) PR awaiting
  your review lands. Without it, imports land in the lane's first column
  without an agent, else in the first column. A card in `reviewed` or
  `approved` also comes back here when your review is requested again.
- **started:** where the card goes once the agent's turn ends and gh has
  confirmed the PR isn't yours, from an agent column.
- **reviewed:** where it goes, from any column, when a review of yours shows up
  on the PR once the agent is idle. A review already there when drover first
  sees the PR is only recorded.
- **approved:** where it goes instead of `reviewed` when your review approves
  the PR. A review card in a Done column is still refreshed while its PR is
  open and you have reviewed it, so a push or a new request brings it back,
  until it is archived.
- **updated:** you get a notice when the author pushes after your review. A
  card in `reviewed` or `approved` also moves here, as it does when your
  approval is dismissed or the PR gets merge conflicts after you approved it.
- **done:** where it goes once the PR merges or closes. A PR that was already
  merged or closed when drover first sees it moves nothing.

Your own review is never sent to the agent as feedback to address, and review
cards don't follow a PR to release.

### Importing PRs to review

`I`, then `tab` to Reviews, lists open PRs by others in this repo in two
groups:

- **Requested from me:** PRs whose review is requested from you personally
  (`user-review-requested:@me`). They land in `reviewFlow.requested`.
- **Reviewed by me, still open:** PRs you have reviewed (`--reviewed-by=@me`).
  They land in `reviewFlow.reviewed`.

A PR in both is listed once, as requested. PRs already on the board are left
out. `/` fetches any open PR by URL, from any repo.

- Importing starts no agent. The prompt is `/code-review <url>`, so `enter`,
  `r`, or moving the card into an agent column, reviews the PR. Once that
  first turn ends, the card moves to `reviewFlow.started`, also from the
  requested column.
- Starting it on the board first asks for the `/code-review` level (`low` to
  `max`, `high` preselected), which goes into the prompt as
  `/code-review <level> <url>`. `esc` cancels the start. A card started any
  other way, such as by `reviewFlow` or `drover move`, runs without a level.
- The card then follows `reviewFlow` like any review card. A review of yours
  already on the PR is only recorded, so the card stays where it landed.
- **`teamReviewRequests`** (default `false`): `true` in either config also
  lists PRs requested from your teams
  (`--review-requested=@me`). With CODEOWNERS that can be many.
- **`reviewRepos`**: more repos to search, as `owner/name`, for example
  `["acme/infra"]`. A value in the project config replaces the global one.
  The agent still runs in this repo; `/code-review` reads the PR from GitHub.

### The review lane

Reviewing is a loop that waits on someone else, so the default board keeps it in
a lane of its own: a second row of columns below the main ones. Give a column
`"lane": "review"` to put it there; the first column can't be in it. The lane
shows while it holds cards. `R` hides it or shows it again and remembers the
choice per repo; the header counts the cards in a hidden lane. `h` and `l` walk
from Done into the lane, and skip it while it is hidden. `H` and `L` move a
card within its lane only; use `m` to move it between them. Drag the divider
above the lane to resize it; `0` resets the layout. `X` hides the main columns so
only the review lane shows, and remembers it per repo too; the header counts the
hidden cards. `X` brings the main columns back; `R` does too, and hides the
review lane.

The default lane is Review requested, Reviewing, Triage findings, Waiting on
author and its own Done (id `review-done`), with a `reviewFlow` that lands
imported PRs (`requested`) in Review requested, sends `started` to Triage
findings, `reviewed` to Waiting on author (`approved` goes to the lane's Done
instead when you approve), `updated` back to Reviewing when the author pushes,
your approval is dismissed or the PR gets conflicts, and `done` to the lane's
Done. Reviewing sends the card on to Triage findings once a re-review turn
ends. A board you
define keeps its own columns; add the lane columns and `reviewFlow` to
get it, or press `W` to write out the default board.

Moving a card whose agent is already working in the project root, such as one
from Planning, into the lane doesn't wait for its worktree: the move returns at
once and the agent keeps working in the root while the worktree and
`initScript` run, so anything it edits meanwhile lands in the main checkout.
The agent then resumes in the worktree with the lane column's settings, right
away if it is idle and when its turn ends otherwise. If the setup fails, the
agent stays in the root and the next move into a worktree column builds it
again.

`ctrl+n` starts a card with an agent straight in the lane, in its first
agent column, and shows the lane if you had hidden it. `drover add --review`
does the same from the [command line](#command-line).

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
  `Q` (suspend every agent and quit) prints, are tracked the same way. The board won't start them again
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

## GitHub issues

Set `issueTracker` to `"github"` (in the settings view, `o`, or in a
[config file](#where-settings-live)) to import GitHub issues instead of
Linear ones. It needs the GitHub CLI signed in (`gh auth login`). Then `I`,
followed by `tab`, lists the open issues in this repository that are assigned
to you. `/` fetches any issue by number (`123` or `#123`) or URL.

- Imported issues land in the first column, with no agent. The issue body becomes the prompt.
- The card shows the issue as `GH-123`, which `{{issueKey}}` also gives in
  automations along with `{{issueUrl}}`.
- The branch is named from the title, as for any card, and nothing is synced
  back to the issue.

## Command line

Scripts, hooks and agents can change the board from a shell. Run these from
the repo or any of its worktrees:

```sh
drover list [--json]
drover add "Fix the login bug" -p "Steps to reproduce…" -c planning
drover add --linear DX-123
drover add "Review PR 123" --review
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
  stays in the first column and the command says why. `add --review` is `-c`
  with the review lane's entry column; it can't be combined with `-c`.
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
  `reviewDecision` (`approved` or `changes requested`), `inMergeQueue`,
  `released`, `reviewing`
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
It also knows drover's own docs, so you can ask it how to do something ("how do
I hide a card?") and it names the feature and its key.
It takes no agent slot and isn't a card, so `idleTimeoutMinutes` and slot
evictions never suspend it.

- The panel needs a terminal at least 100 columns wide and 8 rows tall. `:`
  focuses it, so every key, `ctrl+c` included, goes to Claude except `ctrl+q`
  and `esc`, which return to the board and hide the panel, and `ctrl+k`, which
  opens the command palette over it. You can also click the
  panel to focus it, and use the mouse wheel to scroll it.
- From the board, `Z` (or a click on the panel's top border) makes the panel
  fill the whole screen. Click the border again or press `ctrl+q` or `esc` to put it
  back beside the board; `ctrl+q` or `esc` once more hides it. Fullscreen needs only
  40 columns and the same 8 rows.
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

- Besides read-only git (below) and `gh`, it may only run `drover list`,
  `move`, `add`, `edit` and `rm`, and never `--force`. Anything else is denied
  in `drover ask`, and the panel asks you first. It is told to ask you before
  deleting a card, which removes its worktree. In a one-off `drover ask` you
  answer that in the next question.
- Apart from `gh` (below), the repo is read-only to it, enforced in the tool
  permissions: Read, Grep and Glob, which Claude limits to the repo, and the
  git subcommands `status`, `log`, `show`, `diff`, `blame`, `ls-files`,
  `ls-tree`, `shortlog`, `rev-parse`, `describe` and `cat-file`. Everything
  else is refused in `drover ask`, and the panel asks you first: other shell
  commands, git subcommands that change the repo, a branch or a worktree, and
  reads outside the repo. The edit and write tools are denied in both.
- `gh` is allowed without prompting: it reads live pull request state, checks,
  reviews, runs and issues, and could also merge, comment or close. It is told
  to change GitHub only when you ask, but nothing enforces it. Its sandbox
  allows `api.github.com`, `github.com` and `*.githubusercontent.com`; a
  `deniedHosts` entry in managed settings still wins over that. The `gh`
  commands that change the local checkout or run programs (`pr checkout`,
  `repo clone` and `sync`, `run download`, `alias`, `extension`, `config`,
  `pr merge --delete-branch`) are denied.
- Asked for a change, it offers a card with a title and prompt and runs
  `drover add` once you agree. That is an instruction to it, not a restriction.
- It ignores your user, project and local Claude settings, so none of your allow
  rules or `env` apply, but CLAUDE.md files still load. Only the plugins and
  tools in `assistantPlugins` and `assistantTools` are added. Managed settings still
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

## Where settings live

Settings are personal, so drover keeps them outside the repo, in two JSON files
that share one schema:

| Config | Path | Applies to |
| --- | --- | --- |
| Global | `$XDG_CONFIG_HOME/drover/config.json` (default `~/.config/drover/config.json`) | every repo |
| Project | `config.json` in the repo's state directory, `$XDG_STATE_HOME/drover/<repo>-<hash>/` (default `~/.local/state`) | one repo |

The project config wins over the global one, which wins over the built-in
default. A key a file leaves out keeps the value from below; `columns`,
`prFlow` and `reviewFlow` are taken whole from the highest config that sets
them. The project directory is keyed by the repo's absolute path, so moving
the repo leaves its project config behind, as it does its tasks.

Older versions read `drover.json` and `drover.local.json` from the repo root.
The first time a repo with either opens and has no project config, drover
copies them into it: `drover.json` as is, then `drover.local.json` on top.
The repo files are never read again or deleted; remove them yourself.

## Notes

- Tasks live in `$XDG_STATE_HOME/drover/<repo>-<hash>/state.json`
  (default `~/.local/state`), outside the repo so they can't be committed.
  Every worktree of a repo opens the same board.
- Agents run in a private tmux server (`tmux -L drover ls`) and keep
  running after you quit; the session the panel opened does not, once that
  board quits or its terminal closes. Automations only run while the board is
  open. Events that arrive while it's closed are processed when you reopen it.
- Only Claude Code is supported. Issues import from Linear or GitHub, as `issueTracker` says.

## Releases

Every pull request merged to `main` publishes a release with binaries. Label
each PR with exactly one of `patch`, `minor`, `major` or `no-release` (a CI
check requires it); `no-release` skips the release.

Add a bullet under `## Unreleased` in [CHANGELOG.md](CHANGELOG.md) in each PR
that isn't labelled `no-release`. The release moves it under the new version;
if Unreleased is empty it lists the merged pull request titles instead, so
write PR titles for readers.
