package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stefanm8/sessh/session"
)

type recordedRequest struct {
	Method, Path, Query string
	Body                json.RawMessage
	ContentType         string
}
type requestSession struct {
	session.Session
	requests []recordedRequest
	respond  func(*http.Request) (int, string, error)
}

func (s *requestSession) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	s.requests = append(s.requests, recordedRequest{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, body, r.Header.Get("Content-Type")})
	status, data, err := s.respond(r)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(data)), Header: make(http.Header), Request: r}, nil
}
func runJiraCommand(t *testing.T, s session.Session, stdin string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("JIRA_SITE", "https://fixture.atlassian.net")
	cmd := (&Jira{testSession: s}).Command()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
func noRequestSession(t *testing.T) *requestSession {
	return &requestSession{respond: func(r *http.Request) (int, string, error) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		return 0, "", nil
	}}
}
func decodeObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("invalid JSON %s: %v", data, err)
	}
	return v
}

func TestMutationDryRuns(t *testing.T) {
	for _, tt := range []struct {
		name                string
		args                []string
		stdin, method, path string
		want                map[string]any
		reads               int
	}{
		{name: "create", args: []string{"issue", "create", "DEMO", "--summary", "A new issue", "--type", "10001", "--description", "line one\nline two", "--assignee", "unassigned", "--labels", "first,second", "--dry-run"}, method: "POST", path: "/rest/api/3/issue", want: map[string]any{"summary": "A new issue"}},
		{name: "edit clear", args: []string{"issue", "edit", "DEMO-1", "--description=", "--labels=", "--parent=", "--dry-run"}, method: "PUT", path: "/rest/api/3/issue/DEMO-1", want: map[string]any{"description": nil}},
		{name: "assign", args: []string{"issue", "assign", "DEMO-1", "unassigned", "--dry-run"}, method: "PUT", path: "/rest/api/3/issue/DEMO-1/assignee"},
		{name: "me", args: []string{"issue", "assign", "DEMO-1", "me", "--dry-run"}, method: "PUT", path: "/rest/api/3/issue/DEMO-1/assignee", reads: 1},
		{name: "transition fields", args: []string{"issue", "transition", "DEMO-1", "31", "--fields-json", "-", "--dry-run"}, stdin: `{"resolution":{"id":"1"}}`, method: "POST", path: "/rest/api/3/issue/DEMO-1/transitions", reads: 1},
		{name: "transition populated fields", args: []string{"issue", "transition", "DEMO-1", "31", "--dry-run"}, method: "POST", path: "/rest/api/3/issue/DEMO-1/transitions", reads: 1},
		{name: "comment", args: []string{"issue", "comment", "add", "DEMO-1", "--body-file", "-", "--dry-run"}, stdin: "hello\nworld", method: "POST", path: "/rest/api/3/issue/DEMO-1/comment"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &requestSession{respond: func(r *http.Request) (int, string, error) {
				if r.Method != "GET" {
					t.Fatalf("dry-run wrote: %s", r.Method)
				}
				if r.URL.Path == "/rest/api/3/myself" {
					return 200, `{"accountId":"my-id"}`, nil
				}
				return 200, `{"transitions":[{"id":"31","name":"Done","fields":{"resolution":{"required":true}}}]}`, nil
			}}
			output, err := runJiraCommand(t, s, tt.stdin, tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			if len(s.requests) != tt.reads {
				t.Fatalf("requests=%d want %d", len(s.requests), tt.reads)
			}
			v := decodeObject(t, []byte(output))
			if v["method"] != tt.method || v["path"] != tt.path {
				t.Fatalf("dry-run=%s", output)
			}
			payload := v["payload"].(map[string]any)
			if tt.want != nil {
				f := payload["fields"].(map[string]any)
				for k, want := range tt.want {
					if fmt.Sprint(f[k]) != fmt.Sprint(want) {
						t.Fatalf("%s=%v want%v", k, f[k], want)
					}
				}
			}
			if tt.name == "edit clear" {
				if _, ok := payload["fields"].(map[string]any)["parent"]; ok {
					t.Fatal("cleared parent must use documented update operation")
				}
				update := payload["update"].(map[string]any)
				if update["parent"].([]any)[0].(map[string]any)["set"].(map[string]any)["none"] != true {
					t.Fatalf("parent update=%v", update)
				}
			}
			if tt.name == "assign" && payload["accountId"] != nil {
				t.Fatal("unassigned accountId must be null")
			}
			if tt.name == "me" && payload["accountId"] != "my-id" {
				t.Fatalf("identity not resolved: %v", payload)
			}
			if tt.name == "comment" {
				if text(payload["body"]) != "hello\nworld" {
					t.Fatalf("comment body=%v", payload)
				}
			}
		})
	}
}

