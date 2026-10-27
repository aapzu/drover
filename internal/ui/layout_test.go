package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/config"
	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

func testModel(width, height, columns int, tasksPerColumn int) Model {
	board := &config.Board{}
	var tasks []*store.Task
	for i := range columns {
		id := fmt.Sprintf("c%d", i)
		board.Columns = append(board.Columns, config.Column{ID: id, Name: fmt.Sprintf("Col %d", i)})
		for j := range tasksPerColumn {
			tasks = append(tasks, &store.Task{ID: fmt.Sprintf("%s-t%d", id, j), Number: len(tasks) + 1, Title: "task", ColumnID: id})
		}
	}
	return Model{
		snap:     engine.Snapshot{Board: board, Tasks: tasks},
		width:    width,
		height:   height,
		selected: map[string]string{},
		busy:     map[string]op{},
		prefs:    prefs{ColumnWidths: map[string]int{}},
	}
}

func sum(ws []int) int {
	total := 0
	for _, w := range ws {
		total += w
	}
	return total
}

func TestColumnWindowFillsWidth(t *testing.T) {
	m := testModel(120, 40, 4, 0)
	start, widths := m.columnWindow()
	if start != 0 || len(widths) != 4 || sum(widths) != 120 {
		t.Fatalf("got start %d widths %v", start, widths)
	}
}

func TestColumnWindowKeepsResizedWidth(t *testing.T) {
	m := testModel(120, 40, 4, 0)
	m.prefs.ColumnWidths["c1"] = 50
	_, widths := m.columnWindow()
	if widths[1] != 50 || sum(widths) != 120 {
		t.Fatalf("got widths %v", widths)
	}
}

func TestColumnWindowScrollsToSelection(t *testing.T) {
	m := testModel(60, 40, 6, 0)
	m.col = 5
	start, widths := m.columnWindow()
	if start+len(widths) != 6 || sum(widths) != 60 {
		t.Fatalf("got start %d widths %v", start, widths)
	}
}

func TestLayoutPreviewHeightIsClamped(t *testing.T) {
	m := testModel(120, 40, 3, 0)
	m.prefs.PreviewHeight = 100
	l := m.layout()
	if l.boardH != minBoardHeight || l.previewY != l.bodyY+minBoardHeight {
		t.Fatalf("got board %d preview %d", l.boardH, l.previewH)
	}
	m.prefs.HidePreview = true
	if l := m.layout(); l.previewH != 0 || l.boardH != l.bodyH {
		t.Fatalf("hidden preview still takes %d rows", l.previewH)
	}
}

func TestHitTestMatchesRenderedBoard(t *testing.T) {
	for _, d := range []density{densityNormal, densityCompact, densityTall} {
		m := testModel(120, 50, 3, 3)
		m.prefs.Density = d
		l := m.layout()
		lines := strings.Split(m.renderBoard(l), "\n")
		for _, c := range l.cols {
			for k, task := range m.tasksForColumn(c.index) {
				y := l.bodyY + columnHeaderHeight + k*d.cardHeight() + 1
				h := l.hitTest(m, c.x+3, y)
				if h.kind != hitCard || h.task.ID != task.ID {
					t.Fatalf("density %d: hit at %d,%d = %+v, want %s", d, c.x+3, y, h, task.ID)
				}
				row := ansi.Strip(lines[y-l.bodyY])
				if !strings.Contains(ansi.Cut(row, c.x, c.x+c.width), fmt.Sprintf("#%d", task.Number)) {
					t.Fatalf("density %d: row %d %q has no #%d in column %d", d, y, row, task.Number, c.index)
				}
			}
			if h := l.hitTest(m, c.x+c.width-1, l.bodyY+3); h.kind != hitColumnEdge || h.col != c.index {
				t.Fatalf("edge of column %d: %+v", c.index, h)
			}
		}
		if h := l.hitTest(m, 5, l.previewY); h.kind != hitPreviewEdge {
			t.Fatalf("preview border: %+v", h)
		}
		if h := l.hitTest(m, 5, l.previewY+2); h.kind != hitPreview {
			t.Fatalf("preview body: %+v", h)
		}
	}
}

