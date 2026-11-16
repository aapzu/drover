package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/store"
)

// scheduleWindows are the spans the schedule timeline can show.
var scheduleWindows = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}

const scheduleKey = "T"

// now is when the snapshot was taken, so what a view shows is consistent.
func (m Model) now() time.Time {
	if m.snap.At.IsZero() {
		return time.Now()
	}
	return m.snap.At
}

// countdown says how long it is until a run, like "in 4m12s", or "due" once
// it is past.
func countdown(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d <= 0:
		return "due"
	case d < time.Minute:
		return fmt.Sprintf("in %ds", d/time.Second)
	case d < 10*time.Minute:
		return fmt.Sprintf("in %dm%02ds", d/time.Minute, d%time.Minute/time.Second)
	case d < time.Hour:
		return fmt.Sprintf("in %dm", d/time.Minute)
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh%02dm", d/time.Hour, d%time.Hour/time.Minute)
	}
	return fmt.Sprintf("in %dd", d/(24*time.Hour))
}

// span names a stretch of time, like "15m", "1h30m" or "6h".
func span(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d > time.Hour:
		return fmt.Sprintf("%dh%dm", d/time.Hour, d%time.Hour/time.Minute)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

// pollingLabel is what a card shows for an agent that waits for its next run.
func (m Model) pollingLabel(s *store.Session) string {
	next, ok := s.NextSchedule()
	if !ok {
		return ""
	}
	label := "⏲ " + countdown(next.NextAt.Sub(m.now()))
	if n := len(s.Schedules); n > 1 {
		label += fmt.Sprintf(" +%d", n-1)
	}
	return label
}

// scheduleRow is a card whose agent has runs scheduled.
type scheduleRow struct {
	task *store.Task
	runs []store.Schedule
}

// taskRuns returns the runs scheduled by the sessions of t, soonest first.
func taskRuns(t *store.Task) []store.Schedule {
	var runs []store.Schedule
	for _, s := range t.Sessions {
		runs = append(runs, s.Schedules...)
	}
	slices.SortFunc(runs, func(a, b store.Schedule) int {
		return cmp.Or(a.NextAt.Compare(b.NextAt), cmp.Compare(a.ID, b.ID))
	})
	return runs
}

// scheduleRows lists the cards of agents that have runs scheduled, ordered by
// their next run. Archived cards are left out.
func (m Model) scheduleRows() []scheduleRow {
	var rows []scheduleRow
	for _, t := range m.snap.Tasks {
		if runs := taskRuns(t); len(runs) > 0 && !t.Archived {
			rows = append(rows, scheduleRow{t, runs})
		}
	}
	slices.SortStableFunc(rows, func(a, b scheduleRow) int { return a.runs[0].NextAt.Compare(b.runs[0].NextAt) })
	return rows
}

// runsOf returns the runs scheduled by the agent t's card shows, which for a
// card that shares its agent with others is the owner's.
func (m Model) runsOf(t *store.Task) []store.Schedule {
	return taskRuns(store.Owner(m.snap.Tasks, t))
}

// scheduleLine describes one scheduled run, like
// `in 4m12s  cron */3 * * * *  ran 2×  check the build`.
func (m Model) scheduleLine(sc store.Schedule, width int) string {
	kind := "once"
	switch {
	case sc.Wakeup:
		kind = "wakeup"
	case sc.Recurring:
		kind = "cron " + sc.Cron
	}
	parts := []string{countdown(sc.NextAt.Sub(m.now())), kind}
	if sc.Runs > 0 {
		parts = append(parts, fmt.Sprintf("ran %d×", sc.Runs))
	}
	text := sc.Reason
	if text == "" {
		text = sc.Prompt
	}
	line := strings.Join(parts, "  ")
	if text = strings.Join(strings.Fields(text), " "); text != "" {
		line += "  " + dim.Render(ansi.Truncate(text, max(10, width-lipgloss.Width(line)-2), "…"))
	}
	return ansi.Truncate(line, width, "…")
}

func (m *Model) openSchedules() {
	m.mode = modeSchedules
	m.scheduleSel = 0
}

func (m Model) updateSchedules(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.scheduleRows()
	m.scheduleSel = max(0, min(m.scheduleSel, len(rows)-1))
	switch msg.String() {
	case "esc", "q", scheduleKey:
		m.mode = modeBoard
	case "up", "k":
		m.scheduleSel = max(0, m.scheduleSel-1)
	case "down", "j":
		m.scheduleSel = min(len(rows)-1, m.scheduleSel+1)
	case "left", "h":
		m.scheduleWindow = max(0, m.scheduleWindow-1)
	case "right", "l":
		m.scheduleWindow = min(len(scheduleWindows)-1, m.scheduleWindow+1)
	case "enter":
		if len(rows) > 0 {
			m.selectTask(rows[m.scheduleSel].task)
			m.mode = modeBoard
		}
	}
	return m, nil
}

// renderSchedules draws a timeline with a row for each polling card and a mark
// where each of its runs falls in the window, followed by the selected card's
// schedules. It shows at most height lines, scrolling the rows to keep the
// selected one in view.
func (m Model) renderSchedules(width, height int) string {
	rows := m.scheduleRows()
	window := scheduleWindows[m.scheduleWindow]
	title := bold.Render("Schedules") + dim.Render(fmt.Sprintf("  next %s  ←→ window", span(window)))
	if len(rows) == 0 {
		return title + "\n\n" + dim.Render("No agent has runs scheduled. Agents that use /loop, ScheduleWakeup or CronCreate show up here.")
	}
	sel := max(0, min(m.scheduleSel, len(rows)-1))
	labelW := min(28, width/3)
	axisW := max(10, width-labelW-2)
	now := m.now()

	axis := []rune(strings.Repeat("─", axisW))
	labels := []rune(strings.Repeat(" ", axisW))
	for q := 0; q <= 4; q++ {
		x := min(axisW-1, q*axisW/4)
		axis[x] = '┼'
		text := []rune("now")
		if q > 0 {
			text = []rune(span(window * time.Duration(q) / 4))
		}
		copy(labels[min(x, max(0, axisW-len(text))):], text)
	}
	indent := strings.Repeat(" ", labelW+1)
	lines := []string{title, "", indent + dim.Render(string(labels)), indent + dim.Render(string(axis))}

	runs := rows[sel].runs
	// What is left of the height after the header, the legend and the selected
	// card's runs, which are cut before the rows are.
	room := height - len(lines) - 4
	shown := min(len(runs), max(1, room/3))
	more := 0
	if shown < len(runs) {
		more = 1
	}
	room = max(1, room-shown-more)
	first := clamp(sel-room+1, 0, max(0, len(rows)-room))
	for i := first; i < min(len(rows), first+room); i++ {
		r := rows[i]
		cells := make([]string, axisW)
		for j := range cells {
			cells[j] = dim.Render("·")
		}
		for _, sc := range r.runs {
			mark := idle.Render("●")
			if sc.Wakeup {
				mark = thinking.Render("◇")
			}
			for _, at := range sc.Fires(now, now.Add(window), window/time.Duration(axisW)) {
				x := min(axisW-1, int(float64(at.Sub(now))/float64(window)*float64(axisW)))
				cells[x] = mark
			}
		}
		label := ansi.Truncate(fmt.Sprintf("#%d %s", r.task.Number, taskTitle(r.task)), labelW-2, "…")
		prefix := "  "
		if i == sel {
			prefix, label = "▸ ", bold.Render(label)
		}
		pad := strings.Repeat(" ", max(0, labelW-2-lipgloss.Width(label)))
		lines = append(lines, prefix+label+pad+" "+strings.Join(cells, ""))
	}

	lines = append(lines, "", dim.Render("● cron   ◇ wakeup"), "", muted.Render(fmt.Sprintf("#%d", rows[sel].task.Number)))
	for _, sc := range runs[:shown] {
		lines = append(lines, "  "+m.scheduleLine(sc, width-2))
	}
	if more > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("  +%d more", len(runs)-shown)))
	}
	return strings.Join(lines, "\n")
}
