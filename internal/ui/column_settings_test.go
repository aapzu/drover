package ui

import (
	"strings"
	"testing"
)

func TestColumnSettingsShowOnlyWhenEnabled(t *testing.T) {
	m := testModel(120, 40, 2, 3)
	col := m.columns()[0]
	box := colBox{index: 0, width: 40, slots: 5}

	if out := m.renderColumn(box, 20, false); strings.Contains(out, columnSummary(col)) {
		t.Error("column rendered its settings while hidden")
	}
	m.prefs.ShowSettings = true
	if out := m.renderColumn(box, 20, false); !strings.Contains(out, columnSummary(col)) {
		t.Error("column didn't render its settings once enabled")
	}
}
