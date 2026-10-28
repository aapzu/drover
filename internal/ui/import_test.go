package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/linear"
)

func importModel(more bool, sessions ...claude.Found) Model {
	m := testModel(120, 40, 1, 0)
	m.eng = &engine.Engine{Root: "/repo"}
	m.mode = modeImport
	m.imp = newImportState()
	m.imp.gen = 1
	return m.updateImportLoaded(sessionsLoadedMsg{gen: 1, sessions: sessions, more: more})
}

func selectedIDs(items []importItem) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		if it.session != nil {
			out[it.session.SessionID] = it.selected
		}
	}
	return out
}

func press(m Model, k string) (Model, tea.Cmd) {
	next, cmd := m.updateImport(key(k))
	return next.(Model), cmd
}

func TestLoadedSessionsAreGroupedAndStaleLoadsIgnored(t *testing.T) {
	m := importModel(true,
		claude.Found{SessionID: "a", Cwd: "/repo/x"},
		claude.Found{SessionID: "b", Cwd: "/repo/y"},
	)
	if len(m.imp.items) != 4 || m.imp.items[0].header != "x" || !m.imp.more {
		t.Fatalf("items = %+v, more = %v", m.imp.items, m.imp.more)
	}
	m = m.updateImportLoaded(sessionsLoadedMsg{gen: 0, sessions: []claude.Found{{SessionID: "z", Cwd: "/repo"}}})
	if len(m.imp.items) != 4 {
		t.Errorf("a stale load replaced the list: %+v", m.imp.items)
	}
}

func TestLoadMoreKeepsSelectionAndCursor(t *testing.T) {
	m := importModel(true,
		claude.Found{SessionID: "a", Cwd: "/repo/x"},
		claude.Found{SessionID: "b", Cwd: "/repo/x"},
		claude.Found{SessionID: "c", Cwd: "/repo/y"},
	)
	m.imp.items[1].selected = true
	m.imp.cursor = 4

	m, cmd := press(m, "m")
	if cmd == nil || m.imp.limit != 2*importPageSize || !m.imp.loading {
		t.Fatalf("m did not start a load: cmd=%v limit=%d loading=%v", cmd != nil, m.imp.limit, m.imp.loading)
	}
	if _, again := press(m, "m"); again != nil {
		t.Error("m while loading started a second load")
	}

	m = m.updateImportLoaded(sessionsLoadedMsg{gen: m.imp.gen, sessions: []claude.Found{
		{SessionID: "new", Cwd: "/repo/z", Running: true},
		{SessionID: "c", Cwd: "/repo/y"},
		{SessionID: "a", Cwd: "/repo/x"},
		{SessionID: "b", Cwd: "/repo/x"},
		{SessionID: "old", Cwd: "/repo/x"},
	}})
	got := selectedIDs(m.imp.items)
	if !got["a"] || got["b"] || got["c"] || got["old"] || !got["new"] {
		t.Errorf("selection after reload = %v", got)
	}
	if it := m.imp.items[m.imp.cursor]; it.session == nil || it.session.SessionID != "c" {
		t.Errorf("cursor on %+v, want session c", it)
	}
	if m.imp.more {
		t.Error("more should follow the last load")
	}
}

func TestLoadMoreOnlyWhenThereIsMore(t *testing.T) {
	m := importModel(false, claude.Found{SessionID: "a", Cwd: "/repo"})
	if m, cmd := press(m, "m"); cmd != nil || m.imp.limit != importPageSize {
		t.Error("m loaded with nothing more to load")
	}
	m = importModel(true, claude.Found{SessionID: "a", Cwd: "/repo"})
	m.imp.source = sourceLinear
	if _, cmd := press(m, "m"); cmd != nil {
		t.Error("m loaded on the Linear tab")
	}
}

func TestSelectAllSkipsHeaders(t *testing.T) {
	m := importModel(false,
		claude.Found{SessionID: "a", Cwd: "/repo/x"},
		claude.Found{SessionID: "b", Cwd: "/repo/y"},
	)
	m, _ = press(m, "a")
	for _, it := range m.imp.items {
		if it.header != "" && it.selected {
			t.Fatal("a selected a header")
		}
	}
	if got := selectedIDs(m.imp.items); !got["a"] || !got["b"] {
		t.Fatalf("a did not select every session: %v", got)
	}
	m, _ = press(m, "a")
	if got := selectedIDs(m.imp.items); got["a"] || got["b"] {
		t.Errorf("second a did not clear: %v", got)
	}
}

