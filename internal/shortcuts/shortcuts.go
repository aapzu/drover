// Package shortcuts lists the board's keys and mouse gestures, for the help
// overlay and the assistant to share.
package shortcuts

import "strings"

type Row struct{ Key, Desc string }

var Keys = []Row{
	{"ctrl+k", "search and run a command"},
	{"←→ / h l", "select column"},
	{"↑↓ / j k, g G", "select card"},
	{"H / L, shift+←→", "move card to previous / next column"},
	{"m", "move card to a column (↑↓ and enter, or 1-9)"},
	{"n", "new card with an agent in the first column"},
	{"e", "edit title and prompt"},
	{"x / D", "delete task (branch is kept)"},
	{"enter / a", "attach to the agent's terminal, starting it if needed (Ctrl-q returns)"},
	{"tab", "type into the agent from the preview (Ctrl-q returns)"},
	{"s", "send a message to the agent"},
	{"r", "start or resume the agent in place"},
	{"S", "suspend the agent (resume later with r)"},
	{"c", "open a shell in the card's worktree (Ctrl-q returns)"},
	{":", "open and type into the assistant panel (Ctrl-q returns to the board and hides it)"},
	{"N", "start a new assistant conversation"},
	{"/", "filter cards by number, title, branch, issue or PR (esc clears)"},
	{"u", "update the worktree from its base branch"},
	{"p", "toggle live preview"},
	{"+ / -", "taller / shorter preview"},
	{"< / >", "narrower / wider column"},
	{"z", "card size: normal, compact, tall with last reply"},
	{"0", "reset layout"},
	{"drag", "select text in the live preview and copy it"},
	{"opt/shift+drag", "select text anywhere (opt on macOS, shift elsewhere)"},
	{"M", "toggle mouse (off lets the terminal select text)"},
	{"i", "task details and automation log"},
	{"I", "import running Claude Code agents, past sessions or Linear issues"},
	{"b", "hide or unhide a card in any column (its worktree and agent are untouched)"},
	{"B", "show or hide hidden cards"},
	{"A", "archive or restore a card in Done"},
	{"C", "archive every card in Done"},
	{"v", "show or hide archived cards"},
	{"f", "fold or unfold this column to one card per session"},
	{"R", "show or hide the review lane (columns with lane \"review\")"},
	{"t", "show or hide column settings under column names"},
	{"o", "change settings in drover.local.json and drover.json"},
	{"O", "change column settings in drover.local.json and drover.json"},
	{"W", "write the default board to drover.json"},
	{"q", "quit (agents keep running in tmux)"},
	{"Q", "suspend every agent and quit, printing claude --resume commands"},
}

var Mouse = []Row{
	{"drag a card", "move it to another column"},
	{"double-click", "attach"},
	{"drag column edge", "resize the column"},
	{"drag preview top", "resize the preview"},
	{"drag assistant edge", "resize the assistant"},
	{"click preview", "type into the agent"},
	{"click assistant", "type into the assistant"},
}

var Live = []Row{
	{"ctrl+q", "back to board"},
	{"mouse wheel", "scroll the agent's history"},
	{"click", "goes to agents that track the mouse"},
	{"typing", "jumps back to the bottom"},
	{"anything else", "goes to the agent"},
}

// Text lists every shortcut as plain text, one "key: description" line each.
func Text() string {
	var b strings.Builder
	for i, s := range []struct {
		title string
		rows  []Row
	}{
		{"Board keys", Keys},
		{"Mouse on the board", Mouse},
		{"Live mode (typing into an agent from the board)", Live},
	} {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s.title + ":\n")
		for _, r := range s.rows {
			b.WriteString(r.Key + ": " + r.Desc + "\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}
