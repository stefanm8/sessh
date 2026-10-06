package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/stefanm8/sessh/session"
)

// Jira Cloud REST API v3.
//
//	GET /rest/api/3/project/search   projects, offset pagination
//	GET /rest/api/3/project/{key}
//	GET /rest/api/3/search/jql       modern issue search, token pagination
//	GET /rest/api/3/issue/{key}

// DefaultIssueFields keeps issue-list payloads small and predictable.
var DefaultIssueFields = []string{"summary", "status", "issuetype", "priority", "assignee", "updated"}

type Project struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	Name           string `json:"name"`
	ProjectTypeKey string `json:"projectTypeKey"`
	Description    any    `json:"description"`
	Lead           struct {
		DisplayName string `json:"displayName"`
	} `json:"lead"`
	ProjectCategory struct {
		Name string `json:"name"`
	} `json:"projectCategory"`
}

type Status struct {
	Name           string `json:"name"`
	StatusCategory struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"statusCategory"`
}

type User struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

type Issue struct {
	Key    string `json:"key"`
	ID     string `json:"id"`
	Fields struct {
		Summary   string `json:"summary"`
		Status    Status `json:"status"`
		IssueType struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Priority struct {
			Name string `json:"name"`
		} `json:"priority"`
		Assignee    *User    `json:"assignee"`
		Reporter    *User    `json:"reporter"`
		Created     string   `json:"created"`
		Updated     string   `json:"updated"`
		Description any      `json:"description"`
		Labels      []string `json:"labels"`
		Parent      *struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
			} `json:"fields"`
		} `json:"parent"`
	} `json:"fields"`
}

// ListProjects walks Jira's project search pagination.
func ListProjects(ctx context.Context, s session.Session, max int) ([]Project, []json.RawMessage, error) {
	raws, err := pagedValues(ctx, s, "/rest/api/3/project/search?orderBy=name&expand=lead,description", "values", max)
	if err != nil {
		return nil, nil, err
	}
	items := make([]Project, 0, len(raws))
	for _, raw := range raws {
		var p Project
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nil, fmt.Errorf("decode project: %w", err)
		}
		if p.ID == "" || p.Key == "" {
			return nil, nil, fmt.Errorf("Jira returned a project without ID or key")
		}
		items = append(items, p)
	}
	return items, raws, nil
}

func GetProject(ctx context.Context, s session.Session, key string) (*Project, json.RawMessage, error) {
	raw, err := requestJSON(ctx, s, "GET", "/rest/api/3/project/"+url.PathEscape(key)+"?expand=lead,description", nil)
	if err != nil {
		return nil, nil, err
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil, err
	}
	if p.ID == "" || p.Key == "" {
		return nil, nil, fmt.Errorf("Jira returned a project without ID or key")
	}
	return &p, raw, nil
}

// SearchIssues uses Jira's enhanced token API. The retired legacy search route
// is intentionally not retried: its removal must not turn into extra latency.
func SearchIssues(ctx context.Context, s session.Session, jql string, max int, fields []string) ([]Issue, []json.RawMessage, error) {
	if len(fields) == 0 {
		fields = DefaultIssueFields
	}
	q := url.Values{"jql": {jql}, "fields": {strings.Join(fields, ",")}}
	raws, err := tokenValues(ctx, s, "/rest/api/3/search/jql?"+q.Encode(), "issues", max)
	if err != nil {
		return nil, nil, err
	}
	items := make([]Issue, 0, len(raws))
	for _, raw := range raws {
		var it Issue
		if err := json.Unmarshal(raw, &it); err != nil {
			return nil, nil, fmt.Errorf("decode issue: %w", err)
		}
		if it.Key == "" || it.ID == "" {
			return nil, nil, fmt.Errorf("Jira returned an issue without ID or key")
		}
		items = append(items, it)
	}
	return items, raws, nil
}

func GetIssue(ctx context.Context, s session.Session, key string) (*Issue, json.RawMessage, error) {
	return GetIssueFields(ctx, s, key, nil)
}
func GetIssueFields(ctx context.Context, s session.Session, key string, fields []string) (*Issue, json.RawMessage, error) {
	if len(fields) == 0 {
		fields = []string{"summary", "status", "issuetype", "priority", "assignee", "reporter", "created", "updated", "description", "labels", "parent"}
	}
	raw, err := requestJSON(ctx, s, "GET", "/rest/api/3/issue/"+url.PathEscape(key)+"?fields="+url.QueryEscape(strings.Join(fields, ",")), nil)
	if err != nil {
		return nil, nil, err
	}
	var it Issue
	if err := json.Unmarshal(raw, &it); err != nil {
		return nil, nil, err
	}
	if it.ID == "" || it.Key == "" {
		return nil, nil, fmt.Errorf("Jira returned an issue without ID or key")
	}
	return &it, raw, nil
}

// apiError keeps all Jira validation messages and stable field ordering.
func apiError(raw *session.Raw, what string) error {
	var e struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		Message       string            `json:"message"`
	}
	parts := []string{}
	if raw.JSON != nil && json.Unmarshal(raw.JSON, &e) == nil {
		parts = append(parts, e.ErrorMessages...)
		keys := make([]string, 0, len(e.Errors))
		for k := range e.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, k+": "+e.Errors[k])
		}
		if e.Message != "" {
			parts = append(parts, e.Message)
		}
	}
	hint := ""
	switch raw.Status {
	case 401:
		hint = "authentication required; run sessh auth login jira"
	case 403:
		hint = "permission denied; check Jira project permissions"
	case 404:
		hint = "resource not found or not visible to your account"
	case 410:
		hint = "this Jira endpoint is no longer available; update sessh or check Jira Cloud API availability"
	case 429:
		hint = "Jira rate limit reached; wait before retrying"
	}
	if hint != "" {
		parts = append(parts, hint)
	}
	if len(parts) > 0 {
		return fmt.Errorf("%s: HTTP %d: %s", what, raw.Status, strings.Join(parts, "; "))
	}
	return fmt.Errorf("%s: HTTP %d", what, raw.Status)
}
