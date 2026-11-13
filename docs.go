package main

import (
	_ "embed"

	"github.com/aapzu/drover/internal/ui"
)

//go:embed README.md
var readme string

// helpDocs is the documentation the assistant reads: the README and the
// keyboard and mouse reference of the help screen.
func helpDocs() string {
	return readme + "\n\n# Keyboard and mouse reference\n\n" + ui.HelpText() + "\n"
}
