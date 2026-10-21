package engine

import (
	"testing"

	"github.com/aapzu/drover/internal/config"
)

func TestWorkColumn(t *testing.T) {
	plan := config.Column{ID: "plan", AutoSpawn: true, PermissionMode: "plan"}
	plan2 := config.Column{ID: "plan2", AutoSpawn: true, PermissionMode: "plan"}
	exec := config.Column{ID: "exec", AutoSpawn: true}
	todo := config.Column{ID: "todo", Role: "todo", AutoSpawn: true}
	review := config.Column{ID: "review"}
	cases := []struct {
		name  string
		board *config.Board
		want  string
	}{
		{"default board", config.Default(), "executing"},
		{"skips plan columns", &config.Board{Columns: []config.Column{todo, plan, plan2, exec}}, "exec"},
		{"all plan", &config.Board{Columns: []config.Column{todo, plan, plan2}}, ""},
		{"board plan mode", &config.Board{DefaultPermissionMode: "plan", Columns: []config.Column{todo, exec}}, ""},
		{"no agent column", &config.Board{Columns: []config.Column{todo, review}}, ""},
	}
	for _, c := range cases {
		got, ok := workColumn(c.board)
		if got.ID != c.want || ok != (c.want != "") {
			t.Errorf("%s: workColumn = %q, %v, want %q", c.name, got.ID, ok, c.want)
		}
	}
}

func TestFirstAgentColumn(t *testing.T) {
	plan := config.Column{ID: "plan", AutoSpawn: true, PermissionMode: "plan"}
	todo := config.Column{ID: "todo", Role: "todo", AutoSpawn: true}
	review := config.Column{ID: "review"}
	cases := []struct {
		name  string
		board *config.Board
		want  string
	}{
		{"default board", config.Default(), "planning"},
		{"skips non-agent columns", &config.Board{Columns: []config.Column{todo, review, plan}}, "plan"},
		{"no agent column", &config.Board{Columns: []config.Column{todo, review}}, ""},
	}
	for _, c := range cases {
		got, ok := firstAgentColumn(c.board)
		if got.ID != c.want || ok != (c.want != "") {
			t.Errorf("%s: firstAgentColumn = %q, %v, want %q", c.name, got.ID, ok, c.want)
		}
	}
}
