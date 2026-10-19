// Package gh looks up pull requests with the GitHub CLI.
package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type PR struct {
	Title          string `json:"title"`
	Branch         string `json:"headRefName"`
	State          string `json:"state"`
	ReviewDecision string `json:"reviewDecision"`
	HeadSHA        string `json:"headRefOid"`
	MergeCommit    *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	LatestReviews []Review `json:"latestReviews"`
}

type Review struct {
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// ChangesRequestedAt is when the newest review requesting changes came in.
func (p PR) ChangesRequestedAt() time.Time {
	var at time.Time
	for _, r := range p.LatestReviews {
		if r.State == "CHANGES_REQUESTED" && r.SubmittedAt.After(at) {
			at = r.SubmittedAt
		}
	}
	return at
}

func (p PR) MergeOID() string {
	if p.MergeCommit == nil {
		return ""
	}
	return p.MergeCommit.OID
}

// View fetches a pull request's title, branches, state and reviews.
func View(dir, url string) (PR, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", url, "--json", "title,headRefName,state,reviewDecision,headRefOid,mergeCommit,latestReviews")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return PR{}, fmt.Errorf("gh pr view: timed out after 20s")
		}
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			return PR{}, fmt.Errorf("gh pr view: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return PR{}, err
	}
	var pr PR
	err = json.Unmarshal(out, &pr)
	return pr, err
}

var number = regexp.MustCompile(`/pull/(\d+)$`)

// Number returns the PR number at the end of a pull request URL.
func Number(url string) int {
	m := number.FindStringSubmatch(url)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
