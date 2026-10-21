package main

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/inbox"
	"github.com/aapzu/drover/internal/store"
)

func TestParseArgsAcceptsFlagsAnywhere(t *testing.T) {
	for _, args := range [][]string{
		{"Fix it", "-c", "exec", "--prompt", "now"},
		{"-c", "exec", "Fix it", "--prompt", "now"},
		{"--prompt=now", "-c=exec", "Fix it"},
	} {
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		prompt, column := fs.String("prompt", "", ""), fs.String("c", "", "")
		rest, err := parseArgs(fs, args)
		if err != nil || !slices.Equal(rest, []string{"Fix it"}) || *prompt != "now" || *column != "exec" {
			t.Errorf("parseArgs(%q) = %q, prompt=%q column=%q, err=%v", args, rest, *prompt, *column, err)
		}
	}
}

func TestParseNumber(t *testing.T) {
	for in, want := range map[string]int{"12": 12, "#12": 12} {
		if n, err := parseNumber(in); err != nil || n != want {
			t.Errorf("parseNumber(%q) = %d, %v", in, n, err)
		}
	}
	for _, in := range []string{"", "x", "0", "-1", "#"} {
		if _, err := parseNumber(in); err == nil {
			t.Errorf("parseNumber(%q) accepted", in)
		}
	}
}

func TestValidateRejectsWhatCouldNeverApply(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(root, 0o755)
	state := &store.State{NextNumber: 2, Tasks: []*store.Task{{ID: "a", Number: 1, ColumnID: "todo"}}}
	if err := state.Save(root); err != nil {
		t.Fatal(err)
	}
	title := "x"

	for name, tc := range map[string]struct {
		req  inbox.Request
		want string
	}{
		"unknown column": {inbox.Request{Op: inbox.OpMove, Number: 1, Column: "nowhere"}, `unknown column "nowhere"; columns are todo, planning`},
		"unknown card":   {inbox.Request{Op: inbox.OpRemove, Number: 7}, "no card #7"},
		"bad linear":     {inbox.Request{Op: inbox.OpAdd, Linear: "banana"}, "not a Linear issue key"},
		"fine move":      {inbox.Request{Op: inbox.OpMove, Number: 1, Column: "In review"}, ""},
		"fine add":       {inbox.Request{Op: inbox.OpAdd, Title: &title, Column: "planning"}, ""},
		"fine linear":    {inbox.Request{Op: inbox.OpAdd, Linear: "DX-1"}, ""},
	} {
		err := validate(root, tc.req)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error = %v, want %q", name, err, tc.want)
		}
	}
}