func TestMutationRequestsAndSuccess(t *testing.T) {
	for _, tt := range []struct {
		args                   []string
		status                 int
		response, path, method string
	}{
		{[]string{"issue", "create", "DEMO", "--type", "10001", "--summary", "New", "--json"}, 201, `{"id":"12","key":"DEMO-12","custom":true}`, "/rest/api/3/issue", "POST"},
		{[]string{"issue", "edit", "DEMO-1", "--summary", "Changed", "--json"}, 204, "", "/rest/api/3/issue/DEMO-1", "PUT"},
		{[]string{"issue", "edit", "DEMO-1", "--summary", "Changed", "--json"}, 200, `{"id":"10001","key":"DEMO-1","fields":{"summary":"Changed"}}`, "/rest/api/3/issue/DEMO-1", "PUT"},
		{[]string{"issue", "assign", "DEMO-1", "someone", "--json"}, 204, "", "/rest/api/3/issue/DEMO-1/assignee", "PUT"},
		{[]string{"issue", "transition", "DEMO-1", "31", "--json"}, 204, "", "/rest/api/3/issue/DEMO-1/transitions", "POST"},
		{[]string{"issue", "comment", "add", "DEMO-1", "--body", "A comment", "--json"}, 201, `{"id":"27","body":{"type":"doc","version":1,"content":[]}}`, "/rest/api/3/issue/DEMO-1/comment", "POST"},
	} {
		t.Run(strings.Join(tt.args[:2], " ")+tt.path, func(t *testing.T) {
			writes := 0
			s := &requestSession{respond: func(r *http.Request) (int, string, error) {
				if r.Method == "GET" {
					return 200, `{"transitions":[{"id":"31","name":"Done"}]}`, nil
				}
				writes++
				if r.Method != tt.method || r.URL.Path != tt.path {
					t.Fatalf("request=%s %s", r.Method, r.URL)
				}
				return tt.status, tt.response, nil
			}}
			output, err := runJiraCommand(t, s, "", tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(output)) {
				t.Fatalf("invalid stdout JSON: %s", output)
			}
			if writes != 1 {
				t.Fatalf("writes=%d", writes)
			}
			last := s.requests[len(s.requests)-1]
			if last.ContentType != "application/json" || !json.Valid(last.Body) {
				t.Fatalf("invalid write: %+v", last)
			}
			if (tt.status == 204 || tt.args[1] == "edit") && !strings.Contains(output, `"success": true`) {
				t.Fatalf("missing success: %s", output)
			}
		})
	}
}

func TestMutationsDoNotRetryOrReadAfterWrite(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		body    string
		failure error
	}{
		{"timeout", 0, "", context.DeadlineExceeded}, {"transport", 0, "", errors.New("connection closed")}, {"rate limit", 429, `{"errorMessages":["Slow down"]}`, nil}, {"server", 503, `{"errorMessages":["Unavailable"]}`, nil}, {"invalid accepted response", 201, `{}`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &requestSession{respond: func(r *http.Request) (int, string, error) { return tt.status, tt.body, tt.failure }}
			_, err := runJiraCommand(t, s, "", "issue", "create", "DEMO", "--type", "10001", "--summary", "New")
			if err == nil {
				t.Fatal("expected error")
			}
			if len(s.requests) != 1 {
				t.Fatalf("request count %d", len(s.requests))
			}
		})
	}
}

