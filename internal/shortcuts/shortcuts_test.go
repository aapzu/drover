package shortcuts

import (
	"strings"
	"testing"
)

func TestTextListsEveryShortcut(t *testing.T) {
	got := Text()
	for _, rows := range [][]Row{Keys, Mouse, Live} {
		for _, r := range rows {
			if want := r.Key + ": " + r.Desc; !strings.Contains(got, want) {
				t.Errorf("text lacks %q", want)
			}
		}
	}
}

func TestKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Keys {
		if seen[r.Key] {
			t.Errorf("%q is listed twice", r.Key)
		}
		seen[r.Key] = true
	}
}
