package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/session"
)

const maxInputBytes = 1 << 20

type mutationOptions struct{ dryRun, asJSON bool }

func (o *mutationOptions) flags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "print exact request without writing (may read identity, issue types, or transitions)")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "output JSON")
}

type mutationRequest struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Payload any    `json:"payload"`
}

func executeMutation(ctx context.Context, s session.Session, cmd *cobra.Command, o mutationOptions, method, path string, payload any, key, created string) error {
	if o.dryRun {
		return printJSON(cmd, mutationRequest{method, path, payload})
	}
	raw, err := requestJSON(ctx, s, method, path, payload)
	if err != nil {
		return err
	}
	if raw != nil && (len(raw) == 0 || raw[0] != '{') {
		return fmt.Errorf("Jira accepted the write request but returned an invalid result; inspect Jira before retrying")
	}
	var result struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	decodeErr := json.Unmarshal(raw, &result)
	if created != "" {
		if decodeErr != nil || result.ID == "" || (created == "issue" && result.Key == "") {
			return fmt.Errorf("Jira accepted the %s request but returned an invalid result; inspect Jira before retrying", created)
		}
		if o.asJSON {
			return printJSON(cmd, raw)
		}
		if created == "issue" {
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n%s/browse/%s\n", result.Key, Site(), url.PathEscape(result.Key))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Added comment %s to %s\n", result.ID, key)
		}
		return nil
	}
	if raw != nil {
		// Assign and transition document an empty 204. Edit may return an
		// IssueBean (200); arbitrary objects such as proxy error payloads do
		// not establish that the requested update succeeded.
		edit := method == "PUT" && !strings.HasSuffix(path, "/assignee")
		if !edit || decodeErr != nil || result.ID == "" || result.Key == "" {
			return fmt.Errorf("Jira accepted the write request but returned an unexpected result; inspect Jira before retrying")
		}
	}
	if o.asJSON {
		return printJSON(cmd, map[string]any{"success": true, "key": key})
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Updated "+key)
	return nil
}

