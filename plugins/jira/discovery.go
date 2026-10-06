package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/session"
)

type readCollection func(context.Context, session.Session, []string, int) ([]json.RawMessage, error)
type collectionRow func(json.RawMessage) ([]string, error)

// readListCommand keeps local validation outside browser acquisition and makes
// all discovery and planning collections share output and limit semantics.
func readListCommand(j *Jira, use, short string, count int, columns []string, fetch readCollection, row collectionRow) *cobra.Command {
	var asJSON bool
	var limit int
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(count)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateLimit(limit); err != nil {
			return err
		}
		for _, arg := range args {
			if strings.TrimSpace(arg) == "" {
				return fmt.Errorf("arguments must not be empty")
			}
		}
		return j.withSession(false, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
			items, err := fetch(ctx, s, args, limit)
			if err != nil {
				return err
			}
			if items == nil {
				items = []json.RawMessage{}
			}
			rows := make([][]string, 0, len(items))
			for _, item := range items {
				r, err := row(item)
				if err != nil {
					return err
				}
				rows = append(rows, r)
			}
			if asJSON {
				return printJSON(cmd, items)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No results found.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), format.Table(rows, columns))
			return nil
		})(cmd, args)
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", defaultLimit, "maximum results (0 for all available results)")
	return cmd
}

func discoveryCommands(j *Jira) []*cobra.Command {
	users := &cobra.Command{Use: "user", Aliases: []string{"users"}, Short: "Find Jira account IDs"}
	search := readListCommand(j, "search <query>", "Find active users (Jira returns at most 1,000 matches)", 1,
		[]string{"account ID", "name", "active"},
		func(ctx context.Context, s session.Session, args []string, limit int) ([]json.RawMessage, error) {
			return searchUsers(ctx, s, args[0], limit)
		}, func(raw json.RawMessage) ([]string, error) {
			var u struct {
				AccountID   string `json:"accountId"`
				DisplayName string `json:"displayName"`
				Active      bool   `json:"active"`
			}
			if err := json.Unmarshal(raw, &u); err != nil {
				return nil, err
			}
			if u.AccountID == "" {
				return nil, fmt.Errorf("Jira user has no accountId")
			}
			return []string{u.AccountID, u.DisplayName, strconv.FormatBool(u.Active)}, nil
		})
	search.Long = "Find active Jira users by name or email. Jira limits this endpoint to the first 1,000 matches; --limit 0 reads that available range, not an entire directory. Account visibility and Browse users permission apply."
	users.AddCommand(search)
	fields := &cobra.Command{Use: "field", Aliases: []string{"fields"}, Short: "Discover system and custom fields"}
	fields.AddCommand(readListCommand(j, "list", "List available issue fields", 0, []string{"ID", "name", "type", "custom"},
		func(ctx context.Context, s session.Session, _ []string, limit int) ([]json.RawMessage, error) {
			raw, err := requestJSON(ctx, s, http.MethodGet, "/rest/api/3/field", nil)
			if err != nil {
				return nil, err
			}
			items, err := objectArray(raw)
			if err != nil {
				return nil, err
			}
			if limit > 0 && len(items) > limit {
				items = items[:limit]
			}
			return items, nil
		}, func(raw json.RawMessage) ([]string, error) {
			var f struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Custom bool   `json:"custom"`
				Schema struct {
					Type  string `json:"type"`
					Items string `json:"items"`
				} `json:"schema"`
			}
			if err := json.Unmarshal(raw, &f); err != nil {
				return nil, err
			}
			if f.ID == "" {
				return nil, fmt.Errorf("Jira field has no id")
			}
			kind := f.Schema.Type
			if f.Schema.Items != "" {
				kind += "<" + f.Schema.Items + ">"
			}
			return []string{f.ID, f.Name, kind, strconv.FormatBool(f.Custom)}, nil
		}))
	return []*cobra.Command{users, fields}
}

