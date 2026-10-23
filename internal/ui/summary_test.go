package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aapzu/drover/internal/store"
)

func TestSummaryLine(t *testing.T) {
	if got := summaryLine(nil, 20); got != "" {
		t.Fatalf("no session: got %q", got)
	}
	if got := summaryLine(&store.Session{}, 20); got != "" {
		t.Fatalf("no summary: got %q", got)
	}
	long := &store.Session{Summary: strings.Repeat("word ", 20)}
	got := ansi.Strip(summaryLine(long, 12))
	if w := ansi.StringWidth(got); w != 12 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long summary: got %q (width %d), want a 12-cell line ending in …", got, w)
	}
}
