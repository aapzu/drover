# Changelog

Add a bullet under Unreleased in every pull request that isn't labelled no-release. The release moves it under the new version, or lists the merged pull request titles if Unreleased is empty.

## Unreleased

## [0.20.2](https://github.com/aapzu/drover/compare/v0.20.1...v0.20.2) - 2026-11-27

- Installing an update with `U` now offers to restart the board on the new version

## [0.20.1](https://github.com/aapzu/drover/compare/v0.20.0...v0.20.1) - 2026-11-26

- Turn off Claude Code's agent view in drover agents, so ← on an empty prompt no longer leaves the card's session

## [0.20.0](https://github.com/aapzu/drover/compare/v0.19.2...v0.20.0) - 2026-11-26

- Add quick tasks: agent sessions with no card, column or PR, opened with `w`; the Quick tasks run in setting picks a worktree or the main checkout

## [0.19.2](https://github.com/aapzu/drover/compare/v0.19.1...v0.19.2) - 2026-11-25

- Fix the import view overflowing the screen when there are many sessions, issues or PRs; it now scrolls with the cursor

## [0.19.1](https://github.com/aapzu/drover/compare/v0.19.0...v0.19.1) - 2026-11-25

- ctrl+k card search also finds cards in a lane the board is not showing, and opens that lane when you pick one
- Review cards sent back to Reviewing move on to Triage findings after the re-review, also on boards saved before that existed
- Import GitHub issues from the import view; the new Issue tracker setting switches it between Linear and GitHub
- The board checks for a newer release when it opens and offers to install it with `U`; `drover update` does the same from the shell
- Settings now live in `~/.config/drover/config.json` and a per-repo config in the state directory instead of `drover.json` and `drover.local.json`; old repo files are imported once
- A new card whose first prompt is a review command with no PR, like `/code-review`, moves to the review lane

## [0.15.3](https://github.com/aapzu/drover/compare/v0.15.2...v0.15.3) - 2026-11-24

