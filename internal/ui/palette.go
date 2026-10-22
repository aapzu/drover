package ui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const paletteKey = "ctrl+k"

// paletteCommand is a board key the command palette can press for you.
type paletteCommand struct{ name, key string }

var paletteCommands = []paletteCommand{
	{"New session", "n"},
	{"Attach to the agent's terminal, starting it if needed", "enter"},
	{"Type into the agent from the preview", "tab"},
	{"Send a message to the agent", "s"},
	{"Start or resume the agent", "r"},
	{"Suspend the agent", "S"},
	{"Open a shell in the card's worktree", "c"},
	{"Update the worktree from its base branch", "u"},
	{"Edit title and prompt", "e"},
	{"Delete task", "x"},
	{"Move card to the previous column", "H"},
	{"Move card to the next column", "L"},
	{"Move card to a column…", "m"},
	{"Archive or restore card", "A"},
	{"Archive every card in Done", "C"},
	{"Show or hide archived cards", "v"},
	{"Filter cards", "/"},
	{"Open the assistant panel and type into it", askKey},
	{"Hide the assistant panel", liveExitKey},
	{"Start a new assistant conversation", newAskKey},
	{"Task details and activity log", "i"},
	{"Import agents, sessions or Linear issues", "I"},
	{"Toggle live preview", "p"},
	{"Taller preview", "+"},
	{"Shorter preview", "-"},
	{"Wider column", ">"},
	{"Narrower column", "<"},
	{"Cycle card size", "z"},
	{"Reset layout", "0"},
	{"Toggle mouse", "M"},
	{"Write the default board to drover.json", "W"},
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

func (m Model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	matches := m.paletteMatches()
	switch msg.String() {
	case "esc", paletteKey:
		m.mode = modeBoard
		return m, nil
	case "up", "ctrl+p":
		m.paletteSel = max(0, m.paletteSel-1)
		return m, nil
	case "down", "ctrl+n":
		m.paletteSel = max(0, min(len(matches)-1, m.paletteSel+1))
		return m, nil
	case "enter":
		if m.paletteSel >= len(matches) {
			return m, nil
		}
		m.mode = modeBoard
		return m.updateBoard(keyPress(matches[m.paletteSel].key))
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
	matches := m.paletteMatches()
	rows := clamp(height-9, 1, 12)
	start := clamp(m.paletteSel-rows/2, 0, max(0, len(matches)-rows))

	lines := []string{bold.Render("Commands"), m.paletteInput.View(), ""}
	for i := start; i < min(len(matches), start+rows); i++ {
		c := matches[i]
		key := dim.Render(c.key)
		name := ansi.Truncate(c.name, inner-lipgloss.Width(key)-3, "…")
		gap := strings.Repeat(" ", max(1, inner-2-lipgloss.Width(name)-lipgloss.Width(key)))
		prefix := "  "
		if i == m.paletteSel {
			prefix, name = "▸ ", bold.Render(name)
		}
		lines = append(lines, prefix+name+gap+key)
	}
	if len(matches) == 0 {
		lines = append(lines, dim.Render("No matching commands"))
	} else if len(matches) > rows {
		lines = append(lines, dim.Render(fmt.Sprintf("%d of %d", m.paletteSel+1, len(matches))))
	}
	return modalStyle.Width(width).Render(strings.Join(lines, "\n"))
}

func newPaletteInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Search commands"
	ti.Prompt = "› "
	return ti
}
