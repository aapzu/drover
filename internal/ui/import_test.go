package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/engine"
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