func readInput(cmd *cobra.Command, path string) ([]byte, error) {
	var r io.Reader = cmd.InOrStdin()
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxInputBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxInputBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("input must be valid UTF-8 text")
	}
	return data, nil
}
func loadFields(cmd *cobra.Command, path string) (map[string]any, error) {
	fields := map[string]any{}
	if path == "" {
		return fields, nil
	}
	data, err := readInput(cmd, path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("--fields-json must contain a JSON fields object")
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid --fields-json: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid field ID in --fields-json")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate field %q in --fields-json", key)
		}
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid field %s: %w", key, err)
		}
		fields[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("invalid --fields-json: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("--fields-json must contain exactly one JSON object")
	}

	for k := range fields {
		if strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("--fields-json contains an empty field ID")
		}
		if k == "fields" || k == "update" || k == "transition" || k == "status" {
			return nil, fmt.Errorf("--fields-json accepts field IDs only; %q cannot be set here", k)
		}
	}
	if err := validateStandardFields(fields); err != nil {
		return nil, err
	}
	return fields, nil
}
func addField(fields map[string]any, key string, value any) error {
	if _, ok := fields[key]; ok {
		return fmt.Errorf("field %q is set by both a named flag and --fields-json", key)
	}
	fields[key] = value
	return nil
}
func plainADF(value string) any {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	content := []any{}
	for _, line := range strings.Split(value, "\n") {
		p := map[string]any{"type": "paragraph"}
		if line != "" {
			p["content"] = []any{map[string]any{"type": "text", "text": line}}
		}
		content = append(content, p)
	}
	return map[string]any{"version": 1, "type": "doc", "content": content}
}
func resolveAssignee(ctx context.Context, s session.Session, v string) (any, error) {
	if strings.EqualFold(v, "unassigned") {
		return nil, nil
	}
	if meRe.MatchString(v) {
		var user User
		if err := getJSON(ctx, s, "/rest/api/3/myself", &user); err != nil {
			return nil, err
		}
		if user.AccountID == "" {
			return nil, fmt.Errorf("Jira did not return your account ID")
		}
		return user.AccountID, nil
	}
	if err := validateIdentifier(v, "account ID"); err != nil {
		return nil, err
	}
	return v, nil
}
func resolveIssueType(ctx context.Context, s session.Session, project, value string) (any, error) {
	if _, err := strconv.ParseUint(value, 10, 64); err == nil && value != "0" {
		return map[string]any{"id": value}, nil
	}
	raws, err := pagedValues(ctx, s, "/rest/api/3/issue/createmeta/"+url.PathEscape(project)+"/issuetypes", "issueTypes", 0)
	if err != nil {
		return nil, err
	}
	matches := []string{}
	for _, raw := range raws {
		var v struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if v.ID == "" || v.Name == "" {
			return nil, fmt.Errorf("Jira returned an invalid issue type")
		}
		if strings.EqualFold(v.Name, value) {
			matches = append(matches, v.ID)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("issue type %q matches %d create-capable types; use an ID from jira project types %s", value, len(matches), project)
	}
	return map[string]any{"id": matches[0]}, nil
}

type issueFieldsOptions struct {
	summary, description, descriptionFile, issueType, assignee, priority, parent, fieldsFile string
	labels                                                                                   []string
}

func (o *issueFieldsOptions) flags(cmd *cobra.Command, create bool) {
	summaryHelp := "issue summary"
	if create {
		summaryHelp = "issue summary (required unless fields-json supplies summary)"
	}
	cmd.Flags().StringVar(&o.summary, "summary", "", summaryHelp)
	cmd.Flags().StringVar(&o.description, "description", "", "plain-text description (empty clears on edit)")
	cmd.Flags().StringVar(&o.descriptionFile, "description-file", "", "read plain-text description from a file, or - for stdin")
	cmd.Flags().StringVar(&o.priority, "priority", "", "priority name")
	cmd.Flags().StringSliceVar(&o.labels, "labels", nil, "comma-separated replacement labels (empty clears)")
	cmd.Flags().StringVar(&o.parent, "parent", "", "parent issue key (empty clears on edit)")
	cmd.Flags().StringVar(&o.fieldsFile, "fields-json", "", "file containing additional Jira fields object, or - for stdin; duplicate named fields fail")
	if create {
		cmd.Flags().StringVar(&o.issueType, "type", "", "issue type ID or unique name (required unless fields-json supplies issuetype)")
		cmd.Flags().StringVar(&o.assignee, "assignee", "", "account ID, me, or unassigned")
	}
}
func (o issueFieldsOptions) build(cmd *cobra.Command, create bool, project string) (map[string]any, error) {
	if cmd.Flags().Changed("description") && cmd.Flags().Changed("description-file") {
		return nil, fmt.Errorf("--description and --description-file cannot be combined")
	}
	if o.descriptionFile == "-" && o.fieldsFile == "-" {
		return nil, fmt.Errorf("only one input flag may read stdin")
	}
	for _, name := range []string{"description-file", "fields-json"} {
		if cmd.Flags().Changed(name) && cmd.Flags().Lookup(name).Value.String() == "" {
			return nil, fmt.Errorf("--%s requires a file path or -", name)
		}
	}
	fields, err := loadFields(cmd, o.fieldsFile)
	if err != nil {
		return nil, err
	}
	if create {
		if err := addField(fields, "project", map[string]any{"key": project}); err != nil {
			return nil, err
		}
	}
	for _, v := range []struct {
		flag, key string
		value     any
	}{{"summary", "summary", o.summary}, {"priority", "priority", map[string]any{"name": o.priority}}, {"parent", "parent", map[string]any{"key": o.parent}}, {"labels", "labels", o.labels}} {
		if !cmd.Flags().Changed(v.flag) {
			continue
		}
		if v.flag == "summary" && strings.TrimSpace(o.summary) == "" {
			return nil, fmt.Errorf("--summary cannot be empty")
		}
		if v.flag == "priority" && strings.TrimSpace(o.priority) == "" {
			return nil, fmt.Errorf("--priority cannot be empty; use --fields-json for explicit null")
		}
		if v.flag == "parent" {
			if o.parent == "" {
				if create {
					return nil, fmt.Errorf("--parent cannot be empty when creating an issue")
				}
				v.value = nil
			} else if err := validateIdentifier(o.parent, "parent issue key"); err != nil {
				return nil, err
			}
		}
		if v.flag == "labels" {
			labels := []string{}
			for _, label := range o.labels {
				if label == "" {
					continue
				}
				if strings.ContainsAny(label, " \t\r\n") {
					return nil, fmt.Errorf("labels must not contain whitespace")
				}
				labels = append(labels, label)
			}
			v.value = labels
		}
		if err := addField(fields, v.key, v.value); err != nil {
			return nil, err
		}
	}
	if cmd.Flags().Changed("description") || cmd.Flags().Changed("description-file") {
		desc := o.description
		if o.descriptionFile != "" {
			data, err := readInput(cmd, o.descriptionFile)
			if err != nil {
				return nil, err
			}
			desc = string(data)
		}
		var value any
		if desc != "" {
			value = plainADF(desc)
		}
		if err := addField(fields, "description", value); err != nil {
			return nil, err
		}
	}
	if create {
		if cmd.Flags().Changed("type") {
			if strings.TrimSpace(o.issueType) == "" {
				return nil, fmt.Errorf("--type cannot be empty")
			}
			if _, ok := fields["issuetype"]; ok {
				return nil, fmt.Errorf("field issuetype is set by both --type and --fields-json")
			}
		}
		if !cmd.Flags().Changed("type") {
			v, ok := fields["issuetype"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("--type is required (or issuetype object in --fields-json)")
			}
			id, _ := v["id"].(string)
			name, _ := v["name"].(string)
			if strings.TrimSpace(id) == "" && strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf("issuetype in --fields-json requires a non-empty id or name")
			}
		}
		if summary, ok := fields["summary"].(string); !ok || strings.TrimSpace(summary) == "" {
			return nil, fmt.Errorf("--summary is required (or non-empty summary in --fields-json)")
		}
		if cmd.Flags().Changed("assignee") {
			if err := validateIdentifier(o.assignee, "account ID"); err != nil {
				return nil, err
			}
			if _, ok := fields["assignee"]; ok {
				return nil, fmt.Errorf("field assignee is set by both --assignee and --fields-json")
			}
		}
	} else if len(fields) == 0 {
		return nil, fmt.Errorf("no fields to edit; supply a field flag or non-empty --fields-json")
	}
	if err := validateStandardFields(fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func issueWriteCommands(j *Jira) []*cobra.Command {
	commands := []*cobra.Command{}
	for _, create := range []bool{true, false} {
		var o issueFieldsOptions
		var options mutationOptions
		use, short := "edit <KEY>", "Edit issue fields (omitted fields stay unchanged)"
		if create {
			use, short = "create <PROJECT>", "Create an issue"
		}
		cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1)}
		o.flags(cmd, create)
		options.flags(cmd)
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if err := validateIdentifier(args[0], "project or issue key"); err != nil {
				return err
			}
			fields, err := o.build(cmd, create, args[0])
			if err != nil {
				return err
			}
			return j.withSession(true, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
				if create {
					typeName := o.issueType
					if !cmd.Flags().Changed("type") {
						v := fields["issuetype"].(map[string]any)
						if id, _ := v["id"].(string); id == "" {
							typeName, _ = v["name"].(string)
						}
					}
					if typeName != "" {
						v, err := resolveIssueType(ctx, s, args[0], typeName)
						if err != nil {
							return err
						}
						fields["issuetype"] = v
					}
					if cmd.Flags().Changed("assignee") {
						v, err := resolveAssignee(ctx, s, o.assignee)
						if err != nil {
							return err
						}
						if v == nil {
							fields["assignee"] = nil
						} else {
							fields["assignee"] = map[string]any{"accountId": v}
						}
					}
				}
				method, path, created := "PUT", "/rest/api/3/issue/"+url.PathEscape(args[0]), ""
				if create {
					method, path, created = "POST", "/rest/api/3/issue", "issue"
				}
				payload := map[string]any{"fields": fields}
				if parent, ok := fields["parent"]; !create && ok && parent == nil {
					delete(fields, "parent")
					payload["update"] = map[string]any{"parent": []any{map[string]any{"set": map[string]any{"none": true}}}}
				}
				return executeMutation(ctx, s, cmd, options, method, path, payload, args[0], created)
			})(cmd, args)
		}
		commands = append(commands, cmd)
	}
	var assignOptions mutationOptions
	assign := &cobra.Command{Use: "assign <KEY> <ACCOUNT_ID|me|unassigned>", Short: "Assign an issue using an account ID", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateIdentifier(args[0], "issue key"); err != nil {
			return err
		}
		if err := validateIdentifier(args[1], "account ID"); err != nil {
			return err
		}
		return j.withSession(true, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
			v, err := resolveAssignee(ctx, s, args[1])
			if err != nil {
				return err
			}
			return executeMutation(ctx, s, cmd, assignOptions, "PUT", "/rest/api/3/issue/"+url.PathEscape(args[0])+"/assignee", map[string]any{"accountId": v}, args[0], "")
		})(cmd, args)
	}}
	assignOptions.flags(assign)
	commands = append(commands, assign)
	var transitionOptions mutationOptions
	var fieldsFile string
	transition := &cobra.Command{Use: "transition <KEY> <TRANSITION_ID>", Short: "Apply an available transition by ID", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateIdentifier(args[0], "issue key"); err != nil {
			return err
		}
		id, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil || id == 0 {
			return fmt.Errorf("transition ID must be a positive integer; use jira issue transitions %s", args[0])
		}
		if cmd.Flags().Changed("fields-json") && fieldsFile == "" {
			return fmt.Errorf("--fields-json requires a file path or -")
		}
		fields, err := loadFields(cmd, fieldsFile)
		if err != nil {
			return err
		}
		return j.withSession(true, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
			items, _, err := getTransitions(ctx, s, args[0])
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(items, func(item Transition) bool { return item.ID == args[1] }) {
				return fmt.Errorf("transition %s is unavailable; use jira issue transitions %s", args[1], args[0])
			}
			// Required transition-screen fields may already be populated on the
			// issue. Jira validates the resulting state, so omission is not a local
			// failure; --fields-json supplies values when the screen needs them.

			payload := map[string]any{"transition": map[string]any{"id": args[1]}}
			if len(fields) > 0 {
				payload["fields"] = fields
			}
			return executeMutation(ctx, s, cmd, transitionOptions, "POST", "/rest/api/3/issue/"+url.PathEscape(args[0])+"/transitions", payload, args[0], "")
		})(cmd, args)
	}}
	transition.Flags().StringVar(&fieldsFile, "fields-json", "", "file containing transition fields object, or - for stdin")
	transitionOptions.flags(transition)
	commands = append(commands, transition)
	var commentOptions mutationOptions
	var body, bodyFile string
	comment := &cobra.Command{Use: "comment <command>", Short: "Issue comments"}
	add := &cobra.Command{Use: "add <KEY>", Short: "Add a plain-text comment", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateIdentifier(args[0], "issue key"); err != nil {
			return err
		}
		if cmd.Flags().Changed("body") == cmd.Flags().Changed("body-file") {
			return fmt.Errorf("supply exactly one of --body or --body-file")
		}
		value := body
		if cmd.Flags().Changed("body-file") {
			if bodyFile == "" {
				return fmt.Errorf("--body-file requires a file path or -")
			}
			data, err := readInput(cmd, bodyFile)
			if err != nil {
				return err
			}
			value = string(data)
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("comment body cannot be empty")
		}
		return j.withSession(true, func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error {
			return executeMutation(ctx, s, cmd, commentOptions, "POST", "/rest/api/3/issue/"+url.PathEscape(args[0])+"/comment", map[string]any{"body": plainADF(value)}, args[0], "comment")
		})(cmd, args)
	}}
	add.Flags().StringVar(&body, "body", "", "plain-text comment body")
	add.Flags().StringVar(&bodyFile, "body-file", "", "read plain-text comment from a file, or - for stdin")
	commentOptions.flags(add)
	comment.AddCommand(add)
	commands = append(commands, comment)
	return commands
}

