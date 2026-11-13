package main

import (
	"strings"
	"testing"
)

func TestHelpDocsHoldTheREADMEAndTheKeys(t *testing.T) {
	docs := helpDocs()
	for _, want := range []string{"### Asking the board", "## Keys", "## Mouse"} {
		if !strings.Contains(docs, want) {
			t.Errorf("docs lack %q", want)
		}
	}
}