func TestSpaceOnHeaderTogglesItsGroup(t *testing.T) {
	m := importModel(false,
		claude.Found{SessionID: "a", Cwd: "/repo/x"},
		claude.Found{SessionID: "b", Cwd: "/repo/y"},
	)
	m.imp.cursor = 2
	m, _ = press(m, " ")
	if got := selectedIDs(m.imp.items); got["a"] || !got["b"] || m.imp.items[2].selected {
		t.Errorf("space on a header: sessions %v, header selected %v", got, m.imp.items[2].selected)
	}
	m, _ = press(m, " ")
	if got := selectedIDs(m.imp.items); got["a"] || got["b"] {
		t.Errorf("second space did not clear the group: %v", got)
	}
}

func TestGroupSessionsByDirectory(t *testing.T) {
	sessions := []claude.Found{
		{SessionID: "a", Cwd: "/repo/.worktrees/x", Running: true},
		{SessionID: "b", Cwd: "/repo"},
		{SessionID: "c", Cwd: "/repo/.worktrees/x"},
		{SessionID: "d", Cwd: "/repo/.worktrees/y"},
	}
	items := groupSessions(sessions, "/repo", map[string]bool{"d": true, "c": false})

	var got []string
	for _, it := range items {
		if it.session != nil {
			got = append(got, it.session.SessionID)
		} else {
			got = append(got, "["+it.header+"]")
		}
	}
	want := []string{"[.worktrees/x]", "a", "c", "[repo root]", "b", "[.worktrees/y]", "d"}
	if len(got) != len(want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("items = %v, want %v", got, want)
		}
	}
	if selected := selectedIDs(items); !selected["a"] || selected["b"] || selected["c"] || !selected["d"] {
		t.Errorf("selection = %v: running and kept ones should be selected", selected)
	}
}

// importOn is importModel on a real engine, so importing really adds cards.
func importOn(t *testing.T, sessions ...claude.Found) Model {
	t.Helper()
	m := importModel(false, sessions...)
	eng, err := engine.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.eng = eng
	m.snap = eng.Snapshot()
	return m
}

func session(id, dir string) claude.Found {
	return claude.Found{SessionID: id, Cwd: dir, Title: "title " + id, Branch: "br-" + id, Updated: time.Now()}
}

func issue(id string) linear.Issue {
	i := linear.Issue{ID: "id-" + id, Identifier: id, Title: "issue " + id, BranchName: "feat/" + id}
	i.State.Name = "Todo"
	return i
}

// linearModel is importOn with a Linear key in the environment, so the Linear tab works. The
// key is fake: nothing here runs a command that would use it.
func linearModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("LINEAR_API_KEY", "test-key")
	m := importOn(t)
	m.imp.source = sourceLinear
	return m
}

func TestImportCursorStaysInTheList(t *testing.T) {
	m := importModel(false, claude.Found{SessionID: "a", Cwd: "/repo/x"}, claude.Found{SessionID: "b", Cwd: "/repo/y"})
	for _, tt := range []struct {
		keys []string
		want int
	}{
		{[]string{"up", "k"}, 0},
		{[]string{"j"}, 1},
		{[]string{"down", "down"}, 2},
		{[]string{"j", "j", "j", "j", "j"}, 3},
		{[]string{"j", "j", "k"}, 1},
	} {
		got := m
		for _, k := range tt.keys {
			next, _ := got.updateImport(keyNamed(k))
			got = next.(Model)
		}
		if got.imp.cursor != tt.want {
			t.Errorf("%v: cursor %d, want %d", tt.keys, got.imp.cursor, tt.want)
		}
	}

	empty := importModel(false)
	if next, _ := empty.updateImport(keyNamed("down")); next.(Model).imp.cursor != 0 {
		t.Error("down moved the cursor in an empty list")
	}
}

func keyNamed(k string) tea.KeyMsg {
	switch k {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return key(k)
}

func TestImportKeysSelectOneSession(t *testing.T) {
	for _, k := range []string{" ", "x"} {
		m := importModel(false, claude.Found{SessionID: "a", Cwd: "/repo/x"})
		m.imp.cursor = 1
		m, _ = press(m, k)
		if !selectedIDs(m.imp.items)["a"] {
			t.Errorf("%q did not select the session", k)
		}
		m, _ = press(m, k)
		if selectedIDs(m.imp.items)["a"] {
			t.Errorf("second %q did not clear it", k)
		}
	}
}

func TestImportClosesOnEscAndQ(t *testing.T) {
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, key("q")} {
		m := importModel(false)
		next, cmd := m.updateImport(k)
		if next.(Model).mode != modeBoard || cmd != nil {
			t.Errorf("%q: mode %v", k.String(), next.(Model).mode)
		}
	}
}

