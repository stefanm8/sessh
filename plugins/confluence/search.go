package confluence

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

func searchCommand(c *Confluence) *cobra.Command {
	var (
		asJSON bool
		limit  int
		space  string
		typ    string
		cql    string
		web    bool
	)
	cmd := &cobra.Command{
		Use:     "search <text>",
		Aliases: []string{"find"},
		Short:   "Search the wiki (CQL under the hood)",
		Args:    cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.Join(args, " ")
			if text == "" && cql == "" && space == "" {
				return fmt.Errorf(`usage: sessh confluence search <text> [--space KEY] [--type page] [--cql "<raw CQL>"]`)
			}
			max := limit
			if max == 0 {
				max = 25
			}
			if web {
				q := cql
				if q == "" {
					q = text
				}
				return format.OpenInBrowser(Wiki() + "/search?text=" + url.QueryEscape(q))
			}
			return plugin.WithSession(c, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				cl := New(s)
				var (
					hits []Hit
					err  error
				)
				if cql != "" {
					hits, err = cl.Search(ctx, cql, max)
				} else {
					hits, err = cl.SearchText(ctx, text, space, typ, max)
				}
				if err != nil {
					return err
				}
				if asJSON {
					type hitWithURL struct {
						Hit
						FullURL string `json:"url"`
					}
					out := make([]hitWithURL, 0, len(hits))
					for _, h := range hits {
						out = append(out, hitWithURL{Hit: h, FullURL: fullURL(h)})
					}
					return format.PrintJSON(out)
				}
				if len(hits) == 0 {
					q := cql
					if q == "" {
						q = text
					}
					fmt.Println("No results for " + format.Gray(q) + ".")
					return nil
				}
				rows := make([][]string, 0, len(hits))
				for _, h := range hits {
					where := h.SpaceKey
					if where == "" {
						where = h.Space
					}
					rows = append(rows, []string{
						format.Gray(h.ID),
						format.Truncate(h.Title, 60),
						format.Green(where),
						format.Gray(h.Type),
						format.Gray(format.RelativeTime(h.LastModified)),
					})
				}
				fmt.Println(format.Table(rows, []string{"id", "title", "space", "type", "updated"}))
				fmt.Println(format.Gray("\n" + format.Count(len(hits), "result", "results")))
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of results (default 25)")
	cmd.Flags().StringVar(&space, "space", "", "restrict to a space")
	cmd.Flags().StringVar(&typ, "type", "", "page (default) | blogpost | comment | attachment")
	cmd.Flags().StringVar(&cql, "cql", "", "raw CQL, overrides the text query")
	cmd.Flags().BoolVar(&web, "web", false, "open the search in your default browser instead")
	return cmd
}

// fullURL absolutises a search hit's path, which is relative to the /wiki
// mount point.
func fullURL(h Hit) string {
	if h.URL == "" {
		return ""
	}
	if isURL(h.URL) {
		return h.URL
	}
	if strings.HasPrefix(h.URL, "/wiki") {
		return Site() + h.URL
	}
	return Wiki() + h.URL
}
