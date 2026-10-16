package engine

import "testing"

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
