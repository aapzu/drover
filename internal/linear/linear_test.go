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
	return clientFor(t, func(w http.ResponseWriter, r *http.Request) {
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
	})
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

func clientFor(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{Key: "lin_api_test", Endpoint: srv.URL, HTTP: srv.Client()}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")
	if c, ok := FromEnv(); ok || c != nil {
		t.Errorf("FromEnv without a key = %+v, %v", c, ok)
	}
	t.Setenv("LINEAR_API_KEY", "lin_api_x")
	c, ok := FromEnv()
	if !ok || c.Key != "lin_api_x" || c.Endpoint != Endpoint || c.HTTP == nil {
		t.Errorf("FromEnv = %+v, %v", c, ok)
	}
}

func TestSyncEnabled(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "true": false, "1": true} {
		t.Setenv("DROVER_LINEAR_SYNC", value)
		if got := SyncEnabled(); got != want {
			t.Errorf("DROVER_LINEAR_SYNC=%q: SyncEnabled = %v, want %v", value, got, want)
		}
	}
}

func TestDoErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"GraphQL errors win over the data", 200, `{"data":{"issue":null},"errors":[{"message":"Entity not found"},{"message":"second"}]}`, "linear: Entity not found"},
		{"GraphQL errors on a failing status", 400, `{"errors":[{"message":"bad query"}]}`, "linear: bad query"},
		{"a non-200 status", 502, `{"data":{}}`, "linear: HTTP 502"},
		{"an unreadable body", 500, `<html>oops</html>`, "linear: HTTP 500"},
		{"a missing key", 401, `{"errors":[{"message":"Authentication required"}]}`, "linear: Authentication required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			_, err := c.MyIssues(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestDoRequestFailures(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {})
	c.Endpoint = "http://127.0.0.1:1"
	if _, err := c.MyIssues(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "linear: ") {
		t.Errorf("unreachable endpoint: %v", err)
	}
	c.Endpoint = "://bad"
	if _, err := c.MyIssues(context.Background()); err == nil {
		t.Error("an invalid endpoint is not an error")
	}
}

func TestIssueNotFoundWithoutAnError(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"issue":null}}`))
	})
	if _, err := c.Issue(context.Background(), "DX-9"); err == nil || !strings.Contains(err.Error(), "DX-9 not found") {
		t.Errorf("err = %v", err)
	}
}

func TestStartFailures(t *testing.T) {
	for _, tc := range []struct {
		name, states, update, wantErr string
	}{
		{"no started state", `[{"id":"s1","name":"Done","type":"completed","position":1}]`, "", "no started state"},
		{"a rejected update", `[{"id":"s1","name":"Doing","type":"started","position":1}]`, `{"data":{"issueUpdate":{"success":false}}}`, "update was rejected"},
		{"a failing update", `[{"id":"s1","name":"Doing","type":"started","position":1}]`, `{"errors":[{"message":"forbidden"}]}`, "forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Query string }
				json.NewDecoder(r.Body).Decode(&req)
				if strings.Contains(req.Query, "issueUpdate") {
					w.Write([]byte(tc.update))
					return
				}
				w.Write([]byte(`{"data":{"issue":{"state":{"type":"backlog"},"team":{"states":{"nodes":` + tc.states + `}}}}}`))
			})
			if _, err := c.Start(context.Background(), "i1"); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}

	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"nope"}]}`))
	})
	if _, err := c.Start(context.Background(), "i1"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("lookup failure: %v", err)
	}
}
