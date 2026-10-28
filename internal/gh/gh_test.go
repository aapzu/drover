package gh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChangesRequestedAt(t *testing.T) {
	at := func(s string) time.Time {
		v, _ := time.Parse(time.RFC3339, s)
		return v
	}
	pr := PR{LatestReviews: []Review{
		{State: "CHANGES_REQUESTED", SubmittedAt: at("2026-10-01T10:00:00Z")},
		{State: "APPROVED", SubmittedAt: at("2026-10-03T10:00:00Z")},
		{State: "CHANGES_REQUESTED", SubmittedAt: at("2026-10-02T10:00:00Z")},
		{State: "COMMENTED", SubmittedAt: at("2026-10-04T10:00:00Z")},
	}}
	if got := pr.ChangesRequestedAt(); !got.Equal(at("2026-10-02T10:00:00Z")) {
		t.Errorf("ChangesRequestedAt = %v, want the newest changes-requested review", got)
	}
	if got := (PR{}).ChangesRequestedAt(); !got.IsZero() {
		t.Errorf("no reviews: %v", got)
	}
	mine := func(login, state, when string) Review {
		return Review{Author: Author{Login: login}, State: state, SubmittedAt: at(when)}
	}
	reviewed := PR{LatestReviews: []Review{
		mine("them", "APPROVED", "2026-10-05T10:00:00Z"),
		mine("me", "COMMENTED", "2026-10-01T10:00:00Z"),
		mine("me", "CHANGES_REQUESTED", "2026-10-02T10:00:00Z"),
	}}
	if got := reviewed.ReviewedBy("me"); got.State != "CHANGES_REQUESTED" {
		t.Errorf("ReviewedBy = %+v, want my newest review", got)
	}
	if got := reviewed.ReviewedBy("nobody"); !got.SubmittedAt.IsZero() {
		t.Errorf("ReviewedBy without a review: %+v", got)
	}
	if (PR{}).MergeOID() != "" {
		t.Error("MergeOID without a merge commit")
	}
}

func TestInMergeQueueRejectsAnythingButAPullRequestURL(t *testing.T) {
	if _, err := InMergeQueue("", "https://github.com/acme/widgets/pull/42/files"); err == nil {
		t.Error("a URL that isn't a pull request should be an error")
	}
}

func TestInMergeQueueAsksGraphQLAndReadsTheAnswer(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\necho \"$@\" > \"$FAKE_ARGS\"\ncat \"$FAKE_OUT\"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ARGS", args)
	out := filepath.Join(dir, "out")
	t.Setenv("FAKE_OUT", out)
	for answer, want := range map[string]bool{"true\n": true, "false\n": false} {
		os.WriteFile(out, []byte(answer), 0o644)
		got, err := InMergeQueue(dir, "https://ghe.example.com/acme/widgets/pull/42")
		if err != nil || got != want {
			t.Errorf("answer %q: got %v, %v", answer, got, err)
		}
	}
	argv, _ := os.ReadFile(args)
	for _, want := range []string{"api graphql", "--hostname ghe.example.com", "o=acme", "r=widgets", "n=42", "isInMergeQueue"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("gh was run with %q, which lacks %q", argv, want)
		}
	}
	for _, answer := range []string{"", "null\n", "maybe\n"} {
		os.WriteFile(out, []byte(answer), 0o644)
		if _, err := InMergeQueue(dir, "https://github.com/acme/widgets/pull/42"); err == nil {
			t.Errorf("answer %q should be an error", answer)
		}
	}
}
