package ui

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/aapelihaanpuu/kangentic-tui/internal/store"
)

type density int

const (
	densityNormal density = iota
	densityCompact
	densityTall
)

func (d density) cardHeight() int {
	switch d {
	case densityCompact:
		return 3
	case densityTall:
		return 8
	}
	return 5
}

// prefs is the board layout, kept per repo in .kangentic/tui/ui.json.
type prefs struct {
	// PreviewHeight is in rows; 0 means the default share of the screen.
	PreviewHeight int            `json:"previewHeight,omitempty"`
	ColumnWidths  map[string]int `json:"columnWidths,omitempty"`
	Density       density        `json:"density,omitempty"`
	HidePreview   bool           `json:"hidePreview,omitempty"`
	NoMouse       bool           `json:"noMouse,omitempty"`
}

func prefsPath(root string) string { return filepath.Join(store.Dir(root), "ui.json") }

func loadPrefs(root string) prefs {
	var p prefs
	if data, err := os.ReadFile(prefsPath(root)); err == nil {
		json.Unmarshal(data, &p)
	}
	if p.ColumnWidths == nil {
		p.ColumnWidths = map[string]int{}
	}
	return p
}

func (p prefs) save(root string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
		return err
	}
	return os.WriteFile(prefsPath(root), data, 0o644)
}
