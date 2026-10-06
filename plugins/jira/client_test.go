package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestIssueSearchTokensAndLimit(t *testing.T) {
	s := &requestSession{respond: func(r *http.Request) (int, string, error) {
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/rest/api/3/search/jql" {
			t.Fatalf("request=%s %s", r.Method, r.URL)
		}
		if q.Get("jql") != `project = "DEMO"` || q.Get("fields") != "summary,customfield_7" {
			t.Fatalf("query=%v", q)
		}
		if q.Get("nextPageToken") == "" {
			if q.Get("maxResults") != "3" {
				t.Fatalf("max=%s", q.Get("maxResults"))
			}
			return 200, `{"issues":[{"id":"1","key":"DEMO-1"},{"id":"2","key":"DEMO-2"}],"isLast":false,"nextPageToken":"next +/= token"}`, nil
		}
		if q.Get("nextPageToken") != "next +/= token" || q.Get("maxResults") != "1" {
			t.Fatalf("next query=%v", q)
		}
		return 200, `{"issues":[{"id":"3","key":"DEMO-3","fields":{"customfield_7":{"value":"kept"}}},{"id":"4","key":"DEMO-4"}],"isLast":true}`, nil
	}}
	items, raws, err := SearchIssues(context.Background(), s, `project = "DEMO"`, 3, []string{"summary", "customfield_7"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || len(raws) != 3 || len(s.requests) != 2 {
		t.Fatalf("lengths=%d %d requests%d", len(items), len(raws), len(s.requests))
	}
	if !strings.Contains(string(raws[2]), "customfield_7") {
		t.Fatal("custom field lost")
	}
}

func TestInvalidPages(t *testing.T) {
	cases := []struct{ name, body, kind string }{
		{"token cycle", `{"issues":[{"id":"1","key":"DEMO-1"}],"nextPageToken":"same"}`, "token"},
		{"missing token", `{"issues":[],"isLast":false}`, "token"}, {"missing array", `{"isLast":true}`, "token"}, {"wrong issue type", `{"issues":[null],"isLast":true}`, "token"}, {"missing key", `{"issues":[{}],"isLast":true}`, "token"},
		{"bad JSON", `<html>sign in</html>`, "token"}, {"no legacy on400", `{"errorMessages":["bad JQL"]}`, "400"}, {"no legacy on410", `{"errorMessages":["removed"]}`, "410"},
		{"offset repeat", `{"values":[{"id":"1","key":"DEMO"}],"isLast":false}`, "offset"}, {"offset empty", `{"values":[],"isLast":false,"total":2}`, "offset"}, {"offset wrong object", `{"values":[2],"isLast":true}`, "offset"}, {"offset missing id", `{"values":[{}],"isLast":true}`, "offset"}, {"offset missing array", `{"total":0}`, "offset"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := &requestSession{respond: func(*http.Request) (int, string, error) {
				status := 200
				if tt.kind == "400" {
					status = 400
				}
				if tt.kind == "410" {
					status = 410
				}
				return status, tt.body, nil
			}}
			var err error
			if tt.kind == "offset" {
				_, _, err = ListProjects(context.Background(), s, 0)
			} else {
				_, _, err = SearchIssues(context.Background(), s, "project=DEMO", 0, nil)
			}
			if err == nil {
				t.Fatal("expected pagination error")
			}
			if len(s.requests) > 2 {
				t.Fatalf("failure should be bounded: %d requests", len(s.requests))
			}
		})
	}
}

