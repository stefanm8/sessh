package jira

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/session"
)

func projectCommand(j *Jira) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "project <list|view>",
		Aliases: []string{"projects"},
		Short:   `Projects ("spaces")`,
	}

	var (
		asJSON bool
		limit  int
		web    bool
	)
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all projects",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateLimit(limit); err != nil {
				return err
			}
			if web && asJSON {
				return fmt.Errorf("--web and --json cannot be combined")
			}
			if web {
				return format.OpenInBrowser(Site() + "/jira/projects?page=1&sortKey=name&sortOrder=ASC")
			}
			return j.withSession(false, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				projects, raws, err := ListProjects(ctx, s, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return printJSON(cmd, raws)
				}
				if len(projects) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No projects found (or not authenticated — try: sessh auth login jira).")
					return nil
				}
				rows := make([][]string, 0, len(projects))
				for _, p := range projects {
					rows = append(rows, []string{
						format.Green(p.Key),
						format.Truncate(p.Name, 45),
						format.Gray(p.ProjectTypeKey),
						format.Gray(p.Lead.DisplayName),
					})
				}
				fmt.Fprintln(cmd.OutOrStdout(), format.Table(rows, []string{"key", "name", "type", "lead"}))
				fmt.Fprintln(cmd.OutOrStdout(), format.Gray("\n"+format.Count(len(projects), "project", "projects")))
				return nil
			})(cmd, args)
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	list.Flags().IntVar(&limit, "limit", 0, "maximum results (0 for all; default 0)")
	list.Flags().BoolVar(&web, "web", false, "open the projects page in your default browser instead")

	var (
		viewJSON bool
		viewWeb  bool
	)
	view := &cobra.Command{
		Use:     "view <key>",
		Aliases: []string{"show"},
		Short:   "Show one project",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if err := validateIdentifier(key, "project key"); err != nil {
				return err
			}
			if viewWeb && viewJSON {
				return fmt.Errorf("--web and --json cannot be combined")
			}
			if viewWeb {
				return format.OpenInBrowser(Site() + "/jira/software/projects/" + url.PathEscape(key) + "/boards")
			}
			return j.withSession(false, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				p, raw, err := GetProject(ctx, s, key)
				if err != nil {
					return err
				}
				if viewJSON {
					return printJSON(cmd, raw)
				}
				fmt.Fprintln(cmd.OutOrStdout(), format.Bold(p.Key+"  "+p.Name))
				if desc := text(p.Description); desc != "" {
					fmt.Fprintln(cmd.OutOrStdout(), desc)
				}
				fmt.Fprintln(cmd.OutOrStdout())
				printLine(cmd, "Type", p.ProjectTypeKey, 10)
				printLine(cmd, "Lead", p.Lead.DisplayName, 10)
				printLine(cmd, "Category", p.ProjectCategory.Name, 10)
				printLine(cmd, "URL", Site()+"/browse/"+p.Key, 10)
				return nil
			})(cmd, args)
		},
	}
	view.Flags().BoolVar(&viewJSON, "json", false, "output raw JSON")
	view.Flags().BoolVar(&viewWeb, "web", false, "open the project in your default browser instead")

	cmd.AddCommand(list, view)
	cmd.AddCommand(projectMetadataCommands(j)...)
	return cmd
}
