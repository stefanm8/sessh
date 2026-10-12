// Package confluence is the sessh plugin for Confluence Cloud.
//
// Like jira it rides the shared `atlassian` profile, and like jira it may find
// only an id.atlassian.com SSO session there — Confluence answers 403 (not 401)
// for an anonymous caller, so Verify treats both as "no session yet" and lets
// the silent-SSO navigation mint one.
package confluence

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

// Site is the Atlassian site; Wiki is its Confluence mount point.
func Site() string {
	return strings.TrimRight(
		session.Setting("confluence", "site", os.Getenv("CONFLUENCE_SITE"), ""), "/")
}

func Wiki() string { return Site() + "/wiki" }

type Confluence struct{}

func (c *Confluence) Validate() error {
	return session.ValidateSiteSetting(c.Name(), Site(), "CONFLUENCE_SITE")
}

func init() { plugin.Register(&Confluence{}) }

func (c *Confluence) Name() string { return "confluence" }

func (c *Confluence) Profile() session.Profile {
	return session.Profile{
		Name:     "atlassian", // shared with the bitbucket and jira plugins
		Origin:   Site(),
		LoginURL: Wiki() + "/",
		Verify:   verify,
		Whoami:   whoami,
	}
}

func verify(ctx context.Context, s session.Session) error {
	err := session.VerifySSO(ctx, s, Wiki()+"/", whoami)
	if session.IsAuth(err) {
		return session.Authf("atlassian", "no Confluence session for %s. Run: sessh auth login confluence", Wiki())
	}
	return err
}

func whoami(ctx context.Context, s session.Session) (session.Identity, error) {
	raw, err := session.Get(ctx, s, "/wiki/rest/api/user/current")
	if err != nil {
		if session.IsAuth(err) {
			return session.Identity{}, nil
		}
		return session.Identity{}, err
	}
	if !raw.OK {
		return session.Identity{}, fmt.Errorf("Confluence identity: HTTP %d", raw.Status)
	}
	if raw.JSON == nil {
		return session.Identity{}, fmt.Errorf("Confluence identity response is not JSON")
	}
	var me struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
	}
	if err := raw.Decode(&me); err != nil {
		return session.Identity{}, fmt.Errorf("invalid Confluence identity response: %w", err)
	}
	return session.Identity{AccountID: me.AccountID, DisplayName: me.DisplayName, Email: me.Email}, nil
}

func (c *Confluence) Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "confluence <command>",
		Aliases:           []string{"wiki"},
		Short:             "Read Confluence spaces and pages (as Markdown)",
		PersistentPreRunE: func(*cobra.Command, []string) error { return c.Validate() },
		Long: `Spaces, pages and search on your Confluence Cloud site.
Configure CONFLUENCE_SITE or plugins.confluence.site (without /wiki).

A <ref> is anything that identifies a page: a copied URL, a tiny link, a page
id, SPACE:Title, or free text. Pages render as Markdown by default.`,
	}
	cmd.AddCommand(spaceCommand(c), pageCommand(c), searchCommand(c))
	return cmd
}
