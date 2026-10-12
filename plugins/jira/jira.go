// Package jira is the sessh plugin for Jira Cloud.
//
// It shares the `atlassian` profile with the bitbucket and confluence plugins.
// That sharing is what makes Verify interesting: the profile may hold only an
// id.atlassian.com SSO session (minted by a Bitbucket login) and no tenant
// cookie at all, in which case a direct API call 401s. Verify navigates to the
// tenant, lets Atlassian complete SSO silently, and polls until the session
// becomes available.
package jira

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

// Site is the Jira tenant configured with JIRA_SITE or plugins.jira.site.
func Site() string {
	return strings.TrimRight(
		session.Setting("jira", "site", os.Getenv("JIRA_SITE"), ""), "/")
}

type Jira struct {
	testSession session.Session // injected only by package tests
}

func (j *Jira) Validate() error {
	return session.ValidateSiteSetting(j.Name(), Site(), "JIRA_SITE")
}

func init() { plugin.Register(&Jira{}) }

func (j *Jira) Name() string { return "jira" }

func (j *Jira) Profile() session.Profile {
	return session.Profile{
		Name:     "atlassian", // shared with the bitbucket and confluence plugins
		Origin:   Site(),
		LoginURL: Site() + "/jira/",
		Verify:   verify,
		Whoami:   whoami,
	}
}

// verify ensures the profile carries a live Jira *tenant* session, minting one
// via silent SSO when only an Atlassian id-session is present.
func verify(ctx context.Context, s session.Session) error {
	err := session.VerifySSO(ctx, s, Site()+"/jira/", whoami)
	if session.IsAuth(err) {
		return session.Authf("atlassian", "no Jira session for %s. Run: sessh auth login jira", Site())
	}
	return err
}

func whoami(ctx context.Context, s session.Session) (session.Identity, error) {
	raw, err := session.Get(ctx, s, "/rest/api/3/myself")
	if err != nil {
		if session.IsAuth(err) {
			return session.Identity{}, nil
		}
		return session.Identity{}, err
	}
	if !raw.OK {
		return session.Identity{}, apiError(raw, "Jira identity")
	}
	var me struct {
		AccountID    string `json:"accountId"`
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	}
	if err := raw.Decode(&me); err != nil {
		return session.Identity{}, fmt.Errorf("invalid Jira identity response: %w", err)
	}
	if me.AccountID == "" {
		return session.Identity{}, fmt.Errorf("Jira identity response is missing accountId")
	}
	return session.Identity{AccountID: me.AccountID, DisplayName: me.DisplayName, Email: me.EmailAddress}, nil
}

func (j *Jira) Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "jira <command>",
		Short:             "Work with Jira Cloud issues, projects, boards, and sprints",
		Long:              "Find and update issues, discover fields and users, and explore Jira boards and sprints. Configure JIRA_SITE or plugins.jira.site.",
		PersistentPreRunE: func(*cobra.Command, []string) error { return j.Validate() },
	}
	cmd.AddCommand(projectCommand(j), issueCommand(j))
	cmd.AddCommand(discoveryCommands(j)...)
	cmd.AddCommand(agileCommands(j)...)
	return cmd
}
