package gh

import (
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
