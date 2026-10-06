package jira

import (
	"net/http"
	"strings"
	"testing"
)

// A syntactically valid JSON value is not necessarily a Jira success response.
// Unexpected 2xx payloads must not be reported as a successful issue update.
func TestReleaseQARejectsMalformedMutationSuccess(t *testing.T) {
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

func TestReleaseQAAcceptsReturnedEditedIssue(t *testing.T) {
	s := &requestSession{respond: func(*http.Request) (int, string, error) {
		return http.StatusOK, `{"id":"10001","key":"DEMO-1","fields":{"summary":"Changed"}}`, nil
	}}
	out, err := runJiraCommand(t, s, "", "issue", "edit", "DEMO-1", "--summary", "Changed", "--json")
	if err != nil || !strings.Contains(out, `"success": true`) {
		t.Fatalf("documented edit 200 issue response failed: output=%q error=%v", out, err)
	}
}

// Invalid assignees must fail before create's issue-type metadata preflight.
// Using a type name here makes a late validation regression observable without
// opening a real browser or relying on session acquisition internals.
func TestReleaseQACreateAssigneeValidationBeforeMetadata(t *testing.T) {
	for _, account := range []string{"bad id", "bad/id", "bad?id", ".", ".."} {
		t.Run(account, func(t *testing.T) {
			s := &requestSession{respond: func(*http.Request) (int, string, error) {
				return 200, `{"isLast":true,"issueTypes":[{"id":"10001","name":"Task"}]}`, nil
			}}
			out, err := runJiraCommand(t, s, "", "issue", "create", "DEMO", "--summary", "New", "--type", "Task", "--assignee", account, "--dry-run")
			if err == nil || !strings.Contains(err.Error(), "account ID") || out != "" {
				t.Errorf("output=%q error=%v; expected account ID validation failure", out, err)
			}
			if len(s.requests) != 0 {
				t.Errorf("made %d requests before rejecting invalid account ID", len(s.requests))
			}
		})
	}
}
