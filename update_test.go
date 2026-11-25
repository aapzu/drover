package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aapzu/drover/internal/update"
)

func fakeLatest(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	}))
	t.Cleanup(srv.Close)
	old, oldVersion := updateClient, version
	updateClient = &update.Client{HTTP: srv.Client(), API: srv.URL}
	t.Cleanup(func() { updateClient, version = old, oldVersion })
}

func TestCmdUpdateCheck(t *testing.T) {
	for _, c := range []struct {
		name, current, latest, want string
	}{
		{"newer release", "v1.0.0", "v1.1.0", "v1.1.0 is available (you have v1.0.0)"},
		{"current", "v1.1.0", "v1.1.0", "v1.1.0 is up to date"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeLatest(t, c.latest)
			version = c.current
			var err error
			out := stdout(t, func() { err = cmdUpdate([]string{"--check"}) })
			if err != nil || !strings.Contains(out, c.want) {
				t.Errorf("out = %q, err = %v, want %q", out, err, c.want)
			}
		})
	}
}

func TestCmdUpdateRefusesDevBuildsAndStrayArguments(t *testing.T) {
	fakeLatest(t, "v9.9.9")
	version = ""
	if err := cmdUpdate(nil); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Errorf("dev build: err = %v", err)
	}
	version = "v1.0.0"
	if err := cmdUpdate([]string{"now"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("stray argument: err = %v", err)
	}
}