func TestImportTabSwitchesSource(t *testing.T) {
	m := importModel(false, claude.Found{SessionID: "a", Cwd: "/repo/x"})
	m, cmd := pressKey(m, tea.KeyTab)
	if m.imp.source != sourceLinear || cmd != nil || m.imp.loading || !strings.Contains(m.imp.err, "LINEAR_API_KEY") || len(m.imp.items) != 0 {
		t.Fatalf("to Linear: source %d err %q items %d loading %v", m.imp.source, m.imp.err, len(m.imp.items), m.imp.loading)
	}
	gen := m.imp.gen
	m, cmd = pressKey(m, tea.KeyShiftTab)
	if m.imp.source != sourceClaude || cmd == nil || !m.imp.loading || m.imp.err != "" || m.imp.gen != gen+1 {
		t.Errorf("back to Claude: source %d cmd nil %v loading %v err %q", m.imp.source, cmd == nil, m.imp.loading, m.imp.err)
	}
}

func pressKey(m Model, k tea.KeyType) (Model, tea.Cmd) {
	next, cmd := m.updateImport(tea.KeyMsg{Type: k})
	return next.(Model), cmd
}

func TestImportEnterImportsTheSessionUnderTheCursor(t *testing.T) {
	dir := t.TempDir()
	m := importOn(t, session("a", dir), session("b", dir))
	m.imp.cursor = 2
	m, _ = pressKey(m, tea.KeyEnter)
	if m.mode != modeBoard || m.flash != "Imported 1 card(s)" || len(m.snap.Tasks) != 1 || m.snap.Tasks[0].Title != "title b" {
		t.Fatalf("mode %v flash %q tasks %d", m.mode, m.flash, len(m.snap.Tasks))
	}
	if cur := m.current(); cur == nil || cur.Title != "title b" {
		t.Errorf("the imported card is not selected: %v", cur)
	}
}

func TestImportEnterImportsOnlyWhatIsSelected(t *testing.T) {
	dir := t.TempDir()
	m := importOn(t, session("a", dir), session("b", dir), session("c", dir))
	m.imp.items[1].selected, m.imp.items[3].selected = true, true
	m.imp.cursor = 2
	m, _ = pressKey(m, tea.KeyEnter)
	if m.flash != "Imported 2 card(s)" || len(m.snap.Tasks) != 2 {
		t.Fatalf("flash %q tasks %d", m.flash, len(m.snap.Tasks))
	}
	for _, task := range m.snap.Tasks {
		if task.Title == "title b" {
			t.Error("the card under the cursor was imported although others were selected")
		}
	}
}

func TestImportEnterTellsWhenEverythingIsAlreadyOnTheBoard(t *testing.T) {
	dir := t.TempDir()
	m := importOn(t, session("a", dir))
	m.imp.cursor = 1
	m, _ = pressKey(m, tea.KeyEnter)
	if len(m.snap.Tasks) != 1 {
		t.Fatalf("tasks = %d", len(m.snap.Tasks))
	}

	again := m
	again.mode, again.flash = modeImport, ""
	again.imp.cursor = 1
	again, _ = pressKey(again, tea.KeyEnter)
	if again.flash != "Already on the board" || len(again.snap.Tasks) != 1 || again.mode != modeBoard {
		t.Errorf("flash %q tasks %d mode %v", again.flash, len(again.snap.Tasks), again.mode)
	}
}

func TestImportEnterOnAHeaderOrNothingImportsNothing(t *testing.T) {
	m := importOn(t, session("a", t.TempDir()))
	m, _ = pressKey(m, tea.KeyEnter)
	if m.mode != modeBoard || m.flash != "" || len(m.snap.Tasks) != 0 {
		t.Errorf("header: mode %v flash %q tasks %d", m.mode, m.flash, len(m.snap.Tasks))
	}
}

func TestLinearImportNeedsAKey(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")
	m := importOn(t)
	m.imp.source = sourceLinear
	m, _ = press(m, "/")
	if m.imp.keyEntry {
		t.Error("/ opened the key entry without a Linear key")
	}
	m = m.updateImportLoaded(issuesLoadedMsg{gen: m.imp.gen, issues: []linear.Issue{issue("DX-1")}})
	if got := ansi.Strip(m.renderImport(40)); !strings.Contains(got, "DX-1") {
		t.Errorf("issues are not listed:\n%s", got)
	}
}

