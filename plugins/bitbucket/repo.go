package bitbucket

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

func repoCommand(b *Bitbucket) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "repo <list|view>",
		Aliases: []string{"repos"},
		Short:   "Work with repositories",
	}

	var (
		asJSON bool
		limit  int
		web    bool
	)

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List repositories in the workspace",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws := Workspace()
			if web {
				return format.OpenInBrowser(baseURL + "/" + ws + "/workspace/repositories/")
			}
			return plugin.WithSession(b, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				repos, err := ListRepos(ctx, s, ws, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(repos.Raw)
				}
				if len(repos.Items) == 0 {
					fmt.Printf("No repositories found in %s.\n", ws)
					return nil
				}
				rows := make([][]string, 0, len(repos.Items))
				for _, r := range repos.Items {
					visibility := format.Green("public")
					if r.IsPrivate {
						visibility = format.Yellow("private")
					}
					name := r.Slug
					if name == "" {
						name = r.Name
					}
					rows = append(rows, []string{
						format.Bold(name),
						visibility,
						r.MainBranch.Name,
						format.Gray(format.RelativeTime(r.UpdatedOn)),
						format.Truncate(r.Description, 60),
					})
				}
				fmt.Println(format.Table(rows, []string{"name", "visibility", "branch", "updated", "description"}))
				fmt.Println(format.Gray(fmt.Sprintf("\n%s in %s",
					format.Count(len(repos.Items), "repository", "repositories"), ws)))
				return nil
			})(cmd, args)
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	list.Flags().IntVar(&limit, "limit", 0, "cap the number of results")
	list.Flags().BoolVar(&web, "web", false, "open the page in your default browser instead")

	var (
		viewJSON bool
		viewWeb  bool
	)
	view := &cobra.Command{
		Use:     "view <repo>",
		Aliases: []string{"show"},
		Short:   "Show one repository",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws := Workspace()
			repo := args[0]
			if viewWeb {
				return format.OpenInBrowser(baseURL + "/" + ws + "/" + repo)
			}
			return plugin.WithSession(b, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				r, raw, err := GetRepo(ctx, s, ws, repo)
				if err != nil {
					return err
				}
				if viewJSON {
					return format.PrintJSON(raw)
				}
				name := r.FullName
				if name == "" {
					name = ws + "/" + repo
				}
				fmt.Println(format.Bold(name))
				if r.Description != "" {
					fmt.Println(r.Description)
				}
				fmt.Println()
				visibility := "public"
				if r.IsPrivate {
					visibility = "private"
				}
				format.Line("Visibility", visibility, 12)
				format.Line("Main branch", r.MainBranch.Name, 12)
				format.Line("Language", r.Language, 12)
				if r.Size > 0 {
					format.Line("Size", fmt.Sprintf("%.1f MB", float64(r.Size)/1024/1024), 12)
				}
				format.Line("Updated", format.RelativeTime(r.UpdatedOn), 12)
				format.Line("URL", r.Links.HTML.Href, 12)
				return nil
			})(cmd, args)
		},
	}
	view.Flags().BoolVar(&viewJSON, "json", false, "output raw JSON")
	view.Flags().BoolVar(&viewWeb, "web", false, "open the page in your default browser instead")

	cmd.AddCommand(list, view)
	return cmd
}
