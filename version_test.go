package main

import "testing"

func TestBuildVersion(t *testing.T) {
	t.Cleanup(func() { version = "" })

	version = "1.2.3"
	if got := buildVersion(); got != "1.2.3" {
		t.Errorf("buildVersion() with ldflags = %q, want 1.2.3", got)
	}

	version = ""
	if got := buildVersion(); got != "dev" {
		t.Errorf("buildVersion() without ldflags from a development build = %q, want dev", got)
	}
}
