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
	fakeGh(t, `echo "$@" > "$FAKE_ARGS"
cat "$FAKE_OUT"`)
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

func fakeGh(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNumber(t *testing.T) {
	for url, want := range map[string]int{
		"https://github.com/acme/widgets/pull/42":       42,
		"https://ghe.example.com/acme/widgets/pull/7":   7,
		"https://github.com/acme/widgets/pull/42/files": 0,
		"https://github.com/acme/widgets/issues/42":     0,
		"https://github.com/acme/widgets/pull/":         0,
		"":                                              0,
	} {
		if got := Number(url); got != want {
			t.Errorf("Number(%q) = %d, want %d", url, got, want)
		}
	}
}

func TestViewParsesThePullRequest(t *testing.T) {
	fakeGh(t, `echo '{"title":"Fix it","headRefName":"fix","state":"MERGED","reviewDecision":"APPROVED","headRefOid":"abc","mergeCommit":{"oid":"def"},"author":{"login":"me"},"latestReviews":[{"author":{"login":"them"},"state":"APPROVED"}]}'`)
	pr, err := View(t.TempDir(), "https://github.com/acme/widgets/pull/1")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Title != "Fix it" || pr.Branch != "fix" || pr.State != "MERGED" || pr.HeadSHA != "abc" || pr.MergeOID() != "def" || pr.Author.Login != "me" || len(pr.LatestReviews) != 1 {
		t.Errorf("View = %+v", pr)
	}
}

func TestViewAndMeErrors(t *testing.T) {
	url := "https://github.com/acme/widgets/pull/1"
	for _, tc := range []struct {
		name    string
		script  string
		call    func() error
		wantErr string
	}{
		{"view carries stderr", "echo 'no such PR' >&2; exit 1", func() error { _, err := View(t.TempDir(), url); return err }, "gh pr view: no such PR"},
		{"view with bad JSON", "echo 'not json'", func() error { _, err := View(t.TempDir(), url); return err }, "invalid"},
		{"me carries stderr", "echo 'not logged in' >&2; exit 1", func() error { _, err := Me(t.TempDir()); return err }, "gh api user: not logged in"},
		{"me without a login", "echo", func() error { _, err := Me(t.TempDir()); return err }, "gh api user: no login"},
		{"silent failure keeps the exit error", "exit 3", func() error { _, err := Me(t.TempDir()); return err }, "exit status 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeGh(t, tc.script)
			err := tc.call()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestMeTrimsTheLogin(t *testing.T) {
	fakeGh(t, `echo ' octocat '`)
	got, err := Me(t.TempDir())
	if err != nil || got != "octocat" {
		t.Errorf("Me = %q, %v", got, err)
	}
}

func TestRunWithoutGhInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Me(t.TempDir()); err == nil {
		t.Error("Me succeeded without gh on PATH")
	}
}
