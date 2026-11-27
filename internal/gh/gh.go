// Package gh looks up pull requests and issues with the GitHub CLI.
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
	Mergeable      string `json:"mergeable"`
	MergeCommit    *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	Author         Author   `json:"author"`
	LatestReviews  []Review `json:"latestReviews"`
	ReviewRequests []Author `json:"reviewRequests"`
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

// RequestedFrom reports whether login's review is requested on the PR. Team
// requests carry no login and never match.
func (p PR) RequestedFrom(login string) bool {
	for _, r := range p.ReviewRequests {
		if r.Login != "" && strings.EqualFold(r.Login, login) {
			return true
		}
	}
	return false
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
	out, err := run(dir, "gh pr view", "pr", "view", url, "--json", "title,headRefName,state,reviewDecision,headRefOid,mergeable,mergeCommit,latestReviews,reviewRequests,author")
	if err != nil {
		return PR{}, err
	}
	var pr PR
	err = json.Unmarshal(out, &pr)
	return pr, err
}

// Found is a pull request a search returned.
type Found struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Author  Author `json:"author"`
	IsDraft bool   `json:"isDraft"`
}

// searchLimit is the most pull requests one search returns.
const searchLimit = 100

// Search lists the open pull requests in repos (owner/name) that match
// filter, gh search prs flags or search qualifiers such as
// user-review-requested:@me.
func Search(dir string, repos []string, filter ...string) ([]Found, error) {
	if len(repos) == 0 {
		return nil, errors.New("gh search prs: no repository to search")
	}
	args := append([]string{"search", "prs"}, filter...)
	for _, r := range repos {
		args = append(args, "--repo", r)
	}
	args = append(args, "--state", "open", "--limit", strconv.Itoa(searchLimit), "--json", "number,title,url,author,isDraft")
	out, err := run(dir, "gh search prs", args...)
	if err != nil {
		return nil, err
	}
	var found []Found
	err = json.Unmarshal(out, &found)
	return found, err
}

// ForBranch is the open pull request from branch in the repository itself, if
// there is one. gh matches --head by branch name alone, so pull requests from
// forks that share the name are skipped.
func ForBranch(dir, branch string) (Found, bool, error) {
	out, err := run(dir, "gh pr list", "pr", "list", "--head", branch, "--state", "open", "--limit", "10", "--json", "number,title,url,author,isCrossRepository")
	if err != nil {
		return Found{}, false, err
	}
	var found []struct {
		Found
		CrossRepository bool `json:"isCrossRepository"`
	}
	if err := json.Unmarshal(out, &found); err != nil {
		return Found{}, false, err
	}
	for _, f := range found {
		if !f.CrossRepository {
			return f.Found, true, nil
		}
	}
	return Found{}, false, nil
}

// Repo is the owner/name of the GitHub repository dir belongs to.
func Repo(dir string) (string, error) {
	out, err := run(dir, "gh repo view", "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "", errors.New("gh repo view: no repository")
	}
	return name, nil
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

var prURL = regexp.MustCompile(`^https?://([^/]+)/([^/]+)/([^/]+)/pull/\d+$`)

const mergeQueueQuery = `query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){isInMergeQueue}}}`

// InMergeQueue reports whether the pull request at url waits in its
// repository's merge queue.
func InMergeQueue(dir, url string) (bool, error) {
	m := prURL.FindStringSubmatch(url)
	if m == nil {
		return false, fmt.Errorf("gh api graphql: %q is not a pull request URL", url)
	}
	out, err := run(dir, "gh api graphql", "api", "graphql", "--hostname", m[1],
		"-f", "query="+mergeQueueQuery, "-f", "o="+m[2], "-f", "r="+m[3], "-F", "n="+strconv.Itoa(Number(url)),
		"--jq", ".data.repository.pullRequest.isInMergeQueue")
	if err != nil {
		return false, err
	}
	switch got := strings.TrimSpace(string(out)); got {
	case "true", "false":
		return got == "true", nil
	default:
		return false, fmt.Errorf("gh api graphql: unexpected isInMergeQueue %q", got)
	}
}

// IsURL reports whether url is a pull request URL.
func IsURL(url string) bool { return prURL.MatchString(url) }

// Number returns the PR number at the end of a pull request URL.
func Number(url string) int {
	m := number.FindStringSubmatch(url)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// Issue is a GitHub issue as `gh issue` reports it.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

var issueURL = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/]+)/issues/\d+$`)

// ID names the issue across repositories, such as acme/widgets#42.
func (i Issue) ID() string {
	if m := issueURL.FindStringSubmatch(i.URL); m != nil {
		return m[1] + "#" + strconv.Itoa(i.Number)
	}
	return "#" + strconv.Itoa(i.Number)
}

const issueFields = "number,title,body,url,state"

// Issues lists the open issues in dir's repository that are assigned to you,
// most recently updated first.
func Issues(dir string) ([]Issue, error) {
	out, err := run(dir, "gh issue list", "issue", "list", "--assignee", "@me", "--state", "open", "--limit", "100", "--json", issueFields)
	if err != nil {
		return nil, err
	}
	var issues []Issue
	err = json.Unmarshal(out, &issues)
	return issues, err
}

// IssueView fetches the issue ref names; see ParseIssueRef.
func IssueView(dir, ref string) (Issue, error) {
	out, err := run(dir, "gh issue view", "issue", "view", ref, "--json", issueFields)
	if err != nil {
		return Issue{}, err
	}
	var issue Issue
	err = json.Unmarshal(out, &issue)
	return issue, err
}

var issueNumber = regexp.MustCompile(`^#?(\d+)$`)

// ParseIssueRef accepts an issue number (42 or #42) or an issue URL and
// returns what `gh issue view` takes.
func ParseIssueRef(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if m := issueNumber.FindStringSubmatch(s); m != nil {
		return m[1], true
	}
	return s, issueURL.MatchString(s)
}