func TestLinearImportLoadsListsAndImportsIssues(t *testing.T) {
	m := linearModel(t)
	m.mode = modeBoard
	cmd := m.openImport(sourceLinear)
	if cmd == nil || !m.imp.loading || m.mode != modeImport || m.imp.err != "" {
		t.Fatalf("cmd nil %v loading %v mode %v err %q", cmd == nil, m.imp.loading, m.mode, m.imp.err)
	}

	m = m.updateImportLoaded(issuesLoadedMsg{gen: m.imp.gen - 1, issues: []linear.Issue{issue("OLD-1")}})
	if len(m.imp.items) != 0 || !m.imp.loading {
		t.Fatalf("a stale load changed the list: %+v", m.imp.items)
	}
	m = m.updateImportLoaded(issuesLoadedMsg{gen: m.imp.gen, issues: []linear.Issue{issue("DX-1"), issue("DX-2")}})
	if len(m.imp.items) != 2 || m.imp.loading || m.imp.items[0].selected {
		t.Fatalf("items %+v loading %v", m.imp.items, m.imp.loading)
	}

	m, _ = press(m, "j")
	m, _ = pressKey(m, tea.KeyEnter)
	if m.flash != "Imported 1 card(s)" || len(m.snap.Tasks) != 1 {
		t.Fatalf("flash %q tasks %d", m.flash, len(m.snap.Tasks))
	}
	got := m.snap.Tasks[0]
	if got.Issue == nil || got.Issue.Key != "DX-2" || got.Title != "issue DX-2" {
		t.Errorf("imported %+v", got)
	}
}

func TestLinearImportShowsLoadErrors(t *testing.T) {
	m := linearModel(t)
	m.imp.items = []importItem{{issue: &linear.Issue{Identifier: "DX-1"}}}
	m.imp.gen = 3
	m = m.updateImportLoaded(issuesLoadedMsg{gen: 3, err: errors.New("401 unauthorized")})
	if m.imp.err != "401 unauthorized" || m.imp.loading || len(m.imp.items) != 0 {
		t.Errorf("err %q items %d", m.imp.err, len(m.imp.items))
	}
	if got := ansi.Strip(m.renderImport(40)); !strings.Contains(got, "401 unauthorized") {
		t.Errorf("the error is not shown:\n%s", got)
	}
	m = m.updateImportLoaded(issuesLoadedMsg{gen: 3, issues: []linear.Issue{issue("DX-1")}})
	if m.imp.err != "" {
		t.Errorf("a good load kept the error %q", m.imp.err)
	}
}

func TestLinearImportFetchesAnIssueByKey(t *testing.T) {
	m := linearModel(t)
	m.imp.items = []importItem{{issue: &linear.Issue{Identifier: "DX-1"}}}
	m, _ = press(m, "/")
	if !m.imp.keyEntry {
		t.Fatal("/ did not open the key entry")
	}
	m, _ = press(m, "DX-9")
	if m.imp.keyInput.Value() != "DX-9" {
		t.Fatalf("typed %q", m.imp.keyInput.Value())
	}
	if got := ansi.Strip(m.renderImport(40)); !strings.Contains(got, "Issue › ") || !strings.Contains(got, "DX-9") {
		t.Errorf("key entry is not shown:\n%s", got)
	}

	closed, cmd := pressKey(m, tea.KeyEsc)
	if closed.imp.keyEntry || closed.mode != modeImport || cmd != nil {
		t.Errorf("esc: keyEntry %v mode %v", closed.imp.keyEntry, closed.mode)
	}

	gen := m.imp.gen
	m, cmd = pressKey(m, tea.KeyEnter)
	if m.imp.keyEntry || !m.imp.loading || m.imp.gen != gen+1 || cmd == nil {
		t.Fatalf("enter: keyEntry %v loading %v gen %d cmd nil %v", m.imp.keyEntry, m.imp.loading, m.imp.gen, cmd == nil)
	}

	m = m.updateImportLoaded(issuesLoadedMsg{gen: m.imp.gen, issues: []linear.Issue{issue("DX-9")}, single: true})
	if len(m.imp.items) != 2 || !m.imp.items[1].selected || m.imp.cursor != 1 || m.imp.loading {
		t.Errorf("items %d cursor %d loading %v", len(m.imp.items), m.imp.cursor, m.imp.loading)
	}
}

func TestImportLoadsOnlyWhileImporting(t *testing.T) {
	m := importModel(false)
	m.mode = modeBoard
	m = m.updateImportLoaded(sessionsLoadedMsg{gen: 1, sessions: []claude.Found{{SessionID: "a", Cwd: "/repo"}}})
	m = m.updateImportLoaded(issuesLoadedMsg{gen: 1, issues: []linear.Issue{issue("DX-1")}})
	if len(m.imp.items) != 0 {
		t.Errorf("a result that arrived after closing filled the list: %+v", m.imp.items)
	}
}

