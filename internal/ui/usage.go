package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/store"
)

type usageRow struct {
	task    *store.Task
	session *store.Session
	cost    float64
}

// costLabel is a cost like "$1.20", or "" when it rounds to nothing.
func costLabel(cost float64) string {
	if cost < 0.005 {
		return ""
	}
	return fmt.Sprintf("$%.2f", cost)
}

func (m Model) usageRows() ([]usageRow, float64) {
	var rows []usageRow
	var total float64
	for _, t := range m.snap.Tasks {
		var cost float64
		for _, s := range t.Sessions {
			cost += s.Usage.Cost
		}
		if store.Owner(m.snap.Tasks, t) != t {
			continue
		}
		s := t.AgentSession()
		if costLabel(cost) == "" && contextLabel(s) == "" {
			continue
		}
		total += cost
		rows = append(rows, usageRow{t, s, cost})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].cost != rows[j].cost {
			return rows[i].cost > rows[j].cost
		}
		return rows[i].task.Number < rows[j].task.Number
	})
	return rows, total
}

func (m Model) renderUsage() string {
	rows, total := m.usageRows()
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", bold.Render("Usage and cost"))
	if len(rows) == 0 {
		b.WriteString(muted.Render("No usage recorded yet."))
		return b.String()
	}
	fmt.Fprintf(&b, "%s %s\n\n", muted.Render(fmt.Sprintf("%-10s", "Total")), fmt.Sprintf("$%.2f", total))

	const numW, modelW, ctxW, costW = 5, 24, 5, 9
	titleW := max(10, m.width-6-numW-modelW-ctxW-costW-4)
	cell := func(w int, right bool, text string) string {
		style := lipgloss.NewStyle().Width(w)
		if right {
			style = style.Align(lipgloss.Right)
		}
		return style.Render(ansi.Truncate(text, w, "…"))
	}
	line := func(num, title, model, ctx, cost string) {
		b.WriteString(strings.Join([]string{
			cell(numW, false, num), cell(titleW, false, title), cell(modelW, false, model),
			cell(ctxW, true, ctx), cell(costW, true, cost),
		}, " ") + "\n")
	}
	line("", muted.Render("Task"), muted.Render("Model"), muted.Render("Ctx"), muted.Render("Cost"))
	for _, r := range rows {
		var model string
		if r.session != nil {
			model = r.session.Usage.Model
		}
		line(fmt.Sprintf("#%d", r.task.Number), taskTitle(r.task), model, contextLabel(r.session), costLabel(r.cost))
	}
	return b.String()
}
