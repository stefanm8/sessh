// Package bitbucket is the sessh plugin for Bitbucket Cloud.
//
// It shares the `atlassian` profile with the jira and confluence plugins: one
// Atlassian login covers all three, with access coordinated by the session
// manager.
package bitbucket

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

const (
	baseURL = "https://bitbucket.org"
	// Bitbucket serves REST v2.0 same-origin under the `!api` prefix. We reach
	// it through Playwright's APIRequestContext, which shares the profile's
	// cookies and has no CORS to dodge.
	apiPrefix = "/!api/2.0"
)

// Workspace is configured with BITBUCKET_WORKSPACE or plugins.bitbucket.workspace.
func Workspace() string {
	return session.Setting("bitbucket", "workspace", os.Getenv("BITBUCKET_WORKSPACE"), "")
}

type Bitbucket struct{}

func (b *Bitbucket) Validate() error {
	if strings.TrimSpace(Workspace()) == "" {
		return fmt.Errorf("bitbucket workspace is not configured; set BITBUCKET_WORKSPACE or plugins.bitbucket.workspace in %s", session.ConfigPath())
	}
	return nil
}

func init() { plugin.Register(&Bitbucket{}) }

func (b *Bitbucket) Name() string { return "bitbucket" }

func (b *Bitbucket) Profile() session.Profile {
	return session.Profile{
		Name:     "atlassian", // shared with the jira and confluence plugins
		Origin:   baseURL,
		LoginURL: baseURL + "/" + Workspace() + "/",
		Verify:   verify,
		Whoami:   whoami,
	}
}

// verify checks that the profile carries a live Bitbucket web session.
//
// The cookie jar is authoritative here, not an API call: it is readable even
// mid-SSO, when the page is off-origin and a request would be redirected away.
// This avoids racing an identity request against login redirects.
func verify(ctx context.Context, s session.Session) error {
	cookies, err := s.Cookies(ctx, baseURL)
	if err != nil {
		return err
	}
	for _, c := range cookies {
		if sessionCookie.MatchString(c.Name) && c.Value != "" {
			return nil
		}
	}
	return session.Authf("atlassian", "no Bitbucket session in this profile. Run: sessh auth login bitbucket")
}

var sessionCookie = regexp.MustCompile(`(?i)cloud\.session\.token`)

// whoami is best-effort: /!api/2.0/user is not reachable with a plain web
// session on every tenant, and a missing display name must never downgrade a
// working session to "logged out".
func whoami(ctx context.Context, s session.Session) (session.Identity, error) {
	raw, err := session.Get(ctx, s, apiPath("/user"))
	if err != nil || !raw.OK || raw.JSON == nil {
		return session.Identity{}, nil
	}
	var u struct {
		AccountID   string `json:"account_id"`
		DisplayName string `json:"display_name"`
		Nickname    string `json:"nickname"`
		Username    string `json:"username"`
	}
	if err := raw.Decode(&u); err != nil {
		return session.Identity{}, nil
	}
	return session.Identity{
		AccountID:   u.AccountID,
		DisplayName: u.DisplayName,
		Username:    cmp.Or(u.Nickname, u.Username),
	}, nil
}

// apiPath turns "/repositories/x" into the same-origin API path.
func apiPath(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return apiPrefix + p
	}
	return apiPrefix + "/" + p
}

func (b *Bitbucket) Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "bitbucket <command>",
		Aliases:           []string{"bb"},
		Short:             "Work with Bitbucket Cloud repositories and pull requests",
		Long:              "Repositories, pull requests and CI checks. Configure BITBUCKET_WORKSPACE or plugins.bitbucket.workspace.",
		PersistentPreRunE: func(*cobra.Command, []string) error { return b.Validate() },
	}
	cmd.AddCommand(repoCommand(b), prCommand(b))
	return cmd
}