func TestCardHeightMatchesDensity(t *testing.T) {
	m := testModel(120, 40, 1, 1)
	task := m.snap.Tasks[0]
	task.Title = strings.Repeat("long title ", 10)
	for _, d := range []density{densityNormal, densityCompact, densityTall} {
		m.prefs.Density = d
		if got := lipgloss.Height(m.renderCard(task, 30, false, false)); got != d.cardHeight() {
			t.Fatalf("density %d: card is %d rows, want %d", d, got, d.cardHeight())
		}
	}
}

func TestOverlay(t *testing.T) {
	got := overlay("aaaaaa\nbbbbbb\ncccccc", "XY\nZW", 2, 1)
	want := "aaaaaa\nbbXYbb\nccZWcc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := overlay("ab", "XY", 4, 0); got != "ab  XY" {
		t.Fatalf("past line end: got %q", got)
	}
}

func TestPreviewWindow(t *testing.T) {
	text := "1\n2\n3\n4\n5\n6\n"
	cases := []struct {
		scroll int
		want   string
	}{
		{0, "4\n5\n6"},
		{2, "2\n3\n4"},
		{3, "1\n2\n3"},
		{50, "1\n2\n3"},
	}
	for _, c := range cases {
		if got := previewWindow(text, 3, c.scroll); got != c.want {
			t.Errorf("scroll %d: got %q want %q", c.scroll, got, c.want)
		}
	}
}

func TestPreviewScrollStaysAnchored(t *testing.T) {
	m := testModel(120, 40, 1, 1)
	id := m.snap.Tasks[0].ID
	next, _ := m.Update(previewMsg{taskID: id, history: 100, at: time.Now()})
	m = next.(Model)
	m.previewScroll = 10
	next, _ = m.Update(previewMsg{taskID: id, history: 104, at: time.Now()})
	if got := next.(Model).previewScroll; got != 14 {
		t.Fatalf("scroll is %d after 4 new lines, want 14", got)
	}
	next, _ = next.(Model).Update(previewMsg{taskID: "other", history: 104, at: time.Now()})
	if got := next.(Model).previewScroll; got != 0 {
		t.Fatalf("scroll is %d after switching task, want 0", got)
	}
}

func sentJob(m Model) (keyJob, bool) {
	select {
	case job := <-m.keys.jobs:
		return job, true
	default:
		return keyJob{}, false
	}
}

func TestWheelGoesToAgentThatTracksMouse(t *testing.T) {
	setup := func() Model {
		m := testModel(120, 40, 1, 1)
		id := m.snap.Tasks[0].ID
		m.snap.Live = map[string]string{id: "sess"}
		m.keys = &keySender{jobs: make(chan keyJob, 4)}
		m.previewFor, m.previewMouse = id, true
		return m
	}
	l := setup().layout()
	cases := []struct {
		name   string
		button tea.MouseButton
		x, y   int
		want   string
	}{
		{"up", tea.MouseButtonWheelUp, 10, l.previewY + 3, "\x1b[<64;10;3M"},
		{"down", tea.MouseButtonWheelDown, 10, l.previewY + 3, "\x1b[<65;10;3M"},
		{"left border", tea.MouseButtonWheelUp, 0, l.previewY + 3, "\x1b[<64;1;3M"},
		{"right border", tea.MouseButtonWheelUp, 119, l.previewY + 3, "\x1b[<64;118;3M"},
		{"top border", tea.MouseButtonWheelUp, 10, l.previewY, "\x1b[<64;10;1M"},
		{"bottom border", tea.MouseButtonWheelUp, 10, l.previewY + l.previewH - 1, fmt.Sprintf("\x1b[<64;10;%dM", l.previewH-2)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := setup()
			m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: c.button, X: c.x, Y: c.y})
			job, ok := sentJob(m)
			if !ok || job.session != "sess" || job.literal != c.want {
				t.Fatalf("sent %+v (sent=%v), want %q to sess", job, ok, c.want)
			}
		})
	}

	scrolls := map[string]func(*Model){
		"pane has scrollback": func(m *Model) { m.previewHistory = 50 },
		"agent ignores mouse": func(m *Model) { m.previewMouse = false },
		"stale selection":     func(m *Model) { m.previewFor = "other" },
	}
	for name, mutate := range scrolls {
		t.Run(name, func(t *testing.T) {
			m := setup()
			mutate(&m)
			m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: 10, Y: l.previewY + 3})
			if job, ok := sentJob(m); ok {
				t.Fatalf("sent %+v, want the preview to scroll instead", job)
			}
		})
	}
}

