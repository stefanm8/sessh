package plugin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stefanm8/sessh/internal/background"
	"github.com/stefanm8/sessh/session"
)

// Service identifies long-running work by its resolved browser profile.
func Service(kind string, owner Plugin) background.Spec {
	profile := ProfileFor(owner).Name
	return background.Spec{Dir: session.RunDir(), Key: kind + ":" + profile, Name: kind + " (profile " + profile + ")"}
}

// BackgroundArgs keeps explicitly supplied flags, including inherited identity
// flags, while removing the flag that would recursively launch another child.
func BackgroundArgs(cmd *cobra.Command, positional []string) []string {
	path := cmd.CommandPath()
	if cmd.Name() == "start" {
		path = cmd.Parent().CommandPath()
	}
	args := append(strings.Fields(path)[1:], positional...)
	seen := map[string]bool{}
	add := func(f *pflag.Flag) {
		if f.Changed && !seen[f.Name] && f.Name != "background" {
			seen[f.Name] = true
			args = append(args, "--"+f.Name+"="+f.Value.String())
		}
	}
	cmd.Flags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	return args
}

// StartBackground starts a worker without a terminal. Workers never open a
// login window; a lapsed session shows up in their status instead.
func StartBackground(cmd *cobra.Command, spec background.Spec, positional []string) error {
	st, existing, err := spec.Start(cmd.Context(), BackgroundArgs(cmd, positional))
	if err != nil {
		return err
	}
	if existing {
		fmt.Fprintln(cmd.OutOrStdout(), "Already running.")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Started in the background; you can close this terminal.")
	}
	printServiceStatus(cmd, st)
	return nil
}

func printServiceStatus(cmd *cobra.Command, st background.Status) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s", st.Name, st.State)
	if st.PID != 0 {
		fmt.Fprintf(cmd.OutOrStdout(), " (pid %d)", st.PID)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	if st.Detail != "" {
		fmt.Fprintln(cmd.OutOrStdout(), st.Detail)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Log:", st.Log)
}

// AddServiceControls shares lifecycle controls between daemon and capture.
func AddServiceControls(cmd *cobra.Command, args cobra.PositionalArgs, spec func([]string) (background.Spec, error)) {
	for _, action := range []string{"status", "stop", "logs"} {
		child := &cobra.Command{Use: action, Args: args}
		switch action {
		case "status":
			child.Short = "Show whether the background or foreground worker is running"
		case "stop":
			child.Short = "Stop the worker and close its browser session cleanly"
		case "logs":
			child.Short = "Show recent background output"
		}
		child.RunE = func(c *cobra.Command, positional []string) error {
			s, err := spec(positional)
			if err != nil {
				return err
			}
			switch c.Name() {
			case "status":
				st, _, err := s.Inspect(c.Context())
				if err != nil {
					return err
				}
				printServiceStatus(c, st)
				if st.State == "failed" || st.State == "unreachable" {
					return Exit(1)
				}
			case "stop":
				ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
				defer cancel()
				stopped, err := s.Stop(ctx)
				if err != nil {
					return err
				}
				if stopped {
					fmt.Fprintln(c.OutOrStdout(), "Stopped", s.Name)
				} else {
					fmt.Fprintln(c.OutOrStdout(), "Not running:", s.Name)
				}
			case "logs":
				follow, _ := c.Flags().GetBool("follow")
				return s.Logs(c.Context(), c.OutOrStdout(), follow)
			}
			return nil
		}
		if action == "logs" {
			child.Flags().BoolP("follow", "f", false, "follow new output until Ctrl+C")
		}
		cmd.AddCommand(child)
	}
}