// Validate portable field shapes locally; Jira remains authoritative for
// tenant-specific schemas, editability, allowed values, and custom fields.
func validateStandardFields(fields map[string]any) error {
	if v, ok := fields["summary"]; ok {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return fmt.Errorf("summary must be a non-empty string")
		}
	}
	if v, ok := fields["description"]; ok && v != nil {
		doc, ok := v.(map[string]any)
		if !ok || doc["type"] != "doc" || fmt.Sprint(doc["version"]) != "1" {
			return fmt.Errorf("description must be null or an ADF document with type doc and version 1")
		}
		if _, ok := doc["content"].([]any); !ok {
			return fmt.Errorf("ADF description must contain a content array")
		}
	}
	if v, ok := fields["labels"]; ok {
		var labels []string
		switch v := v.(type) {
		case []string:
			labels = v
		case []any:
			for _, item := range v {
				label, ok := item.(string)
				if !ok {
					return fmt.Errorf("labels must be an array of strings")
				}
				labels = append(labels, label)
			}
		default:
			return fmt.Errorf("labels must be an array of strings (use [] to clear)")
		}
		for _, label := range labels {
			if label == "" || strings.ContainsAny(label, " \t\r\n") {
				return fmt.Errorf("labels must be non-empty and contain no whitespace")
			}
		}
	}
	for _, key := range []string{"issuetype", "priority", "parent", "assignee"} {
		v, ok := fields[key]
		if !ok {
			continue
		}
		if v == nil {
			if key == "issuetype" {
				return fmt.Errorf("issuetype cannot be null")
			}
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", key)
		}
		keys := []string{"id", "name"}
		if key == "parent" {
			keys = []string{"id", "key"}
		}
		if key == "assignee" {
			keys = []string{"accountId"}
		}
		found := false
		for _, name := range keys {
			if raw, ok := obj[name]; ok {
				value, ok := raw.(string)
				if !ok || strings.TrimSpace(value) == "" {
					return fmt.Errorf("%s.%s must be a non-empty string", key, name)
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s requires %s", key, strings.Join(keys, " or "))
		}
	}
	return nil
}