func TestPreviewMsgSetsMouse(t *testing.T) {
	m := testModel(120, 40, 1, 1)
	id := m.snap.Tasks[0].ID
	next, _ := m.Update(previewMsg{taskID: id, mouse: true, at: time.Now()})
	if !next.(Model).previewMouse {
		t.Fatal("previewMouse not set from the capture")
	}
	next, _ = next.(Model).Update(previewMsg{taskID: id, at: time.Now()})
	if next.(Model).previewMouse {
		t.Fatal("previewMouse not cleared by the next capture")
	}
}

func TestTmuxKey(t *testing.T) {
	cases := []struct {
		msg          tea.KeyMsg
		literal, key string
	}{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hé")}, "hé", ""},
		{tea.KeyMsg{Type: tea.KeySpace}, " ", ""},
		{tea.KeyMsg{Type: tea.KeyEnter}, "", "Enter"},
		{tea.KeyMsg{Type: tea.KeyEsc}, "", "Escape"},
		{tea.KeyMsg{Type: tea.KeyBackspace}, "", "BSpace"},
		{tea.KeyMsg{Type: tea.KeyShiftTab}, "", "BTab"},
		{tea.KeyMsg{Type: tea.KeyCtrlC}, "", "C-c"},
		{tea.KeyMsg{Type: tea.KeyCtrlUp}, "", "C-Up"},
		{tea.KeyMsg{Type: tea.KeyShiftLeft}, "", "S-Left"},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p"), Alt: true}, "", "M-p"},
		{tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, "", "M-Enter"},
		{tea.KeyMsg{Type: tea.KeyF5}, "", "F5"},
		{tea.KeyMsg{Type: tea.KeyPgDown}, "", "NPage"},
	}
	for _, c := range cases {
		literal, key := tmuxKey(c.msg)
		if literal != c.literal || key != c.key {
			t.Errorf("%q: got (%q, %q) want (%q, %q)", c.msg.String(), literal, key, c.literal, c.key)
		}
	}
}

func TestIdleCardWithBackgroundTasksIsNotIdle(t *testing.T) {
	m := testModel(120, 40, 2, 0)
	m.snap.Board.Columns[0].AutoSpawn = true
	task := &store.Task{ID: "t", Number: 1, Title: "Build", ColumnID: "c0",
		Sessions: map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityIdle, Background: []string{"b1"}}}}
	m.snap.Tasks = []*store.Task{task}
	card := ansi.Strip(m.renderCard(task, 60, false, false))
	if !strings.Contains(card, "◌ background tasks") || strings.Contains(card, "● idle") {
		t.Errorf("card:\n%s", card)
	}
}