func projectMetadataCommands(j *Jira) []*cobra.Command {
	cmd := readListCommand(j, "types <project>", "List issue types available for creation", 1,
		[]string{"ID", "name", "subtask", "description"},
		func(ctx context.Context, s session.Session, args []string, limit int) ([]json.RawMessage, error) {
			return pagedValues(ctx, s, "/rest/api/3/issue/createmeta/"+url.PathEscape(args[0])+"/issuetypes", "issueTypes", limit)
		}, func(raw json.RawMessage) ([]string, error) {
			var v struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Subtask     bool   `json:"subtask"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			if v.ID == "" {
				return nil, fmt.Errorf("Jira issue type has no id")
			}
			return []string{v.ID, v.Name, strconv.FormatBool(v.Subtask), format.Truncate(v.Description, 70)}, nil
		})
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return err
		}
		return validateIdentifier(args[0], "project key or ID")
	}
	return []*cobra.Command{cmd}
}

func issueMetadataCommands(j *Jira) []*cobra.Command {
	create := readListCommand(j, "create-meta <project> <issue-type-id>", "Show fields required or allowed when creating an issue", 2,
		[]string{"field ID", "name", "required", "type", "allowed values"},
		func(ctx context.Context, s session.Session, args []string, limit int) ([]json.RawMessage, error) {
			path := "/rest/api/3/issue/createmeta/" + url.PathEscape(args[0]) + "/issuetypes/" + url.PathEscape(args[1])
			return pagedValues(ctx, s, path, "fields", limit)
		}, metadataRow)
	create.Args = func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(2)(cmd, args); err != nil {
			return err
		}
		if err := validateIdentifier(args[0], "project key or ID"); err != nil {
			return err
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || id <= 0 || strings.Trim(args[1], "0123456789") != "" {
			return fmt.Errorf("issue type ID must be a positive integer; use jira project types %s", args[0])
		}
		return nil
	}
	var asJSON bool
	edit := &cobra.Command{Use: "edit-meta <issue>", Short: "Show fields editable on an issue", Args: cobra.ExactArgs(1)}
	edit.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateIdentifier(args[0], "issue key"); err != nil {
			return err
		}
		return j.withSession(false, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
			raw, err := requestJSON(ctx, s, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(args[0])+"/editmeta", nil)
			if err != nil {
				return err
			}
			var envelope struct {
				Fields map[string]json.RawMessage `json:"fields"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return err
			}
			if envelope.Fields == nil {
				return fmt.Errorf("Jira edit metadata is missing the fields object")
			}
			keys := make([]string, 0, len(envelope.Fields))
			for key := range envelope.Fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			rows := make([][]string, 0, len(keys))
			for _, key := range keys {
				var f map[string]json.RawMessage
				if err := json.Unmarshal(envelope.Fields[key], &f); err != nil {
					return err
				}
				if f == nil {
					return fmt.Errorf("invalid metadata for field %s", key)
				}
				f["fieldId"], _ = json.Marshal(key)
				item, _ := json.Marshal(f)
				row, err := metadataRow(item)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
			if asJSON {
				return printJSON(cmd, raw)
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No editable fields.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), format.Table(rows, []string{"field ID", "name", "required", "type", "allowed values"}))
			return nil
		})(cmd, args)
	}
	edit.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	return []*cobra.Command{create, edit}
}

func metadataRow(raw json.RawMessage) ([]string, error) {
	var f struct {
		FieldID  string            `json:"fieldId"`
		Key      string            `json:"key"`
		Name     string            `json:"name"`
		Required bool              `json:"required"`
		Allowed  []json.RawMessage `json:"allowedValues"`
		Schema   struct {
			Type string `json:"type"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.FieldID == "" {
		f.FieldID = f.Key
	}
	if f.FieldID == "" {
		return nil, fmt.Errorf("Jira field metadata has no fieldId")
	}
	allowed := make([]string, 0, len(f.Allowed))
	for _, value := range f.Allowed {
		var v struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if json.Unmarshal(value, &v) == nil && (v.Name != "" || v.Value != "") {
			label := v.Name
			if label == "" {
				label = v.Value
			}
			if v.ID != "" {
				label = v.ID + ":" + label
			}
			allowed = append(allowed, label)
		} else {
			allowed = append(allowed, string(value))
		}
	}
	return []string{f.FieldID, f.Name, strconv.FormatBool(f.Required), f.Schema.Type, format.Truncate(strings.Join(allowed, ", "), 100)}, nil
}

func objectArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, fmt.Errorf("Jira response must be an array of objects")
	}
	items := []json.RawMessage{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		if len(item) == 0 || item[0] != '{' {
			return nil, fmt.Errorf("invalid object in Jira array")
		}
	}
	return items, nil
}

func searchUsers(ctx context.Context, s session.Session, query string, limit int) ([]json.RawMessage, error) {
	if err := validateLimit(limit); err != nil {
		return nil, err
	}
	items := []json.RawMessage{}
	seen := map[string]bool{}
	for start := 0; start < 1000; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		size := pageSize(limit, len(items))
		if size > 1000-start {
			size = 1000 - start
		}
		path := fmt.Sprintf("/rest/api/3/user/search?query=%s&startAt=%d&maxResults=%d", url.QueryEscape(query), start, size)
		raw, err := requestJSON(ctx, s, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		page, err := objectArray(raw)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return items, nil
		}
		for _, item := range page {
			var user struct {
				ID string `json:"accountId"`
			}
			if err := json.Unmarshal(item, &user); err != nil {
				return nil, err
			}
			if user.ID == "" {
				return nil, fmt.Errorf("Jira user has no accountId")
			}
			if seen[user.ID] {
				return nil, fmt.Errorf("Jira user pagination repeated an account")
			}
			seen[user.ID] = true
			items = append(items, item)
			if limit > 0 && len(items) >= limit {
				return items, nil
			}
		}
		// The user-search API filters before slicing. Advance by the actual
		// response size; short pages can reflect a server-enforced page cap.
		start += len(page)
	}
	return items, nil
}
