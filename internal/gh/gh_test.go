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
	if (PR{}).MergeOID() != "" {
		t.Error("MergeOID without a merge commit")
	}
}