func TestGroupCardsShareSessionAndShowMarker(t *testing.T) {
	m := testModel(120, 40, 2, 0)
	m.snap.Board.Columns[0].AutoSpawn = true
	owner := &store.Task{ID: "owner", Number: 1, Title: "Refactor", ColumnID: "c1", ActiveCard: "split",
		Branch: "refactor-1", PR: &store.PR{Number: 10, State: "OPEN", ReviewDecision: "APPROVED"},
		Sessions: map[string]*store.Session{store.MainSession: {Status: store.StatusRunning, Activity: store.ActivityIdle}}}
	split := &store.Task{ID: "split", Number: 2, Title: "PR two", ColumnID: "c0", GroupID: "owner",
		PR: &store.PR{Number: 11, Branch: "two", State: "MERGED"}}
	m.snap.Tasks = []*store.Task{owner, split}

	if m.sessionFor(split) != owner.Sessions[store.MainSession] {
		t.Fatal("split card doesn't resolve its group's session")
	}
	splitCard := ansi.Strip(m.renderCard(split, 60, false, false))
	for _, want := range []string{"⧉ 2/2", "● idle", "PR #11 merged two"} {
		if !strings.Contains(splitCard, want) {
			t.Errorf("split card misses %q:\n%s", want, splitCard)
		}
	}
	// The owner sits in a plain column while the agent works for the split
	// card, so it doesn't show the agent's activity.
	ownerCard := ansi.Strip(m.renderCard(owner, 60, false, false))
	if !strings.Contains(ownerCard, "⧉ 1/2") || strings.Contains(ownerCard, "idle") || !strings.Contains(ownerCard, "✓ approved PR #10") {
		t.Errorf("owner card:\n%s", ownerCard)
	}
	for pr, want := range map[store.PR]string{
		{Number: 12, State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}: "✗ changes PR #12",
		{Number: 13, State: "MERGED", Released: true}:                    "PR #13 released",
		{Number: 14, State: "OPEN", Error: "gh pr view: timed out"}:      "⚠ gh PR #14",
	} {
		card := &store.Task{ID: "pr", Number: 3, Title: "PR", ColumnID: "c0", PR: &pr}
		if got := ansi.Strip(m.renderCard(card, 60, false, false)); !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
	for _, d := range []density{densityNormal, densityCompact, densityTall} {
		m.prefs.Density = d
		if got := lipgloss.Height(m.renderCard(split, 30, false, false)); got != d.cardHeight() {
			t.Fatalf("density %d: card is %d rows, want %d", d, got, d.cardHeight())
		}
	}
}

func TestReviewCardsShowYourReview(t *testing.T) {
	m := testModel(120, 40, 1, 0)
	review := func(pr store.PR) store.PR {
		pr.Number, pr.Review, pr.State = 7, true, "OPEN"
		return pr
	}
	for _, tc := range []struct {
		pr           store.PR
		want, absent []string
	}{
		{review(store.PR{}), []string{"review PR #7"}, []string{"@", "you"}},
		{review(store.PR{Author: "them"}), []string{"review PR #7 @them"}, nil},
		{review(store.PR{Author: "them", MyReview: "APPROVED", HeadSHA: "a", ReviewedSHA: "a"}), []string{"✓ you approved"}, []string{"new commits"}},
		{review(store.PR{Author: "them", MyReview: "CHANGES_REQUESTED", HeadSHA: "a", ReviewedSHA: "a"}), []string{"✗ you requested changes"}, nil},
		{review(store.PR{Author: "them", MyReview: "COMMENTED", HeadSHA: "a", ReviewedSHA: "a"}), []string{"✓ you reviewed"}, nil},
		{review(store.PR{Author: "them", MyReview: "APPROVED", HeadSHA: "b", ReviewedSHA: "a"}), []string{"↻ new commits"}, []string{"you approved"}},
		{review(store.PR{Author: "them", ReviewDecision: "APPROVED"}), nil, []string{"✓ approved"}},
		{review(store.PR{Author: "them", Error: "gh pr view: timed out"}), []string{"⚠ gh"}, nil},
	} {
		card := &store.Task{ID: "pr", Number: 3, Title: "Review", ColumnID: "c0", PR: &tc.pr}
		got := ansi.Strip(m.renderCard(card, 80, false, false))
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%+v misses %q:\n%s", tc.pr, want, got)
			}
		}
		for _, absent := range tc.absent {
			if strings.Contains(got, absent) {
				t.Errorf("%+v shows %q:\n%s", tc.pr, absent, got)
			}
		}
	}
}