func TestUpdateRoutesImportLoadsAndKeys(t *testing.T) {
	m := importModel(false)
	m, _ = update(m, sessionsLoadedMsg{gen: 1, sessions: []claude.Found{{SessionID: "a", Cwd: "/repo"}}})
	if len(m.imp.items) != 2 {
		t.Fatalf("items = %d", len(m.imp.items))
	}
	m, _ = update(m, key("j"))
	if m.imp.cursor != 1 {
		t.Errorf("cursor = %d", m.imp.cursor)
	}
}

func TestAgo(t *testing.T) {
	for _, tt := range []struct {
		age  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5*time.Minute + time.Second, "5m ago"},
		{3*time.Hour + time.Second, "3h ago"},
		{47 * time.Hour, "47h ago"},
		{72*time.Hour + time.Second, "3d ago"},
	} {
		if got := ago(time.Now().Add(-tt.age)); got != tt.want {
			t.Errorf("%v: %q, want %q", tt.age, got, tt.want)
		}
	}
}

func TestRenderImport(t *testing.T) {
	m := importModel(true,
		claude.Found{SessionID: "a", Cwd: "/repo/x", Title: "busy one", Branch: "br-a", Running: true, Busy: true},
		claude.Found{SessionID: "b", Cwd: "/repo/x", Title: "idle one", Branch: "br-b", Running: true},
		claude.Found{SessionID: "c", Cwd: "/repo", Title: "old one", Branch: "br-c", Updated: time.Now().Add(-2 * time.Hour)},
	)
	got := ansi.Strip(m.renderImport(40))
	for _, want := range []string{
		"Claude sessions", "Linear", "[x] x", "[ ] repo root", "busy one", "● running", "● running · idle", "2h ago", "br-c",
		"m load more", "Running agents keep running where they are",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("import view misses %q:\n%s", want, got)
		}
	}
	if w := lipgloss.Width(m.renderImport(40)); w > m.width {
		t.Errorf("import view is %d wide on a %d wide screen", w, m.width)
	}

	m.imp.items[1].selected, m.imp.items[2].selected = true, false
	if got := ansi.Strip(m.renderImport(40)); !strings.Contains(got, "[-] x") {
		t.Errorf("a partly selected group should show [-]:\n%s", got)
	}
}

func TestRenderImportStates(t *testing.T) {
	m := importModel(false)
	tests := []struct {
		name  string
		setup func(m *Model)
		want  string
	}{
		{"no sessions", func(*Model) {}, "No running agents"},
		{"loading", func(m *Model) { m.imp.loading = true }, "loading…"},
		{"error", func(m *Model) { m.imp.err = "it broke" }, "it broke"},
		{"no issues", func(m *Model) { m.imp.source = sourceLinear }, "No open issues assigned to you"},
		{"linear hint", func(m *Model) { m.imp.source = sourceLinear }, "/ fetch by key"},
		{"no load more", func(*Model) {}, "space select · a all · enter import"},
		{"linear issues", func(m *Model) {
			m.imp.source = sourceLinear
			i := issue("DX-5")
			m.imp.items = []importItem{{issue: &i, selected: true}}
		}, "[x] DX-5 issue DX-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m
			tt.setup(&got)
			if out := ansi.Strip(got.renderImport(40)); !strings.Contains(out, tt.want) {
				t.Errorf("view misses %q:\n%s", tt.want, out)
			}
		})
	}
}

func TestRenderImportScrollsToTheCursor(t *testing.T) {
	var sessions []claude.Found
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		sessions = append(sessions, claude.Found{SessionID: id, Cwd: "/repo", Title: "title " + id})
	}
	m := importModel(false, sessions...)
	m.imp.cursor = len(m.imp.items) - 1
	got := ansi.Strip(m.renderImport(15))
	if !strings.Contains(got, "▸ [ ] title h") || strings.Contains(got, "title a") {
		t.Errorf("the list doesn't follow the cursor:\n%s", got)
	}
}

func TestViewShowsTheImportModal(t *testing.T) {
	m := importModel(false)
	m.width, m.height = 120, 40
	m.eng = &engine.Engine{Root: "/repo", ProjectName: "repo"}
	view := m.View()
	if !strings.Contains(view, "Claude sessions") {
		t.Errorf("view:\n%s", view)
	}
	fits(t, "import", view, 120, 40)
}