func TestMutationLocalValidation(t *testing.T) {
	cases := [][]string{
		{"issue", "create", "DEMO", "--type", "10001"}, {"issue", "create", "DEMO", "--summary", "New"}, {"issue", "create", "DEMO", "--type", "", "--summary", "New"},
		{"issue", "edit", "DEMO-1"}, {"issue", "edit", "DEMO-1", "--summary="}, {"issue", "edit", "DEMO-1", "--description", "one", "--description-file", "-"}, {"issue", "edit", "DEMO-1", "--description-file", "-", "--fields-json", "-"},
		{"issue", "edit", "DEMO-1", "--fields-json="}, {"issue", "edit", "DEMO-1", "--labels", "bad label"}, {"issue", "assign", "DEMO-1", ""}, {"issue", "transition", "DEMO-1", "Done"},
		{"issue", "comment", "add", "DEMO-1"}, {"issue", "comment", "add", "DEMO-1", "--body", " "}, {"issue", "comment", "add", "DEMO-1", "--body", "one", "--body-file", "-"},
		{"issue", "list", "--limit=-1"}, {"project", "list", "--limit=-1"}, {"issue", "list", "--unassigned", "--assignee", "me"}, {"issue", "view", ".."}, {"issue", "list", "--json", "--web"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, err := runJiraCommand(t, noRequestSession(t), "", args...)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	for _, account := range []string{"bad id", "bad/id", "bad?id", ".", ".."} {
		t.Run("invalid assignee "+account, func(t *testing.T) {
			out, err := runJiraCommand(t, noRequestSession(t), "", "issue", "create", "DEMO", "--summary", "New", "--type", "Task", "--assignee", account, "--dry-run")
			if err == nil || !strings.Contains(err.Error(), "account ID") || out != "" {
				t.Fatalf("output=%q error=%v; expected account ID validation before metadata", out, err)
			}
		})
	}
	for _, input := range []string{`null`, `[]`, `{"summary":42}`, `{"summary":"one","summary":"two"}`, `{"fields":{"summary":"X"}}`, `{"summary":"first"} {"summary":"second"}`} {
		t.Run("fields "+input, func(t *testing.T) {
			_, err := runJiraCommand(t, noRequestSession(t), input, "issue", "edit", "DEMO-1", "--summary", "X", "--fields-json", "-", "--dry-run")
			if err == nil {
				t.Fatal("expected fields validation error")
			}
		})
	}
	for _, input := range []string{`{}`, `{"summary":42}`, `{"description":"plain text"}`, `{"labels":[42]}`, `{"assignee":{"name":"guess"}}`} {
		t.Run("invalid structured field "+input, func(t *testing.T) {
			_, err := runJiraCommand(t, noRequestSession(t), input, "issue", "edit", "DEMO-1", "--fields-json", "-", "--dry-run")
			if err == nil {
				t.Fatal("invalid structured field accepted")
			}
		})
	}
	for _, input := range []string{`{"issuetype":null}`, `{"issuetype":{}}`, `{"issuetype":[]}`} {
		t.Run("type "+input, func(t *testing.T) {
			_, err := runJiraCommand(t, noRequestSession(t), input, "issue", "create", "DEMO", "--summary", "New", "--fields-json", "-", "--dry-run")
			if err == nil {
				t.Fatal("expected invalid issue type error")
			}
		})
	}
}

func TestMutationFieldsAndFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "description.txt")
	if err := os.WriteFile(path, []byte("hello\r\nworld"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := runJiraCommand(t, noRequestSession(t), `{"customfield_10000":1234567890123456789}`, "issue", "edit", "DEMO-1", "--description-file", path, "--fields-json", "-", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1234567890123456789") {
		t.Fatalf("custom number lost precision: %s", out)
	}
	v := decodeObject(t, []byte(out))
	fields := v["payload"].(map[string]any)["fields"].(map[string]any)
	if text(fields["description"]) != "hello\nworld" {
		t.Fatalf("ADF=%v", fields["description"])
	}
	_, err = runJiraCommand(t, noRequestSession(t), "", "issue", "edit", "DEMO-1", "--description-file", filepath.Join(dir, "missing"), "--dry-run")
	if err == nil {
		t.Fatal("missing file accepted")
	}
	_, err = runJiraCommand(t, noRequestSession(t), strings.Repeat("x", maxInputBytes+1), "issue", "comment", "add", "DEMO-1", "--body-file", "-", "--dry-run")
	if err == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestCreateTypeNameResolution(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(fmt.Sprint(ambiguous), func(t *testing.T) {
			s := &requestSession{respond: func(r *http.Request) (int, string, error) {
				if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/issuetypes") {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				types := `[{"id":"1","name":"Task"}]`
				if ambiguous {
					types = `[{"id":"1","name":"Task"},{"id":"2","name":"Task"}]`
				}
				return 200, `{"isLast":true,"issueTypes":` + types + `}`, nil
			}}
			out, err := runJiraCommand(t, s, "", "issue", "create", "DEMO", "--summary", "New", "--type", "Task", "--dry-run")
			if ambiguous {
				if err == nil {
					t.Fatal("ambiguous type accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				v := decodeObject(t, []byte(out))
				if v["payload"].(map[string]any)["fields"].(map[string]any)["issuetype"].(map[string]any)["id"] != "1" {
					t.Fatalf("type=%s", out)
				}
			}
		})
	}
}

// A syntactically valid JSON value is not necessarily a Jira success response.
// Unexpected 2xx payloads must not be reported as a successful issue update.
func TestRejectsMalformedMutationSuccess(t *testing.T) {
	commands := [][]string{
		{"issue", "edit", "DEMO-1", "--summary", "Changed", "--json"},
		{"issue", "assign", "DEMO-1", "account-123", "--json"},
		{"issue", "transition", "DEMO-1", "31", "--json"},
	}
	for _, args := range commands {
		for _, body := range []string{`true`, `42`, `"accepted"`, `[]`, `{}`, `{"errorMessages":["Update rejected"]}`} {
			t.Run(args[1]+"/"+body, func(t *testing.T) {
				writes := 0
				s := &requestSession{respond: func(r *http.Request) (int, string, error) {
					if r.Method == http.MethodGet {
						return 200, `{"transitions":[{"id":"31","name":"Done"}]}`, nil
					}
					writes++
					return http.StatusOK, body, nil
				}}
				out, err := runJiraCommand(t, s, "", args...)
				if err == nil || out != "" {
					t.Errorf("unexpected success: output=%q error=%v", out, err)
				}
				if writes != 1 {
					t.Errorf("writes=%d; malformed success must never trigger a retry", writes)
				}
			})
		}
	}
}
