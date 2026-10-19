package engine

import (
	"testing"

	"github.com/aapzu/drover/internal/config"
)

func TestMatchFinalLine(t *testing.T) {
	cases := []struct {
		reply, pattern string
		want           bool
	}{
		{"All done.\n\nFindings: applied", "Findings: applied", true},
		{"All done.\n\n**Findings: applied**\n\n", "Findings: applied", true},
		{"Summary\nVerdict: Blocked.", "Verdict: Blocked", true},
		{"Summary\nVerdict: Ready", "Verdict: Blocked", false},
		{"PR: ready to merge (#42)", "PR: ready to merge*", true},
		{"Findings: applied\nbut then more text", "Findings: applied", false},
		{"verdict:   READY\n---\n", "Verdict: Ready", true},
		{"", "anything", false},
	}
	for _, c := range cases {
		if got := MatchFinalLine(c.reply, c.pattern); got != c.want {
			t.Errorf("MatchFinalLine(%q, %q) = %v, want %v", c.reply, c.pattern, got, c.want)
		}
	}
}

func TestWorkColumn(t *testing.T) {
	plan := config.Column{ID: "plan", AutoSpawn: true, PermissionMode: "plan"}
	plan2 := config.Column{ID: "plan2", AutoSpawn: true, PermissionMode: "plan"}
	exec := config.Column{ID: "exec", AutoSpawn: true}
	isolated := config.Column{ID: "isolated", AutoSpawn: true, SessionTarget: "isolated"}
	todo := config.Column{ID: "todo", Role: "todo", AutoSpawn: true}
	review := config.Column{ID: "review"}
	cases := []struct {
		name  string
		board *config.Board
		want  string
	}{
		{"default board", config.Default(), "executing"},
		{"skips plan and isolated columns", &config.Board{Columns: []config.Column{todo, plan, plan2, isolated, exec}}, "exec"},
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
	isolated := config.Column{ID: "isolated", AutoSpawn: true, SessionTarget: "isolated"}
	todo := config.Column{ID: "todo", Role: "todo", AutoSpawn: true}
	review := config.Column{ID: "review"}
	cases := []struct {
		name  string
		board *config.Board
		want  string
	}{
		{"default board", config.Default(), "planning"},
		{"skips isolated columns", &config.Board{Columns: []config.Column{todo, review, isolated, plan}}, "plan"},
		{"no agent column", &config.Board{Columns: []config.Column{todo, review}}, ""},
	}
	for _, c := range cases {
		got, ok := firstAgentColumn(c.board)
		if got.ID != c.want || ok != (c.want != "") {
			t.Errorf("%s: firstAgentColumn = %q, %v, want %q", c.name, got.ID, ok, c.want)
		}
	}
}
