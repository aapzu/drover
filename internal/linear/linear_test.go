package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLinear answers the queries the client sends and records mutations.
func fakeLinear(t *testing.T, stateType string, updates *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_api_test" {
			t.Errorf("missing API key header")
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch {
		case strings.Contains(req.Query, "assignedIssues"):
			w.Write([]byte(`{"data":{"viewer":{"assignedIssues":{"nodes":[
			  {"id":"i1","identifier":"DX-1","title":"Speed up CI","url":"https://linear.app/x/issue/DX-1","branchName":"aapeli/dx-1-speed-up-ci",
			   "state":{"name":"Todo","type":"unstarted"},"team":{"id":"t1","key":"DX"}}]}}}}`))
		case strings.Contains(req.Query, "issueUpdate"):
			*updates = append(*updates, req.Variables["state"].(string))
			w.Write([]byte(`{"data":{"issueUpdate":{"success":true}}}`))
		case strings.Contains(req.Query, "states"):
			w.Write([]byte(`{"data":{"issue":{"state":{"type":"` + stateType + `"},"team":{"states":{"nodes":[
			  {"id":"s-review","name":"In Review","type":"started","position":3},
			  {"id":"s-done","name":"Done","type":"completed","position":4},
			  {"id":"s-progress","name":"In Progress","type":"started","position":2}]}}}}}`))
		case strings.Contains(req.Query, "issue(id"):
			if req.Variables["id"] != "DX-1" {
				w.Write([]byte(`{"data":{"issue":null},"errors":[{"message":"Entity not found"}]}`))
				return
			}
			w.Write([]byte(`{"data":{"issue":{"id":"i1","identifier":"DX-1","title":"Speed up CI"}}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Key: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}
}

func TestMyIssuesAndFetch(t *testing.T) {
	var updates []string
	c := fakeLinear(t, "unstarted", &updates)
	issues, err := c.MyIssues(context.Background())
	if err != nil || len(issues) != 1 || issues[0].BranchName != "aapeli/dx-1-speed-up-ci" || issues[0].Team.ID != "t1" {
		t.Fatalf("MyIssues = %+v, %v", issues, err)
	}
	if got, err := c.Issue(context.Background(), "DX-1"); err != nil || got.Title != "Speed up CI" {
		t.Errorf("Issue = %+v, %v", got, err)
	}
	if _, err := c.Issue(context.Background(), "DX-404"); err == nil || !strings.Contains(err.Error(), "Entity not found") {
		t.Errorf("missing issue error = %v", err)
	}
}

func TestStartPicksFirstStartedState(t *testing.T) {
	var updates []string
	c := fakeLinear(t, "unstarted", &updates)
	state, err := c.Start(context.Background(), "i1")
	if err != nil || state != "In Progress" || len(updates) != 1 || updates[0] != "s-progress" {
		t.Fatalf("Start = %q, %v, updates %v", state, err, updates)
	}

	updates = nil
	c = fakeLinear(t, "started", &updates)
	if state, err := c.Start(context.Background(), "i1"); err != nil || state != "" || len(updates) != 0 {
		t.Errorf("already started issue was changed: %q %v %v", state, err, updates)
	}
}

func TestParseKey(t *testing.T) {
	for in, want := range map[string]string{
		"DX-123": "DX-123",
		" dx-7 ": "DX-7",
		"https://linear.app/hoxhunt/issue/DX-1234/foo-bar": "DX-1234",
	} {
		if got, ok := ParseKey(in); !ok || got != want {
			t.Errorf("ParseKey(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := ParseKey("not an issue"); ok {
		t.Errorf("ParseKey accepted junk")
	}
}
