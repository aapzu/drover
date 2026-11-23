package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

// reviewLevelState is the picker for the /code-review level of a review card
// about to start its first agent; start runs once a level is picked.
type reviewLevelState struct {
	task   string
	number int
	cursor int
	start  func() tea.Cmd
}

// askReviewLevel opens the level picker when starting t's agent would run
// /code-review without a level, and reports whether it did.
func (m *Model) askReviewLevel(t *store.Task, start func() tea.Cmd) bool {
	if !engine.UnleveledReview(t) {
		return false
	}
	m.mode = modeReviewLevel
	m.level = reviewLevelState{task: t.ID, number: t.Number, cursor: slices.Index(engine.ReviewLevels, "high"), start: start}
	return true
}

func (m Model) updateReviewLevel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(engine.ReviewLevels)
	switch k := msg.String(); {
	case k == "esc" || k == "q":
		m.mode = modeBoard
		return m, nil
	case k == "up" || k == "k":
		m.level.cursor = max(0, m.level.cursor-1)
		return m, nil
	case k == "down" || k == "j":
		m.level.cursor = min(n-1, m.level.cursor+1)
		return m, nil
	case len(k) == 1 && k[0] >= '1' && int(k[0]-'1') < n:
		m.level.cursor = int(k[0] - '1')
	case k != "enter":
		return m, nil
	}
	m.mode = modeBoard
	if err := m.eng.SetReviewLevel(m.level.task, engine.ReviewLevels[m.level.cursor]); err != nil {
		m.setFlash(fmt.Sprintf("#%d: %v", m.level.number, err))
		return m, nil
	}
	m.snap = m.eng.Snapshot()
	return m, m.level.start()
}

func (m Model) renderReviewLevel() string {
	lines := []string{bold.Render(fmt.Sprintf("Review #%d at which /code-review level?", m.level.number)), ""}
	for i, level := range engine.ReviewLevels {
		line := fmt.Sprintf("  %d %s", i+1, level)
		if i == m.level.cursor {
			line = bold.Render(fmt.Sprintf("▸ %d %s", i+1, level))
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", dim.Render("The card's prompt keeps it; e edits it later."))
	return modalStyle.Width(min(60, m.width-4)).Render(strings.Join(lines, "\n"))
}

// startsAgent reports whether starting t in place can run an agent: it isn't
// in Done.
func (m Model) startsAgent(t *store.Task) bool {
	col, _, _ := m.snap.Board.Column(t.ColumnID)
	return !col.IsDone()
}
