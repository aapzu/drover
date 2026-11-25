package claude

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const opus = "claude-opus-5"

func usageLine(typ, id, model, at string, in, write, read, out int) string {
	return fmt.Sprintf(`{"type":%q,"timestamp":%q,"message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}}`, typ, at, id, model, in, write, read, out)
}

// millionInputs is what one million input tokens cost, so the tests below
// state costs as multiples of it instead of repeating the price list.
func millionInputs(t *testing.T) float64 {
	t.Helper()
	var u Usage
	u.add([]byte(usageLine("assistant", "unit", opus, "t0", 1_000_000, 0, 0, 0)))
	if u.Cost <= 0 {
		t.Fatalf("%s has no price", opus)
	}
	return u.Cost
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScanUsageReadsOnlyWhatIsNew(t *testing.T) {
	unit := millionInputs(t)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	line := func(id string, in int) string { return usageLine("assistant", id, opus, "", in, 0, 0, 0) + "\n" }
	os.WriteFile(path, []byte(`{"type":"user","message":{"content":"hi"}}`+"\n"+line("a", 1_000_000)), 0o644)

	var u Usage
	offset, err := ScanUsage(path, 0, &u)
	if err != nil || !near(u.Cost, unit) {
		t.Fatalf("first scan: cost %v (want %v), err %v", u.Cost, unit, err)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line("b", 1_000_000))
	f.WriteString(line("c", 100) + `{"type":"assistant","message":{"id":"d","model":"claude-opus-5","usage":{"input_tok`)
	f.Close()
	offset, err = ScanUsage(path, offset, &u)
	if err != nil || !near(u.Cost, 2*unit+unit/10_000) {
		t.Errorf("second scan: cost %v, err %v; want only the new complete lines added", u.Cost, err)
	}
	if again, _ := ScanUsage(path, offset, &u); again != offset || !near(u.Cost, 2*unit+unit/10_000) {
		t.Errorf("a third scan with nothing new moved the offset to %d or the cost to %v", again, u.Cost)
	}
}

func TestScanUsageSkipsHistoryAForkCopies(t *testing.T) {
	unit := millionInputs(t)
	dir := t.TempDir()
	line := func(id, at string) string { return usageLine("assistant", id, opus, at, 1_000_000, 0, 0, 0) + "\n" }
	old := line("a", "2026-10-21T10:00:00.000Z")
	os.WriteFile(filepath.Join(dir, "old.jsonl"), []byte(old), 0o644)
	os.WriteFile(filepath.Join(dir, "fork.jsonl"), []byte(old+line("b", "2026-10-21T11:00:00.000Z")), 0o644)

	var u Usage
	for _, name := range []string{"old.jsonl", "fork.jsonl"} {
		if _, err := ScanUsage(filepath.Join(dir, name), 0, &u); err != nil {
			t.Fatal(err)
		}
	}
	if !near(u.Cost, 2*unit) {
		t.Errorf("cost = %v, want %v for two messages, not three", u.Cost, 2*unit)
	}
}

func TestPricesMatchTheMostSpecificModelFirst(t *testing.T) {
	for i, specific := range prices {
		for _, general := range prices[:i] {
			if strings.HasPrefix(specific.prefix, general.prefix) {
				t.Errorf("%q is listed after %q, which matches it first and gives it the wrong price", specific.prefix, general.prefix)
			}
		}
	}
}

func TestContextWindow(t *testing.T) {
	for model, known := range map[string]bool{
		"claude-haiku-4-5-20251001": true,
		opus + "-20260101":          true,
		"claude-unknown-9":          false,
		"<synthetic>":               false,
		"":                          false,
	} {
		if got := ContextWindow(model); (got > 0) != known {
			t.Errorf("ContextWindow(%q) = %d, want known: %v", model, got, known)
		}
	}
}

func TestUsageAdd(t *testing.T) {
	unit := millionInputs(t)
	cases := []struct {
		name        string
		start       Usage
		lines       []string
		cost        float64
		context     int
		model, last string
	}{
		{"cache writes cost 1.25x input", Usage{},
			[]string{usageLine("assistant", "a", opus, "t1", 0, 1_000_000, 0, 0)}, 1.25 * unit, 1_000_000, opus, "a"},
		{"user lines are ignored", Usage{}, []string{usageLine("user", "a", opus, "t1", 1_000_000, 0, 0, 0)}, 0, 0, "", ""},
		{"synthetic messages are ignored", Usage{}, []string{usageLine("assistant", "a", "<synthetic>", "t1", 1_000_000, 0, 0, 0)}, 0, 0, "", ""},
		{"malformed line is ignored", Usage{}, []string{"{not json"}, 0, 0, "", ""},
		{"a repeated message id replaces the earlier line", Usage{},
			[]string{usageLine("assistant", "a", opus, "t1", 1_000_000, 0, 0, 0), usageLine("assistant", "a", opus, "t2", 2_000_000, 0, 0, 0)}, 2 * unit, 2_000_000, opus, "a"},
		{"a different message id adds", Usage{},
			[]string{usageLine("assistant", "a", opus, "t1", 1_000_000, 0, 0, 0), usageLine("assistant", "b", opus, "t2", 1_000_000, 0, 0, 0)}, 2 * unit, 1_000_000, opus, "b"},
		{"lines older than Through are skipped", Usage{Through: "t5", Cost: 1},
			[]string{usageLine("assistant", "a", opus, "t4", 1_000_000, 0, 0, 0)}, 1, 0, "", ""},
		{"an unknown model costs nothing but sets the context", Usage{},
			[]string{usageLine("assistant", "a", "claude-unknown-9", "t1", 100, 0, 0, 100)}, 0, 100, "claude-unknown-9", "a"},
	}
	for _, c := range cases {
		u := c.start
		for _, l := range c.lines {
			u.add([]byte(l))
		}
		if !near(u.Cost, c.cost) || u.Context != c.context || u.Model != c.model || u.LastID != c.last {
			t.Errorf("%s: usage = %+v, want cost %v, context %d, model %q, last %q", c.name, u, c.cost, c.context, c.model, c.last)
		}
	}
}

func TestUsageCostsOrderByTokenKind(t *testing.T) {
	cost := func(in, write, read, out int) float64 {
		var u Usage
		u.add([]byte(usageLine("assistant", "a", opus, "t1", in, write, read, out)))
		return u.Cost
	}
	const n = 1_000_000
	if read, input, write, output := cost(0, 0, n, 0), cost(n, 0, 0, 0), cost(0, n, 0, 0), cost(0, 0, 0, n); !(0 < read && read < input && input < write && write < output) {
		t.Errorf("per-million costs: cache read %v, input %v, cache write %v, output %v; want them increasing in that order", read, input, write, output)
	}
}

func TestUsageContextIsEveryKindOfInput(t *testing.T) {
	var u Usage
	u.add([]byte(usageLine("assistant", "a", opus, "t1", 1, 20, 300, 4000)))
	if u.Context != 321 {
		t.Errorf("Context = %d, want the input, cache write and cache read tokens (321), not the output", u.Context)
	}
}
