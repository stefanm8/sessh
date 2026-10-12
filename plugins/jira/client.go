package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

const defaultLimit = 50
const maxPages = 2000
const maxResponseBytes = 32 << 20

// requestJSON never retries: a transport failure after a write has an uncertain
// outcome. The caller must inspect Jira before deciding to send it again.
func requestJSON(ctx context.Context, s session.Session, method, path string, payload any) (json.RawMessage, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode Jira request: %w", err)
		}
	}
	req, err := session.Request(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Jira response (HTTP %d): %w", resp.StatusCode, err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("Jira response exceeds %d bytes (HTTP %d)", maxResponseBytes, resp.StatusCode)
	}
	data = bytes.TrimSpace(data)
	raw := &session.Raw{Status: resp.StatusCode, OK: resp.StatusCode >= 200 && resp.StatusCode < 300, Body: data}
	if json.Valid(data) {
		raw.JSON = data
	}
	if !raw.OK {
		return nil, apiError(raw, method+" "+strings.Split(path, "?")[0])
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if raw.JSON == nil || bytes.Equal(raw.JSON, []byte("null")) {
		return nil, fmt.Errorf("Jira returned HTTP %d without a JSON result for %s %s", resp.StatusCode, method, strings.Split(path, "?")[0])
	}
	return raw.JSON, nil
}

func getJSON(ctx context.Context, s session.Session, path string, out any) error {
	raw, err := requestJSON(ctx, s, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode Jira response: %w", err)
	}
	return nil
}

func validateLimit(limit int) error {
	if limit < 0 {
		return fmt.Errorf("--limit must be zero (all results) or a positive number")
	}
	return nil
}
func pageSize(limit, count int) int {
	if limit > 0 && limit-count < 100 {
		return limit - count
	}
	return 100
}

// pagedValues handles Jira's offset-based collection APIs. It accepts only
// arrays of objects and detects servers ignoring offsets instead of silently
// printing duplicates or returning an incomplete success.
func pagedValues(ctx context.Context, s session.Session, path, arrayKey string, limit int) ([]json.RawMessage, error) {
	if err := validateLimit(limit); err != nil {
		return nil, err
	}
	items := []json.RawMessage{}
	seen := map[string]bool{}
	start := 0
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		u, err := url.Parse(path)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("startAt", strconv.Itoa(start))
		q.Set("maxResults", strconv.Itoa(pageSize(limit, len(items))))
		u.RawQuery = q.Encode()
		raw, err := requestJSON(ctx, s, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		var e map[string]json.RawMessage
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("invalid Jira page: %w", err)
		}
		data, ok := e[arrayKey]
		if !ok || len(data) == 0 || data[0] != '[' {
			return nil, fmt.Errorf("Jira page is missing the %s array", arrayKey)
		}
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, err
		}
		var offset *int
		if v, ok := e["startAt"]; ok {
			if err := json.Unmarshal(v, &offset); err != nil {
				return nil, fmt.Errorf("invalid Jira startAt: %w", err)
			}
		}
		if offset != nil && *offset != start {
			return nil, fmt.Errorf("Jira pagination did not advance: requested %d, received %d", start, *offset)
		}
		var isLast *bool
		var total *int
		if v, ok := e["isLast"]; ok {
			if err := json.Unmarshal(v, &isLast); err != nil {
				return nil, err
			}
		}
		if v, ok := e["total"]; ok {
			if err := json.Unmarshal(v, &total); err != nil {
				return nil, err
			}
		}
		if total != nil && *total < 0 {
			return nil, fmt.Errorf("Jira pagination returned a negative total")
		}

		if seen[string(data)] && len(values) > 0 {
			return nil, fmt.Errorf("Jira pagination repeated a page")
		}
		seen[string(data)] = true
		for _, v := range values {
			if len(v) == 0 || v[0] != '{' {
				return nil, fmt.Errorf("invalid object in Jira %s page", arrayKey)
			}
			items = append(items, v)
			if limit > 0 && len(items) >= limit {
				return items, nil
			}
		}
		start += len(values)
		if isLast != nil && *isLast {
			return items, nil
		}
		if total != nil && start >= *total {
			return items, nil
		}
		if len(values) == 0 {
			if (isLast != nil && !*isLast) || (total != nil && start < *total) {
				return nil, fmt.Errorf("Jira pagination returned an empty page before completion")
			}
			return items, nil
		}
	}
	return nil, fmt.Errorf("Jira pagination exceeded %d pages; narrow the query or use --limit", maxPages)
}

// tokenValues implements enhanced search pagination for platform and software.
func tokenValues(ctx context.Context, s session.Session, path string, limit int) ([]json.RawMessage, error) {
	if err := validateLimit(limit); err != nil {
		return nil, err
	}
	items := []json.RawMessage{}
	token := ""
	seen := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		u, err := url.Parse(path)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("maxResults", strconv.Itoa(pageSize(limit, len(items))))
		if token != "" {
			q.Set("nextPageToken", token)
		}
		u.RawQuery = q.Encode()
		var e struct {
			Issues        []json.RawMessage `json:"issues"`
			IsLast        *bool             `json:"isLast"`
			NextPageToken string            `json:"nextPageToken"`
		}
		raw, err := requestJSON(ctx, s, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("invalid Jira search page: %w", err)
		}
		if e.Issues == nil {
			return nil, fmt.Errorf("Jira page is missing the issues array")
		}
		for _, v := range e.Issues {
			if len(v) == 0 || v[0] != '{' {
				return nil, fmt.Errorf("invalid object in Jira issues page")
			}
			items = append(items, v)
			if limit > 0 && len(items) >= limit {
				return items, nil
			}
		}
		if e.IsLast != nil && *e.IsLast {
			return items, nil
		}
		if e.NextPageToken == "" {
			if e.IsLast != nil && !*e.IsLast {
				return nil, fmt.Errorf("Jira search omitted its next page token")
			}
			return items, nil
		}
		if seen[e.NextPageToken] {
			return nil, fmt.Errorf("Jira search repeated a page token")
		}
		seen[e.NextPageToken] = true
		token = e.NextPageToken
	}
	return nil, fmt.Errorf("Jira pagination exceeded %d pages; narrow the query or use --limit", maxPages)
}

func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// withSession keeps command handlers independently testable with a synthetic
// session. Production always uses the shared authentication/replay policy.
func (j *Jira) withSession(mutating bool, fn plugin.Runner) func(*cobra.Command, []string) error {
	if j.testSession != nil {
		return func(cmd *cobra.Command, args []string) error { return fn(cmd.Context(), j.testSession, cmd, args) }
	}
	if mutating {
		return plugin.WithMutationSession(j, fn)
	}
	return plugin.WithSession(j, fn)
}

func printLine(cmd *cobra.Command, label, value string, width int) {
	if strings.TrimSpace(value) == "" {
		return
	}
	pad := max(0, width-len(label))
	fmt.Fprintln(cmd.OutOrStdout(), format.Gray(label+strings.Repeat(" ", pad))+" "+value)
}
