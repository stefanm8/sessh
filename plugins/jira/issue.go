package jira

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

func issueCommand(j *Jira) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "issue <list|view>",
		Aliases: []string{"issues", "task", "tasks"},
		Short:   "Issues and their status",
	}
	cmd.AddCommand(issueListCommand(j), issueViewCommand(j))
	return cmd
}

// BuildJQL turns a project plus filters into a query.
//
// Jira's modern search rejects an "unbounded" query (one that is only an ORDER
// BY), so callers must supply at least one restriction — the list command
// defaults to `assignee = currentUser()`.
func BuildJQL(project, status, assignee, rawJQL string) string {
	if rawJQL != "" {
		return rawJQL
	}
	var clauses []string
	if project != "" {
		clauses = append(clauses, fmt.Sprintf(`project = "%s"`, escapeJQL(project)))
	}
	if status != "" {
		clauses = append(clauses, fmt.Sprintf(`status = "%s"`, escapeJQL(status)))
	}
	if assignee != "" {
		if meRe.MatchString(assignee) {
			clauses = append(clauses, "assignee = currentUser()")
		} else {
			clauses = append(clauses, fmt.Sprintf(`assignee = "%s"`, escapeJQL(assignee)))
		}
	}
	where := strings.Join(clauses, " AND ")
	if where != "" {
		where += " "
	}
	return where + "ORDER BY updated DESC"
}

var meRe = regexp.MustCompile(`(?i)^(me|@me|current(user)?)$`)

func escapeJQL(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }

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
	)
	cmd := &cobra.Command{
		Use:     "list [project]",
		Aliases: []string{"ls"},
		Short:   "List issues",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project := ""
			if len(args) == 1 {
				project = args[0]
			}
			// Default to "my issues" when nothing bounds the query.
			if project == "" && rawJQL == "" && status == "" && assignee == "" {
				assignee = "me"
			}
			jql := BuildJQL(project, status, assignee, rawJQL)
			if web {
				return format.OpenInBrowser(Site() + "/issues/?jql=" + url.QueryEscape(jql))
			}
			return plugin.WithSession(j, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				issues, raws, err := SearchIssues(ctx, s, jql, limit, nil)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(raws)
				}
				if len(issues) == 0 {
					fmt.Println("No issues match: " + format.Gray(jql))
					return nil
				}
				rows := make([][]string, 0, len(issues))
				for _, it := range issues {
					f := it.Fields
					who := "Unassigned"
					if f.Assignee != nil && f.Assignee.DisplayName != "" {
						who = f.Assignee.DisplayName
					}
					rows = append(rows, []string{
						format.Green(it.Key),
						format.Truncate(f.Summary, 50),
						StatusColor(f.Status),
						format.Gray(f.IssueType.Name),
						format.Gray(who),
						format.Gray(format.RelativeTime(f.Updated)),
					})
				}
				fmt.Println(format.Table(rows, []string{"key", "summary", "status", "type", "assignee", "updated"}))
				fmt.Println(format.Gray("\n" + format.Count(len(issues), "issue", "issues")))
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of results")
	cmd.Flags().StringVar(&status, "status", "", `filter by status (e.g. "In Progress")`)
	cmd.Flags().StringVar(&assignee, "assignee", "", `filter by assignee ("me" for yourself)`)
	cmd.Flags().StringVar(&rawJQL, "jql", "", "raw JQL (overrides project/status/assignee)")
	cmd.Flags().BoolVar(&web, "web", false, "open the search in your default browser instead")
	return cmd
}

func issueViewCommand(j *Jira) *cobra.Command {
	var (
		asJSON bool
		web    bool
	)
	cmd := &cobra.Command{
		Use:     "view <KEY-123>",
		Aliases: []string{"show"},
		Short:   "Show one issue",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if web {
				return format.OpenInBrowser(Site() + "/browse/" + key)
			}
			return plugin.WithSession(j, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				it, raw, err := GetIssue(ctx, s, key)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(raw)
				}
				f := it.Fields
				fmt.Println(format.Bold(it.Key + "  " + f.Summary))
				who := "Unassigned"
				if f.Assignee != nil && f.Assignee.DisplayName != "" {
					who = f.Assignee.DisplayName
				}
				kind := f.IssueType.Name
				if kind == "" {
					kind = "Issue"
				}
				fmt.Println(StatusColor(f.Status) + format.Gray(fmt.Sprintf(" • %s • %s • updated %s",
					kind, who, format.RelativeTime(f.Updated))))
				fmt.Println()
				if desc := text(f.Description); desc != "" {
					fmt.Println(desc)
					fmt.Println()
				}
				format.Line("Priority", f.Priority.Name, 10)
				if f.Reporter != nil {
					format.Line("Reporter", f.Reporter.DisplayName, 10)
				}
				if f.Parent != nil {
					parent := f.Parent.Key
					if f.Parent.Fields.Summary != "" {
						parent += " – " + f.Parent.Fields.Summary
					}
					format.Line("Parent", parent, 10)
				}
				if len(f.Labels) > 0 {
					format.Line("Labels", strings.Join(f.Labels, ", "), 10)
				}
				format.Line("Created", format.RelativeTime(f.Created), 10)
				format.Line("URL", Site()+"/browse/"+it.Key, 10)
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().BoolVar(&web, "web", false, "open the issue in your default browser instead")
	return cmd
}