func TestOffsetPaginationClampedAndRemaining(t *testing.T) {
	s := &requestSession{respond: func(r *http.Request) (int, string, error) {
		q := r.URL.Query()
		switch q.Get("startAt") {
		case "0":
			if q.Get("maxResults") != "3" {
				t.Fatal(q)
			}
			return 200, `{"startAt":0,"maxResults":1,"total":3,"values":[{"id":"1","key":"A"}]}`, nil
		case "1":
			if q.Get("maxResults") != "2" {
				t.Fatal(q)
			}
			return 200, `{"startAt":1,"maxResults":1,"total":3,"values":[{"id":"2","key":"B"}]}`, nil
		case "2":
			if q.Get("maxResults") != "1" {
				t.Fatal(q)
			}
			return 200, `{"startAt":2,"maxResults":1,"total":3,"values":[{"id":"3","key":"C"}]}`, nil
		}
		t.Fatalf("unexpected offset:%s", r.URL)
		return 0, "", nil
	}}
	items, _, err := ListProjects(context.Background(), s, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || len(s.requests) != 3 {
		t.Fatalf("items %d requests %d", len(items), len(s.requests))
	}
}

func TestPaginationCancellationAndSafetyCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := noRequestSession(t)
	if _, _, err := SearchIssues(ctx, s, "project=DEMO", 0, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	n := 0
	s = &requestSession{respond: func(r *http.Request) (int, string, error) {
		n++
		return 200, fmt.Sprintf(`{"issues":[],"nextPageToken":"token-%d","isLast":false}`, n), nil
	}}
	_, _, err := SearchIssues(context.Background(), s, "project=DEMO", 0, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("error=%v", err)
	}
	if n != maxPages {
		t.Fatalf("requests=%d want%d", n, maxPages)
	}
}

func TestDeterministicAPIErrors(t *testing.T) {
	s := &requestSession{respond: func(*http.Request) (int, string, error) {
		return 400, `{"errorMessages":["Invalid request"],"errors":{"z":"last","a":"first"}}`, nil
	}}
	_, err := requestJSON(context.Background(), s, "PUT", "/rest/api/3/issue/DEMO-1", map[string]any{"fields": map[string]any{"summary": "x"}})
	want := "PUT /rest/api/3/issue/DEMO-1: HTTP 400: Invalid request; a: first; z: last"
	if err == nil || err.Error() != want {
		t.Fatalf("error=%v", err)
	}
}

func TestWhoamiPropagatesServiceFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{{"rate limited", 429, `{"errorMessages":["slow down"]}`}, {"server", 500, `{"errorMessages":["error"]}`}, {"invalid JSON", 200, `<html/>`}, {"missing ID", 200, `{}`}, {"wrong shape", 200, `[]`}} {
		t.Run(tt.name, func(t *testing.T) {
			s := &requestSession{respond: func(*http.Request) (int, string, error) { return tt.status, tt.body, nil }}
			_, err := whoami(context.Background(), s)
			if err == nil {
				t.Fatal("expected actual service failure")
			}
			if len(s.requests) != 1 {
				t.Fatal("extra identity requests")
			}
		})
	}
}

func TestEmptyListsAreArrays(t *testing.T) {
	for _, kind := range []string{"issue", "project"} {
		t.Run(kind, func(t *testing.T) {
			s := &requestSession{respond: func(*http.Request) (int, string, error) {
				if kind == "issue" {
					return 200, `{"issues":[],"isLast":true}`, nil
				}
				return 200, `{"values":[],"isLast":true}`, nil
			}}
			out, err := runJiraCommand(t, s, "", kind, "list", "--json")
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(out) != "[]" {
				t.Fatalf("empty output=%s", out)
			}
		})
	}
}

func TestHumanIssueListDefaultsToStdout(t *testing.T) {
	// Do not SetOut: Cobra Print helpers otherwise mask stderr regressions.
	t.Setenv("JIRA_SITE", "https://fixture.atlassian.net")
	oldOut, oldErr := os.Stdout, os.Stderr
	outReader, outWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errReader, errWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr; outReader.Close(); errReader.Close() }()
	os.Stdout, os.Stderr = outWriter, errWriter
	s := &requestSession{respond: func(*http.Request) (int, string, error) {
		return 200, `{"issues":[{"id":"1","key":"DEMO-1","fields":{"summary":"Visible issue"}}],"isLast":true}`, nil
	}}
	cmd := (&Jira{testSession: s}).Command()
	cmd.SetArgs([]string{"issue", "list"})
	runErr := cmd.Execute()
	outWriter.Close()
	errWriter.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	out, _ := io.ReadAll(outReader)
	stderr, _ := io.ReadAll(errReader)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(string(out), "Visible issue") || len(stderr) != 0 {
		t.Fatalf("stdout=%q stderr=%q", out, stderr)
	}
}

