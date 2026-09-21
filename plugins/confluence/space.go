package confluence

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

func spaceCommand(c *Confluence) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "space <list|view|tree>",
		Aliases: []string{"spaces"},
		Short:   "Spaces",
	}

	var (
		asJSON bool
		limit  int
		web    bool
	)
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all spaces",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if web {
				return format.OpenInBrowser(Site() + "/wiki/spaces")
			}
			return plugin.WithSession(c, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				spaces, err := New(s).ListSpaces(ctx, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(spaces)
				}
				if len(spaces) == 0 {
					fmt.Println("No spaces found (or not authenticated — try: sessh auth login confluence).")
					return nil
				}
				rows := make([][]string, 0, len(spaces))
				for _, sp := range spaces {
					rows = append(rows, []string{
						format.Green(sp.Key),
						format.Truncate(sp.Name, 45),
						format.Gray(sp.Type),
						format.Gray(format.Truncate(sp.Description, 50)),
					})
				}
				fmt.Println(format.Table(rows, []string{"key", "name", "type", "description"}))
				fmt.Println(format.Gray("\n" + format.Count(len(spaces), "space", "spaces")))
				return nil
			})(cmd, args)
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	list.Flags().IntVar(&limit, "limit", 0, "cap the number of results")
	list.Flags().BoolVar(&web, "web", false, "open the spaces page in your default browser instead")

	var (
		viewJSON bool
		viewWeb  bool
	)
	view := &cobra.Command{
		Use:     "view <KEY>",
		Aliases: []string{"show"},
		Short:   "Show one space",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if viewWeb {
				return format.OpenInBrowser(SpaceURL(Site(), key))
			}
			return plugin.WithSession(c, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				sp, err := New(s).GetSpace(ctx, key)
				if err != nil {
					return err
				}
				if viewJSON {
					return format.PrintJSON(sp)
				}
				fmt.Println(format.Bold(sp.Key + "  " + sp.Name))
				if sp.Description != "" {
					fmt.Println(sp.Description)
				}
				fmt.Println()
				format.Line("Type", sp.Type, 10)
				format.Line("Status", sp.Status, 10)
				format.Line("Id", sp.ID, 10)
				if sp.HomepageID != "" {
					format.Line("Homepage", sp.HomepageID+"  "+
						format.Gray("sessh confluence page view "+sp.HomepageID), 10)
				}
				format.Line("URL", SpaceURL(Site(), sp.Key), 10)
				return nil
			})(cmd, args)
		},
	}
	view.Flags().BoolVar(&viewJSON, "json", false, "output raw JSON")
	view.Flags().BoolVar(&viewWeb, "web", false, "open the space in your default browser instead")

	cmd.AddCommand(list, view, pageTreeCommand(c))
	return cmd
}
