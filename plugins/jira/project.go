package jira

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
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
			if web {
				return format.OpenInBrowser(Site() + "/jira/projects?page=1&sortKey=name&sortOrder=ASC")
			}
			return plugin.WithSession(j, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				projects, raws, err := ListProjects(ctx, s, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(raws)
				}
				if len(projects) == 0 {
					fmt.Println("No projects found (or not authenticated — try: sessh auth login jira).")
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
				fmt.Println(format.Table(rows, []string{"key", "name", "type", "lead"}))
				fmt.Println(format.Gray("\n" + format.Count(len(projects), "project", "projects")))
				return nil
			})(cmd, args)
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	list.Flags().IntVar(&limit, "limit", 0, "cap the number of results")
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
			if viewWeb {
				return format.OpenInBrowser(Site() + "/jira/software/projects/" + key + "/boards")
			}
			return plugin.WithSession(j, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				p, raw, err := GetProject(ctx, s, key)
				if err != nil {
					return err
				}
				if viewJSON {
					return format.PrintJSON(raw)
				}
				fmt.Println(format.Bold(p.Key + "  " + p.Name))
				if desc := text(p.Description); desc != "" {
					fmt.Println(desc)
				}
				fmt.Println()
				format.Line("Type", p.ProjectTypeKey, 10)
				format.Line("Lead", p.Lead.DisplayName, 10)
				format.Line("Category", p.ProjectCategory.Name, 10)
				format.Line("URL", Site()+"/browse/"+p.Key, 10)
				return nil
			})(cmd, args)
		},
	}
	view.Flags().BoolVar(&viewJSON, "json", false, "output raw JSON")
	view.Flags().BoolVar(&viewWeb, "web", false, "open the project in your default browser instead")

	cmd.AddCommand(list, view)
	return cmd
}
