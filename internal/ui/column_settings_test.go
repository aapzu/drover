package ui

import (
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/engine"
)

func TestColumnSettingsHiddenUntilToggled(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := testModel(120, 40, 2, 3)
	m.eng = &engine.Engine{Root: t.TempDir()}
	col := m.columns()[0]

	box := colBox{index: 0, width: 40, slots: 5}
	if out := m.renderColumn(box, 20, false); strings.Contains(out, columnSummary(col)) {
		t.Error("column rendered its settings while hidden")
	}

	next, _ := m.updateBoard(key("t"))
	m = next.(Model)
	if !m.prefs.ShowSettings {
		t.Fatal("t didn't show column settings")
	}
	if out := m.renderColumn(box, 20, false); !strings.Contains(out, columnSummary(col)) {
		t.Error("column didn't render its settings after t")
	}
	if !loadPrefs(m.eng.Root).ShowSettings {
		t.Error("ShowSettings wasn't saved to ui.json")
	}

	next, _ = m.updateBoard(key("t"))
	m = next.(Model)
	if m.prefs.ShowSettings || loadPrefs(m.eng.Root).ShowSettings {
		t.Error("second t didn't hide column settings again")
	}
}