func TestSiblingsOfSelectedCardStandOut(t *testing.T) {
	m := testModel(120, 40, 2, 0)
	owner := &store.Task{ID: "owner", Number: 1, Title: "Refactor", ColumnID: "c1"}
	split := &store.Task{ID: "split", Number: 2, Title: "PR two", ColumnID: "c0", GroupID: "owner"}
	other := &store.Task{ID: "other", Number: 3, Title: "Other", ColumnID: "c0"}
	m.snap.Tasks = []*store.Task{owner, split, other}
	m.selectTask(owner)

	if got := ansi.Strip(m.renderCard(split, 30, false, false)); !strings.HasPrefix(got, "┏") {
		t.Errorf("sibling of the selected card isn't thick:\n%s", got)
	}
	if got := ansi.Strip(m.renderCard(other, 30, false, false)); !strings.HasPrefix(got, "╭") {
		t.Errorf("ungrouped card changed border:\n%s", got)
	}
	m.selectTask(other)
	if got := ansi.Strip(m.renderCard(split, 30, false, false)); !strings.HasPrefix(got, "╭") {
		t.Errorf("group card is thick without a selected sibling:\n%s", got)
	}
}

func TestSelectedCardHasDoubleBorder(t *testing.T) {
	m := testModel(120, 40, 1, 0)
	task := &store.Task{ID: "t", Number: 1, Title: "Task", ColumnID: "c0"}
	m.snap.Tasks = []*store.Task{task}

	if got := ansi.Strip(m.renderCard(task, 30, true, false)); !strings.HasPrefix(got, "╔") {
		t.Errorf("selected card isn't double-bordered:\n%s", got)
	}
	if got := ansi.Strip(m.renderCard(task, 30, false, false)); !strings.HasPrefix(got, "╭") {
		t.Errorf("unselected card changed border:\n%s", got)
	}
}

func TestGroupsGetDistinctColors(t *testing.T) {
	m := testModel(120, 40, 1, 0)
	var owners, splits []*store.Task
	for i := range len(groupColors) {
		id := fmt.Sprintf("o%d", i)
		owners = append(owners, &store.Task{ID: id, ColumnID: "c0"})
		splits = append(splits, &store.Task{ID: id + "-a", ColumnID: "c0", GroupID: id}, &store.Task{ID: id + "-b", ColumnID: "c0", GroupID: id})
	}
	slices.Reverse(splits[:4])
	m.snap.Tasks = append(owners, splits...)
	seen := map[lipgloss.Color]string{}
	for _, owner := range owners {
		c := m.groupOf(owner).color
		if other, ok := seen[c]; ok {
			t.Fatalf("groups %s and %s share color %s", other, owner.ID, c)
		}
		seen[c] = owner.ID
	}
	for _, split := range splits {
		if got, want := m.groupOf(split).color, m.groupOf(m.task(split.GroupID)).color; got != want {
			t.Errorf("%s is %s, its owner %s", split.ID, got, want)
		}
	}
}

func TestUsageLabel(t *testing.T) {
	for _, c := range []struct {
		usage claude.Usage
		want  string
	}{
		{claude.Usage{}, ""},
		{claude.Usage{Model: "claude-opus-5-5", Context: 420_000, Cost: 1.2}, "42% · $1.20"},
		{claude.Usage{Model: "claude-mystery", Context: 5000, Cost: 0.001}, ""},
	} {
		if got := usageLabel(&store.Session{Usage: c.usage}); got != c.want {
			t.Errorf("usageLabel(%+v) = %q, want %q", c.usage, got, c.want)
		}
	}
}

