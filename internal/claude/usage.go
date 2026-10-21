package claude

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Usage is what a conversation has used so far, read from its transcripts.
type Usage struct {
	Model   string  `json:"model,omitempty"`
	Context int     `json:"context,omitempty"`
	Cost    float64 `json:"cost,omitempty"`
	// LastID and LastCost count a message written over several lines once,
	// and Through keeps history a fork copies into a new transcript from
	// counting again.
	LastID   string  `json:"lastId,omitempty"`
	LastCost float64 `json:"lastCost,omitempty"`
	Through  string  `json:"through,omitempty"`
}

type modelPrice struct {
	prefix              string
	input, output, read float64
	window              int
}

var prices = []modelPrice{
	{"claude-fable-5-1", 10, 50, 0.25, 1_000_000},
	{"claude-mythos-5-1", 10, 50, 0.25, 1_000_000},
	{"claude-fable-5", 10, 50, 1, 1_000_000},
	{"claude-mythos-5", 10, 50, 1, 1_000_000},
	{"claude-opus-5-5", 4, 20, 0.20, 1_000_000},
	{"claude-opus-5", 5, 25, 0.50, 1_000_000},
	{"claude-opus-4-8", 5, 25, 0.50, 1_000_000},
	{"claude-opus-4-7", 5, 25, 0.50, 1_000_000},
	{"claude-opus-4-6", 5, 25, 0.50, 1_000_000},
	{"claude-sonnet-5", 2, 10, 0.20, 1_000_000},
	{"claude-sonnet-4-6", 3, 15, 0.30, 1_000_000},
	{"claude-haiku-5-5", 0.10, 0.50, 0.01, 1_000_000},
	{"claude-haiku-4-5", 1, 5, 0.10, 200_000},
}

func priceOf(model string) (modelPrice, bool) {
	for _, p := range prices {
		if strings.HasPrefix(model, p.prefix) {
			return p, true
		}
	}
	return modelPrice{}, false
}

// ContextWindow is the model's context size in tokens, or 0 when unknown.
func ContextWindow(model string) int {
	p, _ := priceOf(model)
	return p.window
}

// ScanUsage adds the assistant messages in the transcript after offset to u
// and returns the new offset.
func ScanUsage(path string, offset int64, u *Usage) (int64, error) {
	return readLines(path, offset, func(line []byte) {
		if bytes.Contains(line, []byte(`"usage"`)) {
			u.add(line)
		}
	})
}

func (u *Usage) add(line []byte) {
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage struct {
				Input      int `json:"input_tokens"`
				CacheWrite int `json:"cache_creation_input_tokens"`
				CacheRead  int `json:"cache_read_input_tokens"`
				Output     int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "assistant" || !strings.HasPrefix(entry.Message.Model, "claude-") ||
		entry.Timestamp < u.Through {
		return
	}
	m, n := entry.Message, entry.Message.Usage
	cost := 0.0
	if p, ok := priceOf(m.Model); ok {
		cost = (float64(n.Input)*p.input + float64(n.CacheWrite)*p.input*1.25 +
			float64(n.CacheRead)*p.read + float64(n.Output)*p.output) / 1e6
	}
	if m.ID != "" && m.ID == u.LastID {
		u.Cost -= u.LastCost
	}
	u.Cost += cost
	u.LastID, u.LastCost, u.Through = m.ID, cost, entry.Timestamp
	u.Model = m.Model
	u.Context = n.Input + n.CacheWrite + n.CacheRead
}
