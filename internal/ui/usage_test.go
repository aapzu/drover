package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/claude"
	"github.com/aapzu/drover/internal/store"
)

func TestUsageViewListsCostsAndTotal(t *testing.T) {
	m := realModel(t, "cheap", "pricey", "idle")
	withUsage := func(u claude.Usage) map[string]*store.Session {
		return map[string]*store.Session{store.MainSession: {Usage: u}}
	}
	m.snap.Tasks[0].Sessions = withUsage(claude.Usage{Model: "claude-opus-5-5", Context: 420_000, Cost: 1.2})
	m.snap.Tasks[1].Sessions = withUsage(claude.Usage{Model: "claude-opus-5-5", Context: 100_000, Cost: 3.5})

	m.snap.Tasks[2].GroupID = m.snap.Tasks[0].ID

	got := ansi.Strip(m.renderUsage())
	for _, want := range []string{"Total", "$4.70", "#1", "$1.20", "#2", "$3.50", "42%"} {
		if !strings.Contains(got, want) {
			t.Errorf("usage view misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "idle") {
		t.Errorf("usage view lists a split-off card under its owner's session:\n%s", got)
	}
	if strings.Index(got, "pricey") > strings.Index(got, "cheap") {
		t.Errorf("rows are not sorted by cost:\n%s", got)
	}
}

func TestUsageViewWithoutUsage(t *testing.T) {
	m := realModel(t, "a")
	if got := ansi.Strip(m.renderUsage()); !strings.Contains(got, "No usage recorded yet") {
		t.Errorf("empty usage view = %q", got)
	}
}
