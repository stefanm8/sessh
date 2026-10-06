package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stefanm8/sessh/session"
)

type discoverySession struct {
	session.Session
	requests []*http.Request
	respond  func(*http.Request) (int, string)
}

func (s *discoverySession) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, req)
	status, body := s.respond(req)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func runDiscovery(t *testing.T, s *discoverySession, args ...string) (string, error) {
	t.Helper()
	t.Setenv("JIRA_SITE", "https://tenant.example")
	t.Setenv("SESSH_HOME", t.TempDir())
	j := &Jira{testSession: s}
	cmd := j.Command()
	cmd.SilenceUsage = true // production root sets this as well
	cmd.SilenceErrors = true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestDiscoveryMetadataCommands(t *testing.T) {
	for _, tc := range []struct {
		name             string
		args             []string
		path, body, want string
	}{
		{"fields", []string{"field", "list", "--json"}, "/rest/api/3/field", `[{"id":"customfield_1","name":"Estimate","custom":true,"schema":{"type":"number"}}]`, "customfield_1"},
		{"types", []string{"project", "types", "ENG", "--json"}, "/rest/api/3/issue/createmeta/ENG/issuetypes", `{"issueTypes":[{"id":"10001","name":"Bug","subtask":false}],"startAt":0,"total":1}`, "10001"},
		{"create fields", []string{"issue", "create-meta", "ENG", "10001", "--json"}, "/rest/api/3/issue/createmeta/ENG/issuetypes/10001", `{"fields":[{"fieldId":"summary","name":"Summary","required":true}],"startAt":0,"total":1}`, "summary"},
		{"edit fields", []string{"issue", "edit-meta", "ENG-1", "--json"}, "/rest/api/3/issue/ENG-1/editmeta", `{"fields":{"summary":{"name":"Summary","required":true}}}`, "summary"},
		{"empty fields", []string{"field", "list", "--json"}, "/rest/api/3/field", `[]`, `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &discoverySession{respond: func(r *http.Request) (int, string) {
				if r.Method != http.MethodGet || r.URL.Path != tc.path {
					t.Fatalf("request %s %s", r.Method, r.URL)
				}
				return 200, tc.body
			}}
			out, err := runDiscovery(t, s, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(out)) || !strings.Contains(out, tc.want) {
				t.Fatalf("output=%s", out)
			}
		})
	}
}

func TestMetadataPaginationAndDisplay(t *testing.T) {
	s := &discoverySession{respond: func(r *http.Request) (int, string) {
		if r.URL.Query().Get("startAt") == "0" {
			return 200, `{"fields":[{"fieldId":"summary","name":"Summary","required":true}],"startAt":0,"total":2}`
		}
		if r.URL.Query().Get("startAt") != "1" {
			t.Fatalf("offset %s", r.URL)
		}
		return 200, `{"fields":[{"fieldId":"priority","name":"Priority","required":false,"allowedValues":[{"id":"1","name":"Highest"}]}],"startAt":1,"total":2}`
	}}
	out, err := runDiscovery(t, s, "issue", "create-meta", "ENG", "10001")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.requests) != 2 || !strings.Contains(out, "1:Highest") || !strings.Contains(out, "summary") {
		t.Fatalf("requests=%d output=%s", len(s.requests), out)
	}
}

func TestSearchUsersActualPageSizeAndLimit(t *testing.T) {
	s := &discoverySession{respond: func(r *http.Request) (int, string) {
		q := r.URL.Query()
		if q.Get("query") != "a+b & c" {
			t.Fatalf("query=%q", q.Get("query"))
		}
		switch q.Get("startAt") {
		case "0":
			if q.Get("maxResults") != "2" {
				t.Fatalf("maxResults=%s", q.Get("maxResults"))
			}
			return 200, `[{"accountId":"one","displayName":"One","active":true}]`
		case "1":
			if q.Get("maxResults") != "1" {
				t.Fatalf("maxResults=%s", q.Get("maxResults"))
			}
			return 200, `[{"accountId":"two","displayName":"Two","active":true}]`
		default:
			t.Fatalf("unexpected page %s", r.URL)
			return 500, ""
		}
	}}
	out, err := runDiscovery(t, s, "user", "search", "a+b & c", "--limit", "2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var users []any
	if err := json.Unmarshal([]byte(out), &users); err != nil || len(users) != 2 {
		t.Fatalf("output %s error %v", out, err)
	}
}

func TestDiscoveryBadShapesAndErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		body string
	}{
		{[]string{"field", "list", "--json"}, `{}`},
		{[]string{"field", "list", "--json"}, `[{}]`},
		{[]string{"user", "search", "a", "--json"}, `[null]`},
		{[]string{"user", "search", "a", "--json"}, `[{}]`},
		{[]string{"project", "types", "ENG", "--json"}, `{"values":[]}`},
		{[]string{"issue", "create-meta", "ENG", "1", "--json"}, `{"fields":[{}],"total":1}`},
		{[]string{"issue", "edit-meta", "ENG-1", "--json"}, `{"fields":null}`},
		{[]string{"issue", "edit-meta", "ENG-1", "--json"}, `{"fields":{"x":null}}`},
	} {
		t.Run(strings.Join(tc.args, " ")+tc.body, func(t *testing.T) {
			s := &discoverySession{respond: func(*http.Request) (int, string) { return 200, tc.body }}
			out, err := runDiscovery(t, s, tc.args...)
			if err == nil || out != "" {
				t.Fatalf("output=%q err=%v", out, err)
			}
		})
	}
	t.Run("permission failure", func(t *testing.T) {
		s := &discoverySession{respond: func(*http.Request) (int, string) {
			return 403, `{"errorMessages":["Browse users permission required"]}`
		}}
		_, err := runDiscovery(t, s, "user", "search", "a")
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "Browse users") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("repeated account", func(t *testing.T) {
		s := &discoverySession{respond: func(*http.Request) (int, string) { return 200, `[{"accountId":"same"}]` }}
		_, err := searchUsers(context.Background(), s, "a", 0)
		if err == nil || !strings.Contains(err.Error(), "repeated") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestReadCommandsRejectInvalidArgumentsBeforeRequests(t *testing.T) {
	for _, args := range [][]string{
		{"field", "list", "--limit", "-1"}, {"user", "search", ""},
		{"project", "types", ".."}, {"issue", "create-meta", "ENG", "Bug"},
		{"issue", "create-meta", "ENG", "0"}, {"issue", "edit-meta", "../ENG-1"},
		{"board", "list", "--type", "unknown"}, {"board", "issues", "1", "--limit", "-1"},
		{"board", "view", "../1"}, {"board", "view", "0"}, {"sprint", "issues", "-1"},
		{"sprint", "list", "1", "--state", "active,bogus"}, {"sprint", "list", "1", "--state", ""},
		{"sprint", "list", "1", "--state", "active,active"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			s := &discoverySession{respond: func(r *http.Request) (int, string) { t.Fatalf("unexpected request %s", r.URL); return 500, "" }}
			_, err := runDiscovery(t, s, args...)
			if err == nil {
				t.Fatal("invalid input succeeded")
			}
		})
	}
}

func TestSearchUsersStopsAtVendorRange(t *testing.T) {
	s := &discoverySession{respond: func(r *http.Request) (int, string) {
		start := r.URL.Query().Get("startAt")
		var offset int
		fmt.Sscan(start, &offset)
		items := make([]map[string]string, 100)
		for i := range items {
			items[i] = map[string]string{"accountId": fmt.Sprint(offset + i)}
		}
		body, _ := json.Marshal(items)
		return 200, string(body)
	}}
	items, err := searchUsers(context.Background(), s, "a", 0)
	if err != nil || len(items) != 1000 || len(s.requests) != 10 {
		t.Fatalf("items=%d requests=%d error=%v", len(items), len(s.requests), err)
	}
}
