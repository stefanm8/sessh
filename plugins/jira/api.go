package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/stefanm8/sessh/session"
)

// Jira Cloud REST API v3.
//
//	GET /rest/api/3/project/search   projects, offset pagination
//	GET /rest/api/3/project/{key}
//	GET /rest/api/3/search/jql       modern issue search, token pagination
//	GET /rest/api/3/search           legacy fallback, offset pagination
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

// ListProjects walks Jira's offset pagination: {startAt, isLast, values}.
func ListProjects(ctx context.Context, s session.Session, max int) ([]Project, []json.RawMessage, error) {
	var (
		items []Project
		raws  []json.RawMessage
		start int
	)
	const pageLen = 50
	for i := 0; i < 2000; i++ {
		var envelope struct {
			Values []json.RawMessage `json:"values"`
			IsLast bool              `json:"isLast"`
		}
		path := fmt.Sprintf("/rest/api/3/project/search?maxResults=%d&startAt=%d&orderBy=name&expand=lead,description",
			pageLen, start)
		if err := s.GetJSON(ctx, path, &envelope); err != nil {
			return nil, nil, err
		}
		for _, raw := range envelope.Values {
			var p Project
			if err := json.Unmarshal(raw, &p); err != nil {
				continue
			}
			items = append(items, p)
			raws = append(raws, raw)
			if max > 0 && len(items) >= max {
				return items, raws, nil
			}
		}
		if envelope.IsLast || len(envelope.Values) == 0 {
			break
		}
		start += len(envelope.Values)
	}
	return items, raws, nil
}

// GetProject fetches one project.
func GetProject(ctx context.Context, s session.Session, key string) (*Project, json.RawMessage, error) {
	raw, err := session.Get(ctx, s, "/rest/api/3/project/"+url.PathEscape(key)+"?expand=lead,description")
	if err != nil {
		return nil, nil, err
	}
	if !raw.OK {
		return nil, nil, apiError(raw, "project "+key)
	}
	var p Project
	if err := raw.Decode(&p); err != nil {
		return nil, nil, err
	}
	return &p, raw.JSON, nil
}

// SearchIssues runs JQL.
//
// Primary is the modern /search/jql endpoint (token pagination, no total). If
// that endpoint is unavailable (404/410) we fall back to the legacy /search
// (offset pagination). A 400 — a bad or unbounded query — is a real user error
// and is surfaced, never silently retried.
func SearchIssues(ctx context.Context, s session.Session, jql string, max int, fields []string) ([]Issue, []json.RawMessage, error) {
	if len(fields) == 0 {
		fields = DefaultIssueFields
	}
	fcsv := strings.Join(fields, ",")
	const pageLen = 100

	base := fmt.Sprintf("/rest/api/3/search/jql?jql=%s&fields=%s&maxResults=%d",
		url.QueryEscape(jql), url.QueryEscape(fcsv), pageLen)

	first, err := session.Get(ctx, s, base)
	if err != nil {
		return nil, nil, err
	}
	if first.Status == 404 || first.Status == 410 {
		return legacySearch(ctx, s, jql, fcsv, max)
	}
	if !first.OK || first.JSON == nil {
		return nil, nil, apiError(first, "/rest/api/3/search/jql")
	}

	var (
		items []Issue
		raws  []json.RawMessage
	)
	payload := first.JSON
	for i := 0; i < 2000; i++ {
		var envelope struct {
			Issues        []json.RawMessage `json:"issues"`
			IsLast        bool              `json:"isLast"`
			NextPageToken string            `json:"nextPageToken"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, nil, err
		}
		for _, raw := range envelope.Issues {
			var it Issue
			if err := json.Unmarshal(raw, &it); err != nil {
				continue
			}
			items = append(items, it)
			raws = append(raws, raw)
			if max > 0 && len(items) >= max {
				return items, raws, nil
			}
		}
		if envelope.IsLast || envelope.NextPageToken == "" {
			break
		}
		next, err := session.Get(ctx, s, base+"&nextPageToken="+url.QueryEscape(envelope.NextPageToken))
		if err != nil {
			return nil, nil, err
		}
		if !next.OK || next.JSON == nil {
			return nil, nil, apiError(next, "/rest/api/3/search/jql")
		}
		payload = next.JSON
	}
	return items, raws, nil
}

func legacySearch(ctx context.Context, s session.Session, jql, fcsv string, max int) ([]Issue, []json.RawMessage, error) {
	var (
		items []Issue
		raws  []json.RawMessage
		start int
	)
	const pageLen = 100
	for i := 0; i < 2000; i++ {
		var envelope struct {
			Issues []json.RawMessage `json:"issues"`
			Total  *int              `json:"total"`
		}
		path := fmt.Sprintf("/rest/api/3/search?jql=%s&fields=%s&maxResults=%d&startAt=%d",
			url.QueryEscape(jql), url.QueryEscape(fcsv), pageLen, start)
		if err := s.GetJSON(ctx, path, &envelope); err != nil {
			return nil, nil, err
		}
		for _, raw := range envelope.Issues {
			var it Issue
			if err := json.Unmarshal(raw, &it); err != nil {
				continue
			}
			items = append(items, it)
			raws = append(raws, raw)
			if max > 0 && len(items) >= max {
				return items, raws, nil
			}
		}
		start += len(envelope.Issues)
		total := start
		if envelope.Total != nil {
			total = *envelope.Total
		}
		if len(envelope.Issues) == 0 || start >= total {
			break
		}
	}
	return items, raws, nil
}

// GetIssue fetches one issue with the fields the view command prints.
func GetIssue(ctx context.Context, s session.Session, key string) (*Issue, json.RawMessage, error) {
	const fields = "summary,status,issuetype,priority,assignee,reporter,created,updated,description,labels,parent"
	raw, err := session.Get(ctx, s, "/rest/api/3/issue/"+url.PathEscape(key)+"?fields="+fields)
	if err != nil {
		return nil, nil, err
	}
	if !raw.OK {
		return nil, nil, apiError(raw, "issue "+key)
	}
	var it Issue
	if err := raw.Decode(&it); err != nil {
		return nil, nil, err
	}
	return &it, raw.JSON, nil
}

// apiError turns a Jira error payload into a readable one-liner.
func apiError(raw *session.Raw, what string) error {
	var e struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if raw.JSON != nil && json.Unmarshal(raw.JSON, &e) == nil {
		if len(e.ErrorMessages) > 0 {
			return fmt.Errorf("%s: %s", what, strings.Join(e.ErrorMessages, "; "))
		}
		if len(e.Errors) > 0 {
			var parts []string
			for k, v := range e.Errors {
				parts = append(parts, k+": "+v)
			}
			return fmt.Errorf("%s: %s", what, strings.Join(parts, "; "))
		}
	}
	return fmt.Errorf("%s: HTTP %d", what, raw.Status)
}
