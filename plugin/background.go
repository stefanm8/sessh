package plugin

import (
	"context"
	"encoding/json"
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
		if f.Changed && !seen[f.Name] && f.Name != "background" && f.Name != "foreground" {
			seen[f.Name] = true
			args = append(args, "--"+f.Name+"="+f.Value.String())
		}
	}
	cmd.Flags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	// Commands that detach by default must execute the work in the child,
	// including when restarting a paused worker after login.
	if cmd.Flags().Lookup("foreground") != nil {
		args = append(args, "--foreground=true")
	}
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
	if !st.Started.IsZero() {
		fmt.Fprintln(cmd.OutOrStdout(), "Started:", st.Started.Local().Format(time.RFC3339))
	}
	if st.Detail != "" {
		fmt.Fprintln(cmd.OutOrStdout(), st.Detail)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Log:", st.Log)
}

// AddServiceControls shares lifecycle controls between daemon and capture.
func AddServiceControls(cmd *cobra.Command, args cobra.PositionalArgs, spec func([]string) (background.Spec, error)) {
	for _, action := range []string{"status", "monitor", "stop", "logs"} {
		child := &cobra.Command{Use: action, Args: args}
		switch action {
		case "status":
			child.Short = "Show whether the background or foreground worker is running"
		case "monitor":
			child.Short = "Watch worker status and progress until Ctrl+C (leaves the worker running)"
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
			case "status", "monitor":
				return watchServiceStatus(c, s)
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
		if action == "status" || action == "monitor" {
			child.Flags().Bool("json", false, "print JSON (one object per refresh when watching)")
			child.Flags().Duration("interval", 2*time.Second, "time between status refreshes when watching")
			if action == "status" {
				child.Flags().BoolP("watch", "w", false, "watch status until Ctrl+C (same as monitor)")
			}
		}
		cmd.AddCommand(child)
	}
}

func watchServiceStatus(cmd *cobra.Command, spec background.Spec) error {
	watch, _ := cmd.Flags().GetBool("watch")
	watch = watch || cmd.Name() == "monitor"
	asJSON, _ := cmd.Flags().GetBool("json")
	interval, _ := cmd.Flags().GetDuration("interval")
	if interval <= 0 {
		return fmt.Errorf("--interval must be positive")
	}
	for {
		st, _, err := spec.Inspect(cmd.Context())
		if cmd.Context().Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if asJSON {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(st); err != nil {
				return err
			}
		} else {
			if watch {
				fmt.Fprintln(cmd.OutOrStdout(), "Observed:", time.Now().Format(time.RFC3339))
			}
			printServiceStatus(cmd, st)
		}
		if !watch {
			if st.State == "failed" || st.State == "unreachable" {
				return Exit(1)
			}
			return nil
		}
		// Continue across failures and restarts. Watching never owns or stops
		// the worker; cancelling this command only ends observation.
		timer := time.NewTimer(interval)
		select {
		case <-cmd.Context().Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
