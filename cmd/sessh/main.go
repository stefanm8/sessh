// Command sessh is a browser-session hub with pluggable CLIs.
//
// It owns Chromium profiles; tools are plugins that register themselves and are
// handed an authenticated session. Adding a tool means writing its commands and
// nothing about browsers.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"

	// Plugins register themselves from init(). This is the whole wiring.
	_ "github.com/stefanm8/sessh/plugins/bitbucket"
	_ "github.com/stefanm8/sessh/plugins/confluence"
	_ "github.com/stefanm8/sessh/plugins/jira"
	_ "github.com/stefanm8/sessh/plugins/teams"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCommand()
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(report(err))
	}
}

func report(err error) int {
	var exit *plugin.ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	if session.IsAuth(err) {
		fmt.Fprintln(os.Stderr, format.Yellow(err.Error()))
		return 1
	}
	var busy *session.ErrBusy
	if errors.As(err, &busy) {
		fmt.Fprintln(os.Stderr, format.Yellow(err.Error()))
		return 1
	}
	fmt.Fprintln(os.Stderr, format.Red("Error:"), err.Error())
	return 1
}

func newRootCommand() *cobra.Command {
	flags := &plugin.Flags{}
	plugin.BindFlags(flags)

	root := &cobra.Command{
		Use:   "sessh <plugin> <command> [flags]",
		Short: "Session shell — one login, many tools",
		Long: format.Bold("sessh (session shell)") + ` — one login, many tools.

sessh owns the Chromium profiles. Each plugin declares which profile it needs;
plugins that name the same profile share one login (bitbucket, jira and
confluence are all one Atlassian account). Log in once with ` + format.Bold("sessh auth login") + `;
every other command runs headless against the saved session.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flags.Profile, "profile", "", "browser profile to use (default: the plugin's own)")
	pf.StringVar(&flags.From, "from", "", "when creating a profile, seed it from this one instead of `default`")
	pf.BoolVar(&flags.NoInherit, "no-inherit", false, "when creating a profile, start it empty")
	pf.BoolVar(&flags.Login, "login", false, "open a headed window if the session has lapsed")
	pf.BoolVar(&flags.Verbose, "verbose", false, "print session bookkeeping to stderr")

	root.AddCommand(newAuthCommand())
	root.AddCommand(newDaemonCommand())
	for _, p := range plugin.All() {
		root.AddCommand(p.Command())
	}
	return root
}
