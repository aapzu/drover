package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/engine"
	"github.com/aapzu/drover/internal/store"
)

func TestPrefsRoundTrip(t *testing.T) {
	root := t.TempDir()
	want := prefs{
		PreviewHeight:  14,
		ColumnWidths:   map[string]int{"planning": 30, "done": 20},
		Density:        densityTall,
		HidePreview:    true,
		NoMouse:        true,
		ShowArchived:   true,
		ShowHidden:     true,
		ShowSettings:   true,
		AssistantWidth: 50,
	}
	if err := want.save(root); err != nil {
		t.Fatal(err)
	}
	if got := loadPrefs(root); !reflect.DeepEqual(got, want) {
		t.Errorf("loaded %+v, want %+v", got, want)
	}
}

func TestLoadPrefsFallsBackToDefaults(t *testing.T) {
	tests := map[string]func(t *testing.T, root string){
		"missing": func(*testing.T, string) {},
		"corrupt": func(t *testing.T, root string) {
			if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(prefsPath(root), []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(t, root)
			got := loadPrefs(root)
			if got.ColumnWidths == nil || len(got.ColumnWidths) != 0 {
				t.Errorf("ColumnWidths = %v, want an empty map", got.ColumnWidths)
			}
			got.ColumnWidths = nil
			if !reflect.DeepEqual(got, prefs{}) {
				t.Errorf("prefs = %+v, want the zero value", got)
			}
		})
	}
}

func TestLoadPrefsFromOlderFileHasWritableColumnWidths(t *testing.T) {
	for name, json := range map[string]string{
		"no field":  `{"previewHeight": 12}`,
		"null":      `{"previewHeight": 12, "columnWidths": null}`,
		"unknowns":  `{"previewHeight": 12, "somethingNew": true}`,
		"all empty": `{"previewHeight": 12, "columnWidths": {}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(store.Dir(root), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(prefsPath(root), []byte(json), 0o644); err != nil {
				t.Fatal(err)
			}
			got := loadPrefs(root)
			if got.PreviewHeight != 12 {
				t.Errorf("PreviewHeight = %d, want 12", got.PreviewHeight)
			}
			got.ColumnWidths["planning"] = 20
		})
	}
}

func TestSavingPrefsOmitsDefaults(t *testing.T) {
	root := t.TempDir()
	if err := (prefs{}).save(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(prefsPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "{}" {
		t.Errorf("ui.json = %q, want an empty object", got)
	}
}

func TestSavingPrefsFailsWhereTheStateDirIsAFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(store.Dir(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Dir(root), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (prefs{}).save(root); err == nil {
		t.Fatal("save succeeded")
	}

	m := testModel(120, 40, 1, 0)
	m.eng = &engine.Engine{Root: root}
	m.savePrefs()
	if !strings.Contains(m.flash, "Could not save layout") {
		t.Errorf("flash = %q", m.flash)
	}
}
