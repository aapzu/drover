package claude

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestScanUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	line := func(id, model string, in, write, read, out int) string {
		return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}}`+"\n", id, model, in, write, read, out)
	}
	os.WriteFile(path, []byte(
		`{"type":"user","message":{"content":"hi"}}`+"\n"+
			line("a", "claude-opus-5-5", 1_000_000, 0, 0, 0)+
			line("b", "claude-opus-5-5", 0, 1_000_000, 0, 10)+
			line("b", "claude-opus-5-5", 0, 1_000_000, 0, 1_000_000)+
			`{"type":"assistant","message":{"id":"c","model":"<synthetic>","usage":{"input_tokens":5}}}`+"\n"), 0o644)

	var u Usage
	offset, err := ScanUsage(path, 0, &u)
	if err != nil {
		t.Fatal(err)
	}
	if want := 4.0 + 5 + 20; math.Abs(u.Cost-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", u.Cost, want)
	}
	if u.Context != 1_000_000 || u.Model != "claude-opus-5-5" || ContextWindow(u.Model) != 1_000_000 {
		t.Errorf("usage = %+v", u)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line("d", "claude-haiku-4-5-20251001", 0, 0, 150_000, 0))
	f.WriteString(line("e", "claude-unknown-9", 100, 0, 0, 100))
	f.Close()
	if _, err := ScanUsage(path, offset, &u); err != nil {
		t.Fatal(err)
	}
	if want := 29 + 0.015; math.Abs(u.Cost-want) > 1e-9 {
		t.Errorf("cost after more turns = %v, want %v", u.Cost, want)
	}
	if u.Context != 100 || ContextWindow(u.Model) != 0 {
		t.Errorf("unknown model: %+v", u)
	}
}

func TestScanUsageSkipsHistoryAForkCopies(t *testing.T) {
	dir := t.TempDir()
	line := func(id, at string) string {
		return `{"type":"assistant","timestamp":"` + at + `","message":{"id":"` + id + `","model":"claude-opus-5-5","usage":{"input_tokens":1000000}}}` + "\n"
	}
	old := line("a", "2026-10-21T10:00:00.000Z")
	os.WriteFile(filepath.Join(dir, "old.jsonl"), []byte(old), 0o644)
	os.WriteFile(filepath.Join(dir, "fork.jsonl"), []byte(old+line("b", "2026-10-21T11:00:00.000Z")), 0o644)

	var u Usage
	for _, name := range []string{"old.jsonl", "fork.jsonl"} {
		if _, err := ScanUsage(filepath.Join(dir, name), 0, &u); err != nil {
			t.Fatal(err)
		}
	}
	if math.Abs(u.Cost-8) > 1e-9 {
		t.Errorf("cost = %v, want $8 for two messages", u.Cost)
	}
}

func TestPriceOf(t *testing.T) {
	cases := []struct {
		model       string
		input, read float64
		window      int
		known       bool
	}{
		{"claude-fable-5-1-20260101", 10, 0.25, 1_000_000, true},
		{"claude-fable-5", 10, 1, 1_000_000, true},
		{"claude-opus-5-5", 4, 0.20, 1_000_000, true},
		{"claude-opus-5", 5, 0.50, 1_000_000, true},
		{"claude-haiku-4-5-20251001", 1, 0.10, 200_000, true},
		{"claude-unknown-9", 0, 0, 0, false},
		{"<synthetic>", 0, 0, 0, false},
		{"", 0, 0, 0, false},
	}
	for _, c := range cases {
		p, ok := priceOf(c.model)
		if ok != c.known || p.input != c.input || p.read != c.read || p.window != c.window {
			t.Errorf("priceOf(%q) = %+v, %v", c.model, p, ok)
		}
		if got := ContextWindow(c.model); got != c.window {
			t.Errorf("ContextWindow(%q) = %d, want %d", c.model, got, c.window)
		}
	}
}

func TestUsageAdd(t *testing.T) {
	msg := func(typ, id, model, at string, in, write, read, out int) string {
		return fmt.Sprintf(`{"type":%q,"timestamp":%q,"message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}}`, typ, at, id, model, in, write, read, out)
	}
	const opus = "claude-opus-5"
	cases := []struct {
		name        string
		start       Usage
		lines       []string
		cost        float64
		context     int
		model, last string
	}{
		{"cache write costs 1.25x input and reads their own rate", Usage{},
			[]string{msg("assistant", "a", opus, "t1", 1_000_000, 1_000_000, 1_000_000, 1_000_000)}, 5 + 6.25 + 0.5 + 25, 3_000_000, opus, "a"},
		{"user lines are ignored", Usage{}, []string{msg("user", "a", opus, "t1", 1_000_000, 0, 0, 0)}, 0, 0, "", ""},
		{"synthetic messages are ignored", Usage{}, []string{msg("assistant", "a", "<synthetic>", "t1", 1_000_000, 0, 0, 0)}, 0, 0, "", ""},
		{"malformed line is ignored", Usage{}, []string{"{not json"}, 0, 0, "", ""},
		{"a repeated message id replaces the earlier line", Usage{},
			[]string{msg("assistant", "a", opus, "t1", 1_000_000, 0, 0, 0), msg("assistant", "a", opus, "t2", 1_000_000, 0, 0, 1_000_000)}, 5 + 25, 1_000_000, opus, "a"},
		{"a different message id adds", Usage{},
			[]string{msg("assistant", "a", opus, "t1", 1_000_000, 0, 0, 0), msg("assistant", "b", opus, "t2", 1_000_000, 0, 0, 0)}, 10, 1_000_000, opus, "b"},
		{"lines older than Through are skipped", Usage{Through: "t5", Cost: 1},
			[]string{msg("assistant", "a", opus, "t4", 1_000_000, 0, 0, 0)}, 1, 0, "", ""},
		{"unknown model costs nothing but sets the context", Usage{},
			[]string{msg("assistant", "a", "claude-unknown-9", "t1", 100, 0, 0, 100)}, 0, 100, "claude-unknown-9", "a"},
	}
	for _, c := range cases {
		u := c.start
		for _, l := range c.lines {
			u.add([]byte(l))
		}
		if math.Abs(u.Cost-c.cost) > 1e-9 || u.Context != c.context || u.Model != c.model || u.LastID != c.last {
			t.Errorf("%s: usage = %+v, want cost %v, context %d, model %q, last %q", c.name, u, c.cost, c.context, c.model, c.last)
		}
	}
}
