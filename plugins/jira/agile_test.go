package jira

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAgileDiscoveryCommands(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		path, body string
	}{
		{[]string{"board", "list", "--project", "ENG", "--name", "Daily & Scrum", "--type", "scrum", "--json"}, "/rest/agile/1.0/board", `{"values":[{"id":7,"name":"Daily","type":"scrum"}],"isLast":true}`},
		{[]string{"board", "view", "7", "--json"}, "/rest/agile/1.0/board/7", `{"id":7,"name":"Daily","type":"scrum"}`},
		{[]string{"sprint", "list", "7", "--state", "active,future", "--json"}, "/rest/agile/1.0/board/7/sprint", `{"values":[{"id":12,"name":"Next","state":"future"}],"isLast":true}`},
		{[]string{"sprint", "view", "12", "--json"}, "/rest/agile/1.0/sprint/12", `{"id":12,"name":"Next","state":"future","goal":"Finish","originBoardId":7}`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			s := &requestSession{respond: func(r *http.Request) (int, string, error) {
				if r.Method != http.MethodGet || r.URL.Path != tc.path {
					t.Fatalf("request %s %s", r.Method, r.URL)
				}
				if tc.args[0] == "board" && tc.args[1] == "list" {
					q := r.URL.Query()
					if q.Get("projectKeyOrId") != "ENG" || q.Get("name") != "Daily & Scrum" || q.Get("type") != "scrum" {
						t.Fatalf("query=%s", r.URL.RawQuery)
					}
				}
				if tc.args[0] == "sprint" && tc.args[1] == "list" && r.URL.Query().Get("state") != "active,future" {
					t.Fatalf("query=%s", r.URL.RawQuery)
				}
				return 200, tc.body, nil
			}}
			out, err := runJiraCommand(t, s, "", tc.args...)
			if err != nil || !json.Valid([]byte(out)) {
				t.Fatalf("output=%s error=%v", out, err)
			}
		})
	}
}

func TestAgileIssuesUseEnhancedTokenPagination(t *testing.T) {
	for _, kind := range []string{"board", "sprint"} {
		t.Run(kind, func(t *testing.T) {
			s := &requestSession{respond: func(r *http.Request) (int, string, error) {
				if r.URL.Path != "/rest/software/1.0/"+kind+"/7/issue" {
					t.Fatalf("retired route used: %s", r.URL)
				}
				q := r.URL.Query()
				if !strings.Contains(q.Get("fields"), "summary") {
					t.Fatalf("fields missing: %s", r.URL)
				}
				if q.Get("nextPageToken") == "" {
					if q.Get("maxResults") != "2" {
						t.Fatalf("first limit %s", r.URL)
					}
					return 200, `{"issues":[{"key":"ENG-1","fields":{"summary":"One"}}],"nextPageToken":"a+b/=&","isLast":false}`, nil
				}
				if q.Get("nextPageToken") != "a+b/=&" || q.Get("maxResults") != "1" {
					t.Fatalf("next page %s", r.URL)
				}
				return 200, `{"issues":[{"key":"ENG-2","fields":{"summary":"Two"}}],"isLast":true}`, nil
			}}
			out, err := runJiraCommand(t, s, "", kind, "issues", "7", "--limit", "2", "--json")
			if err != nil || len(s.requests) != 2 || !strings.Contains(out, "ENG-2") {
				t.Fatalf("output=%s requests=%d error=%v", out, len(s.requests), err)
			}
		})
	}
}

func TestAgileInvalidRecordsFailEvenForJSON(t *testing.T) {
	for _, tc := range []struct {
		args []string
		body string
	}{
		{[]string{"board", "list", "--json"}, `{"values":[{}],"isLast":true}`},
		{[]string{"board", "view", "7", "--json"}, `{"id":7}`},
		{[]string{"sprint", "view", "7", "--json"}, `{}`},
		{[]string{"board", "issues", "7", "--json"}, `{"issues":[{}],"isLast":true}`},
	} {
		s := &requestSession{respond: func(*http.Request) (int, string, error) { return 200, tc.body, nil }}
		out, err := runJiraCommand(t, s, "", tc.args...)
		if err == nil || out != "" {
			t.Fatalf("args=%v output=%s error=%v", tc.args, out, err)
		}
	}
}

func TestAgileEmptyVisibleIssuesRemainEmptyArray(t *testing.T) {
	s := &requestSession{respond: func(*http.Request) (int, string, error) { return 200, `{"issues":[],"isLast":true}`, nil }}
	out, err := runJiraCommand(t, s, "", "board", "issues", "7", "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("output=%s error=%v", out, err)
	}
}
