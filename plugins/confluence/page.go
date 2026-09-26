package confluence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

// formats maps --format aliases to what we ask Confluence for.
var formats = map[string]string{
	"md": "markdown", "markdown": "markdown", "text": "markdown",
	"storage": "storage", "html": "storage", "xhtml": "storage",
	"view": "view", "rendered": "view",
	"adf":  "adf",
	"json": "json",
}

func pageCommand(c *Confluence) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "page <list|view|children|tree|attachments>",
		Aliases: []string{"pages"},
		Short:   "Pages — 'view' renders Markdown",
		Long: `A <ref> is anything that identifies a page:

  https://your-tenant.atlassian.net/wiki/spaces/ENG/pages/123456/Example+Page
  https://your-tenant.atlassian.net/wiki/x/AQAAAA  (tiny link)
  123456                                        (page id)
  ENG:Example Page                              (space:title)
  "example project guide"                       (free text — best match)`,
	}
	cmd.AddCommand(
		pageListCommand(c),
		pageViewCommand(c),
		pageChildrenCommand(c),
		pageTreeCommand(c),
		pageAttachmentsCommand(c),
	)
	return cmd
}

func pageListCommand(c *Confluence) *cobra.Command {
	var (
		asJSON bool
		limit  int
		space  string
		web    bool
	)
	cmd := &cobra.Command{
		Use:     "list [SPACE]",
		Aliases: []string{"ls"},
		Short:   "List recently updated pages",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := space
			if len(args) == 1 {
				key = args[0]
			}
			if web {
				if key != "" {
					return format.OpenInBrowser(Wiki() + "/spaces/" + key + "/pages")
				}
				return format.OpenInBrowser(Wiki() + "/")
			}
			max := limit
			if max == 0 {
				max = 50
			}
			return plugin.WithSession(c, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				cl := New(s)
				pages, err := cl.ListPages(ctx, ListPagesOptions{Space: key, Max: max})
				if err != nil {
					return err
				}
				attachSpaceKeys(ctx, cl, pages)
				if asJSON {
					return format.PrintJSON(withURLs(pages))
				}
				if len(pages) == 0 {
					if key != "" {
						fmt.Printf("No pages found in %s.\n", key)
					} else {
						fmt.Println("No pages found (or not authenticated — try: sessh auth login confluence).")
					}
					return nil
				}
				rows := make([][]string, 0, len(pages))
				for _, p := range pages {
					version := ""
					if p.Version > 0 {
						version = fmt.Sprintf("v%d", p.Version)
					}
					rows = append(rows, []string{
						format.Gray(p.ID),
						format.Truncate(p.Title, 60),
						format.Green(p.SpaceKey),
						format.Gray(version),
						format.Gray(format.RelativeTime(p.Updated)),
					})
				}
				fmt.Println(format.Table(rows, []string{"id", "title", "space", "version", "updated"}))
				fmt.Println(format.Gray("\n" + format.Count(len(pages), "page", "pages")))
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of results (default 50)")
	cmd.Flags().StringVar(&space, "space", "", "restrict to a space")
	cmd.Flags().BoolVar(&web, "web", false, "open the page list in your default browser instead")
	return cmd
}

func pageViewCommand(c *Confluence) *cobra.Command {
	var (
		asJSON   bool
		formatFl string
		out      string
		bodyOnly bool
		web      bool
	)
	cmd := &cobra.Command{
		Use:     "view <ref>",
		Aliases: []string{"show", "get"},
		Short:   "Render a page as Markdown",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := strings.Join(args, " ") // titles are often typed unquoted
			want := formatFl
			if want == "" {
				want = "md"
				if asJSON {
					want = "json"
				}
			}
			fmt_, ok := formats[strings.ToLower(want)]
			if !ok {
				return fmt.Errorf("unknown --format %q. Use: md, storage, view, adf, json", formatFl)
			}
			// A URL can be opened without spending a browser launch on
			// resolution.
			if web && isURL(input) {
				return format.OpenInBrowser(input)
			}
			return plugin.WithSession(c, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				cl := New(s)
				ref, err := cl.ResolveRef(ctx, input)
				if err != nil {
					return err
				}
				if web {
					return format.OpenInBrowser(Wiki() + "/pages/viewpage.action?pageId=" + ref.ID)
				}

				wanted := "storage"
				switch fmt_ {
				case "view":
					wanted = "view"
				case "adf":
					wanted = "atlas_doc_format"
				}
				page, err := cl.GetPage(ctx, ref.ID, wanted)
				if err != nil {
					return err
				}
				url := WebURL(Site(), page.Links, page.ID, page.Title, page.SpaceKey)

				if fmt_ == "storage" || fmt_ == "view" || fmt_ == "adf" {
					return emit(page.Body, out, page)
				}

				ancestors := cl.GetAncestors(ctx, ref.ID)
				labels := page.Labels
				if labels == nil {
					labels = cl.GetLabels(ctx, ref.ID)
				}
				users := cl.ResolveUsers(ctx, AccountIDsIn(page.Body))
				markdown := ToMarkdown(page.Body, MarkdownOptions{
					Site: Site(), Wiki: Wiki(), SpaceKey: page.SpaceKey, PageID: page.ID, Users: users,
				})

				if fmt_ == "json" {
					return format.PrintJSON(struct {
						ID         string     `json:"id"`
						Title      string     `json:"title"`
						SpaceKey   string     `json:"spaceKey"`
						SpaceID    string     `json:"spaceId"`
						ParentID   string     `json:"parentId"`
						Status     string     `json:"status"`
						Version    int        `json:"version"`
						Updated    string     `json:"updated"`
						UpdatedBy  string     `json:"updatedBy"`
						CreatedAt  string     `json:"createdAt"`
						Labels     []string   `json:"labels"`
						Ancestors  []Ancestor `json:"ancestors"`
						URL        string     `json:"url"`
						BodyFormat string     `json:"bodyFormat"`
						Markdown   string     `json:"markdown"`
						Body       string     `json:"body"`
					}{
						page.ID, page.Title, page.SpaceKey, page.SpaceID, page.ParentID,
						page.Status, page.Version, page.Updated, page.UpdatedBy, page.CreatedAt,
						labels, ancestors, url, page.BodyFormat, markdown, page.Body,
					})
				}

				doc := markdown
				if !bodyOnly {
					doc = HeaderBlock(page, url, ancestors, labels) + "\n\n" + markdown + "\n"
				}
				return emit(doc, out, page)
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON (same as --format json)")
	cmd.Flags().StringVar(&formatFl, "format", "", "md (default) | storage | view | adf | json")
	cmd.Flags().StringVar(&out, "out", "", "write to a file instead of stdout")
	cmd.Flags().BoolVar(&bodyOnly, "body-only", false, "omit the title/metadata header")
	cmd.Flags().BoolVar(&web, "web", false, "open the page in your default browser instead")
	return cmd
}

func isURL(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// HeaderBlock is the Markdown front matter: title, provenance, breadcrumb,
// labels, link. Written as a blockquote with hard line breaks so the whole
// thing stays valid Markdown.
func HeaderBlock(page *Page, url string, ancestors []Ancestor, labels []string) string {
	var meta []string
	var first []string
	if page.SpaceKey != "" {
		first = append(first, "**"+page.SpaceKey+"**")
	}
	if page.Version > 0 {
		first = append(first, fmt.Sprintf("v%d", page.Version))
	}
	if page.Updated != "" {
		first = append(first, "updated "+format.RelativeTime(page.Updated))
	}
	if page.UpdatedBy != "" {
		first = append(first, "by "+page.UpdatedBy)
	}
	if len(first) > 0 {
		meta = append(meta, strings.Join(first, " · "))
	}
	if len(ancestors) > 0 {
		var titles []string
		for _, a := range ancestors {
			if a.Title != "" {
				titles = append(titles, a.Title)
			}
		}
		if len(titles) > 0 {
			meta = append(meta, "Path: "+strings.Join(titles, " › "))
		}
	}
	if len(labels) > 0 {
		quoted := make([]string, 0, len(labels))
		for _, l := range labels {
			quoted = append(quoted, "`"+l+"`")
		}
		meta = append(meta, "Labels: "+strings.Join(quoted, ", "))
	}
	meta = append(meta, "<"+url+">")
	if page.BodyFormat != "" && page.BodyFormat != "storage" {
		meta = append(meta, "_(rendered from "+page.BodyFormat+" — storage body was empty)_")
	}

	quotedLines := make([]string, 0, len(meta))
	for _, m := range meta {
		quotedLines = append(quotedLines, "> "+m)
	}
	return format.Bold("# "+page.Title) + "\n\n" +
		format.Gray(strings.Join(quotedLines, "  \n")) + "\n\n---"
}

// emit writes to --out or stdout. File output is never colorized.
func emit(text, out string, page *Page) error {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if out == "" {
		_, err := os.Stdout.WriteString(text)
		return err
	}
	file, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, []byte(format.StripANSI(text)), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s Wrote %s %s\n", format.Green("✓"), format.Bold(file), format.Gray("("+page.Title+")"))
	return nil
}

func pageChildrenCommand(c *Confluence) *cobra.Command {
	var (
		asJSON bool
		limit  int
	)
	cmd := &cobra.Command{
		Use:     "children <ref>",
		Aliases: []string{"child"},
		Short:   "List a page's child pages",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := strings.Join(args, " ")
			return plugin.WithSession(c, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				cl := New(s)
				ref, err := cl.ResolveRef(ctx, input)
				if err != nil {
					return err
				}
				kids, err := cl.GetChildren(ctx, ref.ID, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(withURLs(kids))
				}
				if len(kids) == 0 {
					fmt.Println("No child pages.")
					return nil
				}
				rows := make([][]string, 0, len(kids))
				for _, p := range kids {
					rows = append(rows, []string{
						format.Gray(p.ID),
						format.Truncate(p.Title, 60),
						format.Gray(p.Status),
					})
				}
				fmt.Println(format.Table(rows, []string{"id", "title", "status"}))
				fmt.Println(format.Gray("\n" + format.Count(len(kids), "child page", "child pages")))
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of results")
	return cmd
}

// treeBudget stops a runaway walk of a huge space.
const treeBudget = 500

type treeNode struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Children []*treeNode `json:"children"`
}

func pageTreeCommand(c *Confluence) *cobra.Command {
	var (
		asJSON bool
		depth  int
		space  string
	)
	cmd := &cobra.Command{
		Use:   "tree <SPACE|ref>",
		Short: "Show the page hierarchy under a space or page",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := strings.Join(args, " ")
			if arg == "" {
				arg = space
			}
			if arg == "" {
				return fmt.Errorf("usage: sessh confluence page tree <SPACE|url|id> [--depth N]")
			}
			if depth < 1 {
				depth = 3
			}
			return plugin.WithSession(c, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				cl := New(s)
				rootID, rootTitle, err := resolveTreeRoot(ctx, cl, arg)
				if err != nil {
					return err
				}
				root := &treeNode{ID: rootID, Title: rootTitle}
				count, truncated := 0, false
				var walk func(n *treeNode, d int) error
				walk = func(n *treeNode, d int) error {
					if d <= 0 || count >= treeBudget {
						return nil
					}
					kids, err := cl.GetChildren(ctx, n.ID, 200)
					if err != nil {
						return err
					}
					for _, k := range kids {
						if count >= treeBudget {
							truncated = true
							return nil
						}
						count++
						child := &treeNode{ID: k.ID, Title: k.Title}
						n.Children = append(n.Children, child)
						if err := walk(child, d-1); err != nil {
							return err
						}
					}
					return nil
				}
				if err := walk(root, depth); err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(root)
				}
				fmt.Println(format.Bold(root.Title) + format.Gray("  "+root.ID))
				printTree(root, "")
				fmt.Println(format.Gray(fmt.Sprintf("\n%s (depth %d)",
					format.Count(count, "descendant page", "descendant pages"), depth)))
				if truncated {
					fmt.Println(format.Yellow(fmt.Sprintf(
						"Stopped at %d pages — narrow the scope or lower --depth.", treeBudget)))
				}
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&depth, "depth", 3, "how deep to walk")
	cmd.Flags().StringVar(&space, "space", "", "space to start from")
	return cmd
}

var bareKeyRe = regexp.MustCompile(`^[A-Za-z0-9~._-]+$`)

func resolveTreeRoot(ctx context.Context, cl *Client, arg string) (string, string, error) {
	ref := ParseRef(arg)

	// An explicit space (a /spaces/KEY URL), or a bare token that turns out to
	// be a space key — start from the space homepage.
	asSpaceKey := ""
	switch {
	case ref != nil && ref.Kind == RefSpace:
		asSpaceKey = ref.Space
	case ref != nil && ref.Kind == RefSearch && bareKeyRe.MatchString(ref.Text) && !digitsRe.MatchString(ref.Text):
		asSpaceKey = ref.Text
	}
	if asSpaceKey != "" {
		if sp, err := cl.GetSpace(ctx, asSpaceKey); err == nil && sp != nil {
			if sp.HomepageID == "" {
				return "", "", fmt.Errorf("space %s has no homepage to start a tree from", sp.Key)
			}
			home, err := cl.GetPage(ctx, sp.HomepageID, "storage")
			if err != nil {
				return "", "", err
			}
			return home.ID, sp.Key + " — " + home.Title, nil
		}
		// not a space after all — fall through to page resolution
	}

	resolved, err := cl.ResolveRef(ctx, arg)
	if err != nil {
		return "", "", err
	}
	page, err := cl.GetPage(ctx, resolved.ID, "storage")
	if err != nil {
		return "", "", err
	}
	return page.ID, page.Title, nil
}

func printTree(n *treeNode, prefix string) {
	for i, c := range n.Children {
		last := i == len(n.Children)-1
		branch := "├── "
		next := prefix + "│   "
		if last {
			branch = "└── "
			next = prefix + "    "
		}
		fmt.Println(prefix + branch + c.Title + "  " + format.Gray(c.ID))
		printTree(c, next)
	}
}

func pageAttachmentsCommand(c *Confluence) *cobra.Command {
	var (
		asJSON bool
		limit  int
	)
	cmd := &cobra.Command{
		Use:     "attachments <ref>",
		Aliases: []string{"files"},
		Short:   "List a page's attachments",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := strings.Join(args, " ")
			return plugin.WithSession(c, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				cl := New(s)
				ref, err := cl.ResolveRef(ctx, input)
				if err != nil {
					return err
				}
				files, err := cl.GetAttachments(ctx, ref.ID, limit)
				if err != nil {
					return err
				}
				if asJSON {
					return format.PrintJSON(files)
				}
				if len(files) == 0 {
					fmt.Println("No attachments.")
					return nil
				}
				rows := make([][]string, 0, len(files))
				for _, a := range files {
					rows = append(rows, []string{
						format.Truncate(a.Title, 50),
						format.Gray(a.MediaType),
						format.Gray(format.HumanSize(a.Size)),
						format.Gray(format.RelativeTime(a.Updated)),
					})
				}
				fmt.Println(format.Table(rows, []string{"name", "type", "size", "updated"}))
				fmt.Println(format.Gray("\n" + format.Count(len(files), "attachment", "attachments")))
				return nil
			})(cmd, args)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output raw JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap the number of results")
	return cmd
}

// attachSpaceKeys resolves the numeric space ids v2 hands back into the keys
// humans read. Resolution is cached per space, so a listing spanning a handful
// of spaces costs a handful of calls.
func attachSpaceKeys(ctx context.Context, cl *Client, pages []*Page) {
	for _, p := range pages {
		if p.SpaceKey == "" && p.SpaceID != "" {
			p.SpaceKey = cl.SpaceKeyFor(ctx, p.SpaceID)
		}
	}
}

// pageWithURL is the JSON shape for listings: the normalized fields plus a
// resolved web URL.
type pageWithURL struct {
	*Page
	URL string `json:"url"`
}

func withURLs(pages []*Page) []pageWithURL {
	out := make([]pageWithURL, 0, len(pages))
	for _, p := range pages {
		out = append(out, pageWithURL{Page: p, URL: WebURL(Site(), p.Links, p.ID, p.Title, p.SpaceKey)})
	}
	return out
}
