package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/store"
)

func TestBoardFooter(t *testing.T) {
	const hint = "ctrl+k commands · ? help"
	running := func(m *Model) {
		task := m.snap.Tasks[0]
		task.Branch = "aapeli/dx-1-foo"
		task.Sessions = map[string]*store.Session{store.MainSession: {
			Status: store.StatusRunning, Model: "opus", Effort: "high", PermissionMode: "acceptEdits",
		}}
	}
	tests := []struct {
		name          string
		width, tasks  int
		setup         func(m *Model)
		want, wantNot []string
	}{
		{"shows the selected card", 100, 1, running,
			[]string{"#1", "opus", "high", "acceptEdits", "aapeli/dx-1-foo", hint}, nil},
		{"hides an inherited permission mode", 100, 1, func(m *Model) {
			running(m)
			m.snap.Tasks[0].Sessions[store.MainSession].ModeInherited = true
		}, nil, []string{"acceptEdits"}},
		{"without a card keeps the hint", 100, 0, func(*Model) {}, []string{hint}, nil},
		{"stays one row and keeps the hint when narrow", 40, 1, func(m *Model) {
			m.snap.Tasks[0].Branch = "aapeli/dx-1234-a-very-long-branch-name-indeed"
		}, []string{"#1", "…", hint}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModel(tt.width, 30, 1, tt.tasks)
			m.mode = modeBoard
			tt.setup(&m)

			footer := m.renderFooter()
			if w := lipgloss.Width(footer); w > m.width {
				t.Errorf("footer is %d wide, terminal is %d", w, m.width)
			}
			plain := ansi.Strip(footer)
			for _, want := range tt.want {
				if !strings.Contains(plain, want) {
					t.Errorf("footer misses %q: %q", want, plain)
				}
			}
			for _, not := range tt.wantNot {
				if strings.Contains(plain, not) {
					t.Errorf("footer shows %q: %q", not, plain)
				}
			}
		})
	}
}
