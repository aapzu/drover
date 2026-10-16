// Package linear is a minimal Linear GraphQL client: list and fetch issues
// to import as tasks, and move an issue to "started" when an agent picks it up.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Endpoint = "https://api.linear.app/graphql"

type Client struct {
	Key      string
	Endpoint string
	HTTP     *http.Client
}

// FromEnv returns a client when LINEAR_API_KEY is set.
func FromEnv() (*Client, bool) {
	key := os.Getenv("LINEAR_API_KEY")
	if key == "" {
		return nil, false
	}
	return &Client{Key: key, Endpoint: Endpoint, HTTP: &http.Client{Timeout: 20 * time.Second}}, true
}

// SyncEnabled reports whether moving a task into an agent column should
// also move its Linear issue to a started state.
func SyncEnabled() bool { return os.Getenv("KANGENTIC_LINEAR_SYNC") == "1" }

type Issue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	BranchName  string `json:"branchName"`
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Team struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"team"`
}

const issueFields = `id identifier title description url branchName state { name type } team { id key }`

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("linear: HTTP %d: %w", resp.StatusCode, err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("linear: %s", envelope.Errors[0].Message)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(envelope.Data, out)
}

// MyIssues returns open issues assigned to the API key's user, most recently
// updated first.
func (c *Client) MyIssues(ctx context.Context) ([]Issue, error) {
	query := `query {
  viewer {
    assignedIssues(first: 100, orderBy: updatedAt,
      filter: { state: { type: { nin: ["completed", "canceled"] } } }) {
      nodes { ` + issueFields + ` }
    }
  }
}`
	var out struct {
		Viewer struct {
			AssignedIssues struct {
				Nodes []Issue `json:"nodes"`
			} `json:"assignedIssues"`
		} `json:"viewer"`
	}
	if err := c.do(ctx, query, nil, &out); err != nil {
		return nil, err
	}
	return out.Viewer.AssignedIssues.Nodes, nil
}

var keyPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*-\d+`)

// ParseKey extracts an issue key such as DX-123 from a key or issue URL.
func ParseKey(s string) (string, bool) {
	key := keyPattern.FindString(strings.TrimSpace(s))
	return strings.ToUpper(key), key != ""
}

// Issue fetches one issue by key (DX-123) or id.
func (c *Client) Issue(ctx context.Context, key string) (Issue, error) {
	var out struct {
		Issue *Issue `json:"issue"`
	}
	if err := c.do(ctx, `query($id: String!) { issue(id: $id) { `+issueFields+` } }`, map[string]any{"id": key}, &out); err != nil {
		return Issue{}, err
	}
	if out.Issue == nil {
		return Issue{}, fmt.Errorf("linear: issue %s not found", key)
	}
	return *out.Issue, nil
}

// Start moves an unstarted issue to its team's first started state and
// returns that state's name. Issues already started, done or canceled are
// left alone and an empty name is returned.
func (c *Client) Start(ctx context.Context, issueID string) (string, error) {
	var current struct {
		Issue struct {
			State struct {
				Type string `json:"type"`
			} `json:"state"`
			Team struct {
				States struct {
					Nodes []struct {
						ID       string  `json:"id"`
						Name     string  `json:"name"`
						Type     string  `json:"type"`
						Position float64 `json:"position"`
					} `json:"nodes"`
				} `json:"states"`
			} `json:"team"`
		} `json:"issue"`
	}
	query := `query($id: String!) { issue(id: $id) { state { type } team { states { nodes { id name type position } } } } }`
	if err := c.do(ctx, query, map[string]any{"id": issueID}, &current); err != nil {
		return "", err
	}
	if t := current.Issue.State.Type; t != "backlog" && t != "unstarted" && t != "triage" {
		return "", nil
	}
	states := current.Issue.Team.States.Nodes
	sort.Slice(states, func(i, j int) bool { return states[i].Position < states[j].Position })
	for _, s := range states {
		if s.Type != "started" {
			continue
		}
		var res struct {
			IssueUpdate struct {
				Success bool `json:"success"`
			} `json:"issueUpdate"`
		}
		mutation := `mutation($id: String!, $state: String!) { issueUpdate(id: $id, input: { stateId: $state }) { success } }`
		if err := c.do(ctx, mutation, map[string]any{"id": issueID, "state": s.ID}, &res); err != nil {
			return "", err
		}
		if !res.IssueUpdate.Success {
			return "", errors.New("linear: issue update was rejected")
		}
		return s.Name, nil
	}
	return "", errors.New("linear: team has no started state")
}
