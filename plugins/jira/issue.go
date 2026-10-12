package jira

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/session"
)

func issueCommand(j *Jira) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "issue <command>",
		Aliases: []string{"issues", "task", "tasks"},
		Short:   "Issues and their status",
	}
	cmd.AddCommand(issueListCommand(j), issueViewCommand(j))
	cmd.AddCommand(issueReadCommands(j)...)
	cmd.AddCommand(issueWriteCommands(j)...)
	cmd.AddCommand(issueMetadataCommands(j)...)
	return cmd
}

// StatusColor colors a status by its Jira status category.
func StatusColor(st Status) string {
	switch st.StatusCategory.Key {
	case "done":
		return format.Green(st.Name)
	case "indeterminate":
		return format.Yellow(st.Name)
	case "new":
		return format.Blue(st.Name)
	default:
		return format.Gray(st.Name)
	}
}

func issueListCommand(j *Jira) *cobra.Command {
	var (
		asJSON   bool
		limit    int
		status   string
		assignee string
		rawJQL   string
		web      bool
		extra    searchFilters
		fields   []string
	)
	cmd := &cobra.Command{
		Use:     "list [project]",
		Aliases: []string{"ls"},
		Short:   "List issues",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateLimit(limit); err != nil {
				return err
			}
			if web && asJSON {
				return fmt.Errorf("--web and --json cannot be combined")
			}
			if rawJQL == "" && extra.unassigned && assignee != "" {
				return fmt.Errorf("--unassigned and --assignee cannot be combined")
			}
			project := ""
			if len(args) == 1 {
				project = args[0]
			}
			// Default to "my issues" when nothing bounds the query.
			if project == "" && rawJQL == "" && status == "" && assignee == "" && extra.empty() {
				assignee = "me"
			}
			jql := buildSearchJQL(project, status, assignee, rawJQL, extra)
			if web {
				return format.OpenInBrowser(Site() + "/issues/?jql=" + url.QueryEscape(jql))
			}
			return j.withSession(false, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				issues, raws, err := SearchIssues(ctx, s, jql, limit, issueFields(fields, asJSON, false))
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, raws)
				}
				if len(issues) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No issues match: "+format.Gray(jql))
					return nil
				}
				rows := make([][]string, 0, len(issues))
				for _, it := range issues {
					f := it.Fields
					rows = append(rows, []string{
						format.Green(it.Key),
						format.Truncate(f.Summary, 50),
						StatusColor(f.Status),
						format.Gray(f.IssueType.Name),
						format.Gray(assigneeName(f.Assignee)),
						format.Gray(format.RelativeTime(f.Updated)),
					})
				}
				fmt.Fprintln(cmd.OutOrStdout(), format.Table(rows, []string{"key", "summary", "status", "type", "assignee", "updated"}))
				fmt.Fprintln(cmd.OutOrStdout(), format.Gray("\n"+format.Count(len(issues), "issue", "issues")))
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum results (0 for all; default 0)")
	cmd.Flags().StringVar(&status, "status", "", `filter by status (e.g. "In Progress")`)
	cmd.Flags().StringVar(&assignee, "assignee", "", `filter by assignee ("me" for yourself)`)
	cmd.Flags().StringVar(&rawJQL, "jql", "", "raw JQL and ordering (overrides all generated filters)")
	cmd.Flags().BoolVar(&web, "web", false, "open the search in your default browser instead")
	cmd.Flags().StringVar(&extra.issueType, "type", "", "filter by issue type")
	cmd.Flags().StringVar(&extra.priority, "priority", "", "filter by priority")
	cmd.Flags().StringVar(&extra.label, "label", "", "filter by label")
	cmd.Flags().StringVar(&extra.reporter, "reporter", "", "filter by reporter (me for yourself)")
	cmd.Flags().StringVar(&extra.text, "text", "", "Jira text-search expression")
	cmd.Flags().BoolVar(&extra.unassigned, "unassigned", false, "filter issues without an assignee")
	cmd.Flags().StringSliceVar(&fields, "fields", nil, "comma-separated field IDs; custom fields preserved in JSON")
	return cmd
}

func issueViewCommand(j *Jira) *cobra.Command {
	var (
		asJSON bool
		web    bool
		fields []string
	)
	cmd := &cobra.Command{
		Use:     "view <KEY-123>",
		Aliases: []string{"show"},
		Short:   "Show one issue",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := validateIdentifier(key, "issue key"); err != nil {
				return err
			}
			if web && asJSON {
				return fmt.Errorf("--web and --json cannot be combined")
			}
			if web {
				return format.OpenInBrowser(Site() + "/browse/" + url.PathEscape(key))
			}
			return j.withSession(false, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				it, raw, err := GetIssueFields(ctx, s, key, issueFields(fields, asJSON, true))
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, raw)
				}
				f := it.Fields
				fmt.Fprintln(cmd.OutOrStdout(), format.Bold(it.Key+"  "+f.Summary))
				fmt.Fprintln(cmd.OutOrStdout(), StatusColor(f.Status)+format.Gray(fmt.Sprintf(" • %s • %s • updated %s",
					cmp.Or(f.IssueType.Name, "Issue"), assigneeName(f.Assignee), format.RelativeTime(f.Updated))))
				fmt.Fprintln(cmd.OutOrStdout())
				if desc := text(f.Description); desc != "" {
					fmt.Fprintln(cmd.OutOrStdout(), desc)
					fmt.Fprintln(cmd.OutOrStdout())
				}
				printLine(cmd, "Priority", f.Priority.Name, 10)
				if f.Reporter != nil {
					printLine(cmd, "Reporter", f.Reporter.DisplayName, 10)
				}
				if f.Parent != nil {
					parent := f.Parent.Key
					if f.Parent.Fields.Summary != "" {
						parent += " – " + f.Parent.Fields.Summary
					}
					printLine(cmd, "Parent", parent, 10)
				}
				if len(f.Labels) > 0 {
					printLine(cmd, "Labels", strings.Join(f.Labels, ", "), 10)
				}
				printLine(cmd, "Created", format.RelativeTime(f.Created), 10)
				printLine(cmd, "URL", Site()+"/browse/"+it.Key, 10)
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().BoolVar(&web, "web", false, "open the issue in your default browser instead")
	cmd.Flags().StringSliceVar(&fields, "fields", nil, "comma-separated field IDs; custom fields preserved in JSON")
	return cmd
}

func assigneeName(user *User) string {
	if user == nil {
		return "Unassigned"
	}
	return cmp.Or(user.DisplayName, "Unassigned")
}
