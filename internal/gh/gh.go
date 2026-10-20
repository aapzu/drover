// Package gh looks up pull requests with the GitHub CLI.
package gh

import (
	"context"
	"encoding/json"
	"errors"
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
	Author        Author   `json:"author"`
	LatestReviews []Review `json:"latestReviews"`
}

type Author struct {
	Login string `json:"login"`
}

type Review struct {
	Author      Author    `json:"author"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
	Commit      struct {
		OID string `json:"oid"`
	} `json:"commit"`
}

// ReviewedBy is the newest review by login, or the zero Review.
func (p PR) ReviewedBy(login string) Review {
	var newest Review
	for _, r := range p.LatestReviews {
		if r.Author.Login == login && r.SubmittedAt.After(newest.SubmittedAt) {
			newest = r
		}
	}
	return newest
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

// run executes gh with args in dir, giving up after 20s. Errors say what
// was being run and carry gh's stderr.
func run(dir, what string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: timed out after 20s", what)
		}
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %s", what, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// View fetches a pull request's title, branches, state and reviews.
func View(dir, url string) (PR, error) {
	out, err := run(dir, "gh pr view", "pr", "view", url, "--json", "title,headRefName,state,reviewDecision,headRefOid,mergeCommit,latestReviews,author")
	if err != nil {
		return PR{}, err
	}
	var pr PR
	err = json.Unmarshal(out, &pr)
	return pr, err
}

// Me is the login the GitHub CLI is signed in as.
func Me(dir string) (string, error) {
	out, err := run(dir, "gh api user", "api", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	login := strings.TrimSpace(string(out))
	if login == "" {
		return "", errors.New("gh api user: no login")
	}
	return login, nil
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