- Search more card fields in the board filter ([#175](https://github.com/aapzu/drover/pull/175))

## [0.15.2](https://github.com/aapzu/drover/compare/v0.15.1...v0.15.2) - 2026-11-24

- Send esc and ctrl+k to the board from live mode ([#176](https://github.com/aapzu/drover/pull/176))

## [0.15.1](https://github.com/aapzu/drover/compare/v0.15.0...v0.15.1) - 2026-11-24

- Audit tests: drop restating and duplicate tests, cover untested behaviour ([#174](https://github.com/aapzu/drover/pull/174))

## [0.15.0](https://github.com/aapzu/drover/compare/v0.14.2...v0.15.0) - 2026-11-24

- Search cards from the ctrl+k palette ([#173](https://github.com/aapzu/drover/pull/173))

## [0.14.2](https://github.com/aapzu/drover/compare/v0.14.1...v0.14.2) - 2026-11-24

- Show the hidden review lane while a filter only matches there ([#172](https://github.com/aapzu/drover/pull/172))

## [0.14.1](https://github.com/aapzu/drover/compare/v0.14.0...v0.14.1) - 2026-11-23

- Treat a Stop-hook loop as the agent waiting for you ([#171](https://github.com/aapzu/drover/pull/171))

## [0.14.0](https://github.com/aapzu/drover/compare/v0.13.1...v0.14.0) - 2026-11-23

- Pin cards to the top of a column, list newest first ([#170](https://github.com/aapzu/drover/pull/170))

## [0.13.1](https://github.com/aapzu/drover/compare/v0.13.0...v0.13.1) - 2026-11-23

- Stop the agent when its turn ends if its card is moved mid-turn ([#169](https://github.com/aapzu/drover/pull/169))

## [0.13.0](https://github.com/aapzu/drover/compare/v0.12.6...v0.13.0) - 2026-11-19

- Send approved review cards back to Reviewing on dismissal or conflicts ([#168](https://github.com/aapzu/drover/pull/168))

## [0.12.6](https://github.com/aapzu/drover/compare/v0.12.5...v0.12.6) - 2026-11-19

- Build the worktree in the background when a running agent enters the review lane ([#165](https://github.com/aapzu/drover/pull/165))

## [0.12.5](https://github.com/aapzu/drover/compare/v0.12.4...v0.12.5) - 2026-11-19

- Remove the doubled caret in the filter and send bars ([#167](https://github.com/aapzu/drover/pull/167))

## [0.12.4](https://github.com/aapzu/drover/compare/v0.12.3...v0.12.4) - 2026-11-19

- Wait for the agent's screen to settle before sending a message ([#166](https://github.com/aapzu/drover/pull/166))

## [0.12.3](https://github.com/aapzu/drover/compare/v0.12.2...v0.12.3) - 2026-11-19

- Make shift+enter insert a newline in the live view ([#164](https://github.com/aapzu/drover/pull/164))

## [0.12.2](https://github.com/aapzu/drover/compare/v0.12.1...v0.12.2) - 2026-11-17

- Add a changelog that the release workflow keeps up to date ([#163](https://github.com/aapzu/drover/pull/163))

## [0.12.1](https://github.com/aapzu/drover/compare/v0.12.0...v0.12.1) - 2026-11-17

- Start new review cards in the review lane's first agent column ([#162](https://github.com/aapzu/drover/pull/162))

## [0.12.0](https://github.com/aapzu/drover/compare/v0.11.6...v0.12.0) - 2026-11-16

- Import open PRs you review into the review lane ([#159](https://github.com/aapzu/drover/pull/159))

## [0.11.6](https://github.com/aapzu/drover/compare/v0.11.5...v0.11.6) - 2026-11-16

- Hide prices from the board; show cost in a usage view ([#160](https://github.com/aapzu/drover/pull/160))

## [0.11.5](https://github.com/aapzu/drover/compare/v0.11.4...v0.11.5) - 2026-11-16

- Test reviewFlow.approved with its own column ([#161](https://github.com/aapzu/drover/pull/161))

## [0.11.4](https://github.com/aapzu/drover/compare/v0.11.3...v0.11.4) - 2026-11-16

- Add a rename command that suggests a title ([#158](https://github.com/aapzu/drover/pull/158))

## [0.11.3](https://github.com/aapzu/drover/compare/v0.11.2...v0.11.3) - 2026-11-16

- Show the review lane while ctrl+n starts a review ([#157](https://github.com/aapzu/drover/pull/157))

## [0.11.2](https://github.com/aapzu/drover/compare/v0.11.1...v0.11.2) - 2026-11-16

- Move PRs you approve to Done ([#154](https://github.com/aapzu/drover/pull/154))

## [0.11.1](https://github.com/aapzu/drover/compare/v0.11.0...v0.11.1) - 2026-11-16

- Add X key to hide the main lane ([#156](https://github.com/aapzu/drover/pull/156))

## [0.11.0](https://github.com/aapzu/drover/compare/v0.10.2...v0.11.0) - 2026-11-16

- Track and show the runs agents schedule for themselves ([#155](https://github.com/aapzu/drover/pull/155))

## [0.10.2](https://github.com/aapzu/drover/compare/v0.10.1...v0.10.2) - 2026-11-16

- Make the assistant pane fullscreenable ([#148](https://github.com/aapzu/drover/pull/148))

## [0.10.1](https://github.com/aapzu/drover/compare/v0.10.0...v0.10.1) - 2026-11-16

- Add cards straight to the review lane ([#151](https://github.com/aapzu/drover/pull/151))

## [0.10.0](https://github.com/aapzu/drover/compare/v0.9.5...v0.10.0) - 2026-11-16

- Let the board assistant use MCP plugin tools ([#152](https://github.com/aapzu/drover/pull/152))

## [0.9.5](https://github.com/aapzu/drover/compare/v0.9.4...v0.9.5) - 2026-11-16

- Have the In review column fix PR merge conflicts ([#153](https://github.com/aapzu/drover/pull/153))
- Select text in the assistant panel, with double and triple click ([#149](https://github.com/aapzu/drover/pull/149))

## [0.9.4](https://github.com/aapzu/drover/compare/v0.9.3...v0.9.4) - 2026-11-16

- Add a Done column to the review lane ([#150](https://github.com/aapzu/drover/pull/150))

## [0.9.3](https://github.com/aapzu/drover/compare/v0.9.2...v0.9.3) - 2026-11-16

- Let the assistant's gh reach GitHub and the panel ask about unlisted commands ([#147](https://github.com/aapzu/drover/pull/147))

## [0.9.2](https://github.com/aapzu/drover/compare/v0.9.1...v0.9.2) - 2026-11-13

- Let the rightmost column narrow when every column is resized ([#146](https://github.com/aapzu/drover/pull/146))

## [0.9.1](https://github.com/aapzu/drover/compare/v0.9.0...v0.9.1) - 2026-11-13

- Turn tmux's mouse mode off when disableMouse is set ([#145](https://github.com/aapzu/drover/pull/145))

## [0.9.0](https://github.com/aapzu/drover/compare/v0.8.3...v0.9.0) - 2026-11-13

- Let the board assistant run gh ([#143](https://github.com/aapzu/drover/pull/143))

## [0.8.3](https://github.com/aapzu/drover/compare/v0.8.2...v0.8.3) - 2026-11-13

- Move ClaudeEnv so dropTodo keeps its doc comment ([#144](https://github.com/aapzu/drover/pull/144))
- Search the settings with / ([#138](https://github.com/aapzu/drover/pull/138))

## [0.8.2](https://github.com/aapzu/drover/compare/v0.8.1...v0.8.2) - 2026-11-13

- Ignore background agent notifications when detecting PR requests ([#142](https://github.com/aapzu/drover/pull/142))

## [0.8.1](https://github.com/aapzu/drover/compare/v0.8.0...v0.8.1) - 2026-11-13

- Page live agents with the mouse wheel when nothing else can scroll ([#140](https://github.com/aapzu/drover/pull/140))
- Make the review lane resizable ([#139](https://github.com/aapzu/drover/pull/139))

## [0.8.0](https://github.com/aapzu/drover/compare/v0.7.1...v0.8.0) - 2026-11-13

- Edit a column's opening prompt in the columns view ([#141](https://github.com/aapzu/drover/pull/141))

## [0.7.1](https://github.com/aapzu/drover/compare/v0.7.0...v0.7.1) - 2026-11-13

- Show the cursor in the assistant panel ([#137](https://github.com/aapzu/drover/pull/137))

## [0.7.0](https://github.com/aapzu/drover/compare/v0.6.0...v0.7.0) - 2026-11-12

- Let the assistant answer how-to questions from drover's own docs ([#136](https://github.com/aapzu/drover/pull/136))

## [0.6.0](https://github.com/aapzu/drover/compare/v0.5.0...v0.6.0) - 2026-11-12

- Add disableMouse option for claude sessions ([#135](https://github.com/aapzu/drover/pull/135))

## [0.5.0](https://github.com/aapzu/drover/compare/v0.4.0...v0.5.0) - 2026-11-12

- Add a hideable review lane for PRs you review ([#134](https://github.com/aapzu/drover/pull/134))

## [0.4.0](https://github.com/aapzu/drover/compare/v0.3.1...v0.4.0) - 2026-11-12

- Forward focused assistant panel clicks to claude ([#132](https://github.com/aapzu/drover/pull/132))

## [0.3.1](https://github.com/aapzu/drover/compare/v0.3.0...v0.3.1) - 2026-11-11

- Move a card to Done when its PR is closed without merging ([#133](https://github.com/aapzu/drover/pull/133))

## [0.3.0](https://github.com/aapzu/drover/compare/v0.2.1...v0.3.0) - 2026-11-11

- Forward live-view clicks to agents that track the mouse ([#131](https://github.com/aapzu/drover/pull/131))

## [0.2.1](https://github.com/aapzu/drover/compare/v0.2.0...v0.2.1) - 2026-11-11

- Fold cards only in the current column ([#130](https://github.com/aapzu/drover/pull/130))

## [0.2.0](https://github.com/aapzu/drover/compare/v0.1.0...v0.2.0) - 2026-11-06

- Show one card per session with f ([#129](https://github.com/aapzu/drover/pull/129))

## [0.1.0](https://github.com/aapzu/drover/commits/v0.1.0) - 2026-11-06

The first tagged release, summarised by theme. The [release notes](https://github.com/aapzu/drover/releases/tag/v0.1.0) list all of its pull requests.

- Board: cards for Claude sessions that move through Planning, Executing, In review and Done, with a command palette (ctrl+k), `/` filtering, hidden and archived cards, and a settings view for `drover.json` and `drover.local.json` ([#52](https://github.com/aapzu/drover/pull/52), [#39](https://github.com/aapzu/drover/pull/39), [#95](https://github.com/aapzu/drover/pull/95), [#26](https://github.com/aapzu/drover/pull/26), [#98](https://github.com/aapzu/drover/pull/98))
- Pull requests: cards follow their PR through review, merge and release, show queued and merged state, and move to Done when the PR merges ([#2](https://github.com/aapzu/drover/pull/2), [#108](https://github.com/aapzu/drover/pull/108), [#118](https://github.com/aapzu/drover/pull/118))
- Review flow: the board reviews a PR, proposes fixes before applying them, and opens the PR once the review comes back clean ([#71](https://github.com/aapzu/drover/pull/71), [#105](https://github.com/aapzu/drover/pull/105))
- Agents: suspend all agents and hand them off to native Claude, open or take over agents running outside the board, cap and suspend idle agents, and notify about them ([#1](https://github.com/aapzu/drover/pull/1), [#12](https://github.com/aapzu/drover/pull/12), [#36](https://github.com/aapzu/drover/pull/36))
- Sessions: import Claude sessions, start a new one with `n`, title cards with a Haiku summary, and start Sessions cards in plan mode ([#28](https://github.com/aapzu/drover/pull/28), [#4](https://github.com/aapzu/drover/pull/4), [#40](https://github.com/aapzu/drover/pull/40), [#77](https://github.com/aapzu/drover/pull/77))
- Worktrees: one per card under `.claude/worktrees`, set up on creation, with a shell and base-branch update per card ([#32](https://github.com/aapzu/drover/pull/32), [#37](https://github.com/aapzu/drover/pull/37))
- Cards show context fill and estimated cost, and the board footer shows the selected card's state ([#38](https://github.com/aapzu/drover/pull/38), [#100](https://github.com/aapzu/drover/pull/100))
- Columns and automations: `run_script`, `notify` and `condition` automations, `keepAgent` columns, `conditionScript`, and column settings you edit from the UI ([#35](https://github.com/aapzu/drover/pull/35), [#73](https://github.com/aapzu/drover/pull/73), [#51](https://github.com/aapzu/drover/pull/51), [#116](https://github.com/aapzu/drover/pull/116))
- Board assistant: `drover ask` and the `:` side panel running the Claude Code TUI, resizable by mouse ([#56](https://github.com/aapzu/drover/pull/56), [#66](https://github.com/aapzu/drover/pull/66), [#91](https://github.com/aapzu/drover/pull/91))
- CLI: change the board from outside it ([#27](https://github.com/aapzu/drover/pull/27))
- Live view: forward wheel scrolling to agents on the alternate screen, pass Shift+Enter through to agents in tmux, and keep attached agent terminals from being resized by the preview ([#29](https://github.com/aapzu/drover/pull/29), [#7](https://github.com/aapzu/drover/pull/7), [#96](https://github.com/aapzu/drover/pull/96))
- Board state lives outside the repo, one board per repo ([#6](https://github.com/aapzu/drover/pull/6))
- CI workflow for build, vet and tests, and version numbers with automatic releases ([#53](https://github.com/aapzu/drover/pull/53), [#127](https://github.com/aapzu/drover/pull/127))
