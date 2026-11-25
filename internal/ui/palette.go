package ui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/store"
)

const paletteKey = "ctrl+k"

// paletteCommand is a board key the command palette can press for you.
type paletteCommand struct{ name, key string }

var paletteCommands = []paletteCommand{
	{"New card with an agent", "n"},
	{"New review card with an agent", newReviewKey},
	{"Attach to the agent's terminal, starting it if needed", "enter"},
	{"Type into the agent from the preview", "tab"},
	{"Send a message to the agent", "s"},
	{"Start or resume the agent", "r"},
	{"Suspend the agent", "S"},
	{"Open a shell in the card's worktree", "c"},
	{"Update the worktree from its base branch", "u"},
	{"Edit title and prompt", "e"},
	{"Rename card, suggesting a title", "E"},
	{"Delete task", "x"},
	{"Move card to the previous column", "H"},
	{"Move card to the next column", "L"},
	{"Move card to a column…", "m"},
	{"Hide or unhide card", "b"},
	{"Show or hide hidden cards", "B"},
	{"Pin or unpin card", "P"},
	{"Archive or restore card", "A"},
	{"Archive every card in Done", "C"},
	{"Show or hide archived cards", "v"},
	{"Fold or unfold this column to one card per session", "f"},
	{"Show or hide the review lane", "R"},
	{"Show only the review lane, or bring the main columns back", "X"},
	{"Show or hide column settings", "t"},
	{"Filter cards", "/"},
	{"Open the assistant panel and type into it", askKey},
	{"Show the assistant panel fullscreen", zoomKey},
	{"Hide the assistant panel", liveExitKey},
	{"Start a new assistant conversation", newAskKey},
	{"Change settings in the global and project config", "o"},
	{"Change column settings in the config that holds the columns", "O"},
	{"Task details and activity log", "i"},
	{"Usage and cost", "$"},
	{"Timeline of the runs agents have scheduled", scheduleKey},
	{"Import agents, sessions, Linear or GitHub issues or PRs to review", "I"},
	{"Toggle live preview", "p"},
	{"Taller preview", "+"},
	{"Shorter preview", "-"},
	{"Wider column", ">"},
	{"Narrower column", "<"},
	{"Cycle card size", "z"},
	{"Reset layout", "0"},
	{"Toggle mouse", "M"},
	{"Update drover to the latest release", "U"},
	{"Write the default board to the project config", "W"},
	{"Help", "?"},
	{"Suspend every agent and quit", "Q"},
	{"Quit", "q"},
}

func keyPress(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case liveExitKey:
		return tea.KeyMsg{Type: tea.KeyCtrlQ}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

func (m *Model) openPalette() tea.Cmd {
	m.mode = modePalette
	m.paletteInput.Reset()
	m.paletteSel = 0
	return m.paletteInput.Focus()
}

func (m Model) paletteMatches() []paletteCommand {
	query := m.paletteInput.Value()
	return slices.DeleteFunc(slices.Clone(paletteCommands), func(c paletteCommand) bool {
		return !containsAllWords(c.name+" "+c.key, query)
	})
}

// paletteCards is the cards on the board that match the search, newest first,
// including those in a lane the board does not draw.
func (m Model) paletteCards() []*store.Task {
	query := m.paletteInput.Value()
	if strings.TrimSpace(query) == "" {
		return nil
	}
	var onBoard []*store.Task
	for _, t := range m.snap.Tasks {
		id := m.columnOf(t)
		if _, _, ok := m.snap.Board.Column(id); ok && !m.offBoard(t, m.hidesArchived(id)) {
			onBoard = append(onBoard, t)
		}
	}
	cards := filterCards(onBoard, query)
	slices.SortFunc(cards, func(a, b *store.Task) int { return b.Number - a.Number })
	return cards
}

// openCard selects the card, showing its lane if the board hides it, and
// attaches to it as enter does on the board.
func (m Model) openCard(t *store.Task) (tea.Model, tea.Cmd) {
	m.mode = modeBoard
	m.filter = ""
	if _, col, ok := m.snap.Board.Column(m.columnOf(t)); ok {
		m.revealColumn(col)
	}
	m.selectTask(t)
	return m.updateBoard(keyPress("enter"))
}

func (m Model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cards, matches := m.paletteCards(), m.paletteMatches()
	rows := len(cards) + len(matches)
	switch msg.String() {
	case "esc", paletteKey:
		m.mode = modeBoard
		return m, nil
	case "up", "ctrl+p":
		m.paletteSel = max(0, m.paletteSel-1)
		return m, nil
	case "down", "ctrl+n":
		m.paletteSel = max(0, min(rows-1, m.paletteSel+1))
		return m, nil
	case "enter":
		if m.paletteSel >= rows {
			return m, nil
		}
		if m.paletteSel < len(cards) {
			return m.openCard(cards[m.paletteSel])
		}
		m.mode = modeBoard
		return m.updateBoard(keyPress(matches[m.paletteSel-len(cards)].key))
	}
	before := m.paletteInput.Value()
	var cmd tea.Cmd
	m.paletteInput, cmd = m.paletteInput.Update(msg)
	if m.paletteInput.Value() != before {
		m.paletteSel = 0
	}
	return m, cmd
}

func (m Model) paletteWidth() int { return min(70, m.width-4) }

func (m Model) renderPalette(height int) string {
	width := m.paletteWidth()
	inner := width - 6
	cards, commands := m.paletteCards(), m.paletteMatches()
	total := len(cards) + len(commands)
	rows := clamp(height-9, 1, 12)
	start := clamp(m.paletteSel-rows/2, 0, max(0, total-rows))

	lines := []string{bold.Render("Search"), m.paletteInput.View(), ""}
	for i := start; i < min(total, start+rows); i++ {
		var name, hint string
		if i < len(cards) {
			t := cards[i]
			col, _, _ := m.snap.Board.Column(m.columnOf(t))
			name, hint = fmt.Sprintf("#%d %s", t.Number, taskTitle(t)), col.Name
		} else {
			c := commands[i-len(cards)]
			name, hint = c.name, c.key
		}
		hint = dim.Render(hint)
		name = ansi.Truncate(name, inner-lipgloss.Width(hint)-3, "…")
		gap := strings.Repeat(" ", max(1, inner-2-lipgloss.Width(name)-lipgloss.Width(hint)))
		prefix := "  "
		if i == m.paletteSel {
			prefix, name = "▸ ", bold.Render(name)
		}
		lines = append(lines, prefix+name+gap+hint)
	}
	if total == 0 {
		lines = append(lines, dim.Render("No matches"))
	} else if total > rows {
		lines = append(lines, dim.Render(fmt.Sprintf("%d of %d", m.paletteSel+1, total)))
	}
	return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
}

func newPaletteInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Search cards and commands"
	ti.Prompt = "› "
	return ti
}