func TestFilterNarrowsColumnsUntilCleared(t *testing.T) {
	m := testModel(120, 40, 2, 0)
	m.filterInput = textinput.New()
	m.snap.Tasks = []*store.Task{
		{ID: "a", Number: 1, Title: "Fix the login bug", ColumnID: "c0"},
		{ID: "b", Number: 2, Title: "Dark mode", Branch: "dark-mode-1234", ColumnID: "c0"},
		{ID: "c", Number: 3, Title: "Review", ColumnID: "c1", PR: &store.PR{Number: 42, Title: "Login page"}},
	}
	ids := func() string {
		var out []string
		for i := range m.columns() {
			for _, t := range m.tasksForColumn(i) {
				out = append(out, t.ID)
			}
		}
		return strings.Join(out, ",")
	}

	next, _ := m.updateBoard(key("/"))
	m = next.(Model)
	next, _ = m.updateFilter(key("LOGIN"))
	m = next.(Model)
	if got := ids(); got != "a,c" {
		t.Errorf("filter LOGIN shows %s, want a,c", got)
	}
	m.filterInput.SetValue("")
	next, _ = m.updateFilter(key("dark 1234"))
	m = next.(Model)
	next, _ = m.updateFilter(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if got := ids(); m.mode != modeBoard || got != "b" {
		t.Errorf("after enter: mode %v, cards %s, want the board showing b", m.mode, got)
	}
	next, _ = m.updateBoard(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if got := ids(); got != "a,b,c" {
		t.Errorf("esc left %s", got)
	}
}

func TestFilterModeKeepsCardsNavigable(t *testing.T) {
	m := testModel(120, 40, 1, 0)
	m.filterInput = textinput.New()
	m.snap.Tasks = []*store.Task{
		{ID: "a", Number: 1, Title: "Login one", ColumnID: "c0"},
		{ID: "b", Number: 2, Title: "Login two", ColumnID: "c0"},
		{ID: "c", Number: 3, Title: "Other", ColumnID: "c0"},
	}
	next, _ := m.updateBoard(key("/"))
	m = next.(Model)
	next, _ = m.updateFilter(key("login"))
	m = next.(Model)

	next, _ = m.updateFilter(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.mode != modeFilter || m.current().ID != "b" {
		t.Fatalf("down: mode %v, selected %s, want filter mode on b", m.mode, m.current().ID)
	}

	l := m.layout()
	x, y := l.cols[0].x+3, l.bodyY+columnHeaderHeight+1
	if h := l.hitTest(m, x, y); h.kind != hitCard || h.task.ID != "a" {
		t.Fatalf("hit at %d,%d = %+v, want card a", x, y, h)
	}
	next, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = next.(Model)
	if m.mode != modeBoard || m.current().ID != "a" || m.filter != "login" {
		t.Errorf("click: mode %v, selected %s, filter %q, want board on a keeping the filter", m.mode, m.current().ID, m.filter)
	}
}

func TestCardLinksItsPR(t *testing.T) {
	m := testModel(120, 40, 1, 0)
	url := "https://github.com/acme/app/pull/21"
	card := &store.Task{ID: "pr", Number: 3, Title: "PR", ColumnID: "c0",
		PR: &store.PR{Number: 21, URL: url, State: "OPEN", Branch: "a-long-branch-name"}}
	for _, width := range []int{60, 18} {
		got := m.renderCard(card, width, false, false)
		if open := strings.Count(got, ansi.SetHyperlink(url)); open != 1 {
			t.Errorf("width %d: %d hyperlinks opened:\n%q", width, open, got)
		}
		if closed := strings.Count(got, ansi.ResetHyperlink()); closed != 1 {
			t.Errorf("width %d: %d hyperlinks closed:\n%q", width, closed, got)
		}
	}
	linked := m.renderCard(card, 60, false, false)
	if !strings.Contains(linked, ansi.SetHyperlink(url)+"PR #21"+ansi.ResetHyperlink()) {
		t.Errorf("link doesn't wrap exactly the PR number:\n%q", linked)
	}
	card.PR.URL = ""
	plain := m.renderCard(card, 60, false, false)
	if strings.Contains(plain, "\x1b]8") {
		t.Errorf("card links a PR without a URL:\n%q", plain)
	}
	if ansi.Strip(linked) != ansi.Strip(plain) {
		t.Errorf("the link changes the visible card:\n%s\nvs\n%s", ansi.Strip(linked), ansi.Strip(plain))
	}
}
