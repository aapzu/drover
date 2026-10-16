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
	Title  string `json:"title"`
	Branch string `json:"headRefName"`
	State  string `json:"state"`
}

// View fetches a pull request's title, head branch and state.
func View(dir, url string) (PR, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", url, "--json", "title,headRefName,state")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
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