func TestExtendedSearchJQLAndFields(t *testing.T) {
	f := searchFilters{issueType: "Bug", priority: "High", label: `quote\"slash`, reporter: "@me", text: `x\y"z`, unassigned: true}
	q := buildSearchJQL("DEMO", "", "", "", f)
	for _, part := range []string{`project = "DEMO"`, `issuetype = "Bug"`, `priority = "High"`, `labels = "quote\\\"slash"`, `reporter = currentUser()`, `text ~ "x\\y\"z"`, `assignee IS EMPTY`} {
		if !strings.Contains(q, part) {
			t.Fatalf("query=%s missing%s", q, part)
		}
	}
	if buildSearchJQL("DEMO", "", "me", "key = DEMO-1", f) != "key = DEMO-1" {
		t.Fatal("raw JQL no longer overrides filters")
	}
	got := issueFields([]string{"customfield_1"}, false, true)
	for _, v := range []string{"customfield_1", "summary", "description", "parent"} {
		found := false
		for _, f := range got {
			found = found || f == v
		}
		if !found {
			t.Fatalf("human view missing%s: %v", v, got)
		}
	}
	data, _ := json.Marshal(issueFields([]string{"customfield_1"}, true, false))
	if string(data) != `["customfield_1"]` {
		t.Fatalf("json fields=%s", data)
	}
}

func TestCommentHistoryTransitionReads(t *testing.T) {
	for _, tt := range []struct{ command, body, want string }{
		{"comments", `{"comments":[{"id":"7","author":{"displayName":"Reader"},"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Hello"}]}]}}],"total":1}`, "Hello"},
		{"history", `{"values":[{"id":"8","author":{"displayName":"Editor"},"items":[{"field":"status","fromString":"Open","toString":"Done"}]}],"isLast":true}`, "Open → Done"},
		{"transitions", `{"transitions":[{"id":"31","name":"Resolve","to":{"name":"Done"},"fields":{"resolution":{"required":true}}}]}`, "resolution"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			s := &requestSession{respond: func(r *http.Request) (int, string, error) {
				if r.Method != "GET" {
					t.Fatal("read wrote")
				}
				return 200, tt.body, nil
			}}
			out, err := runJiraCommand(t, s, "", "issue", tt.command, "DEMO-1")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tt.want) {
				t.Fatalf("output=%s", out)
			}
			s.requests = nil
			out, err = runJiraCommand(t, s, "", "issue", tt.command, "DEMO-1", "--json")
			if err != nil || !json.Valid([]byte(out)) {
				t.Fatalf("JSON=%s error=%v", out, err)
			}
		})
	}
}

func TestMalformedOffsetMetadataRejectedAtLimit(t *testing.T) {
	for _, metadata := range []string{`"isLast":"yes"`, `"total":"one"`, `"total":-1`} {
		t.Run(metadata, func(t *testing.T) {
			s := &requestSession{respond: func(*http.Request) (int, string, error) {
				return 200, `{"values":[{"id":"1","key":"A"}],` + metadata + `}`, nil
			}}
			_, _, err := ListProjects(context.Background(), s, 1)
			if err == nil {
				t.Fatal("malformed metadata accepted at limit")
			}
		})
	}
}

func TestADFUsefulAttributes(t *testing.T) {
	doc := adf(t, `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"text":"@Ada"}},{"type":"emoji","attrs":{"shortName":":smile:"}},{"type":"text","text":"Link","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]},{"type":"inlineCard","attrs":{"url":"https://example.com/card"}}]},{"type":"rule"},{"type":"table","content":[{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}]}]}]}`)
	result := adfToText(doc)
	for _, part := range []string{"@Ada", ":smile:", "Link (https://example.com)", "https://example.com/card", "---", "a\tb"} {
		if !strings.Contains(result, part) {
			t.Fatalf("rendering %q missing %q", result, part)
		}
	}
}
