package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/internal/background"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

func newDaemonCommand() *cobra.Command {
	var pluginName string
	var idle time.Duration
	var detached bool
	ownerFor := func(args []string) (plugin.Plugin, error) {
		if len(args) == 0 && pluginName != "" {
			args = []string{pluginName}
		}
		return resolvePluginArg(args)
	}
	cmd := &cobra.Command{
		Use: "daemon [plugin]", Short: "Keep a browser ready for later commands",
		Long: `Keep a browser profile open so later commands skip launching it.

Use 'sessh daemon start <plugin>' to run in the background, even after closing
this terminal. Use status, logs, and stop to manage it. Without a subcommand,
runs in the foreground until Ctrl+C. Teams capture already keeps its browser
open; it does not need a separate daemon.`,
		Args: cobra.MaximumNArgs(1),
	}
	run := func(c *cobra.Command, args []string) error {
		owner, err := ownerFor(args)
		if err != nil {
			return err
		}
		if err := plugin.Validate(owner); err != nil {
			return err
		}
		if idle < 0 {
			return fmt.Errorf("--idle cannot be negative")
		}
		spec := plugin.Service("daemon", owner)
		if detached || c.Name() == "start" {
			return plugin.StartBackground(c, spec, []string{owner.Name()})
		}
		return spec.Run(c.Context(), func(ctx context.Context, report func(string, string)) error {
			h, err := plugin.Acquire(ctx, owner, session.AcquireOptions{
				Serve: true, SkipVerify: true, Notify: func(m string) { fmt.Fprintln(c.ErrOrStderr(), "sessh:", m) },
			})
			if err != nil {
				return err
			}
			defer h.Close()
			if h.Routed {
				return fmt.Errorf("profile %q is already kept open by another process (%s); no additional daemon is needed", h.Profile.Name, session.Holder(h.Profile.Name))
			}
			detail := fmt.Sprintf("Serving profile %s. Ctrl+C or sessh daemon stop %s --profile %s to stop.", h.Profile.Name, owner.Name(), h.Profile.Name)
			report("running", detail)
			fmt.Fprintln(c.OutOrStdout(), detail)
			if idle > 0 {
				fmt.Fprintf(c.OutOrStdout(), "Exiting after %s without activity.\n", idle)
			}
			interval := time.Second
			if idle > 0 && idle < interval {
				interval = idle
			}
			tick := time.NewTicker(interval)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-tick.C:
					if idle > 0 && h.IdleFor() >= idle {
						fmt.Fprintln(c.OutOrStdout(), "Idle timeout reached; shutting down.")
						return nil
					}
				}
			}
		})
	}
	cmd.RunE = run
	cmd.PersistentFlags().StringVar(&pluginName, "plugin", "", "which plugin's profile to hold open")
	cmd.PersistentFlags().DurationVar(&idle, "idle", 0, "exit after this long without activity (0 = keep running)")
	cmd.Flags().BoolVarP(&detached, "background", "d", false, "start in the background (same as daemon start)")
	cmd.AddCommand(&cobra.Command{Use: "start [plugin]", Short: "Start in the background and return to your shell", Args: cobra.MaximumNArgs(1), RunE: run})
	plugin.AddServiceControls(cmd, cobra.MaximumNArgs(1), func(args []string) (background.Spec, error) {
		owner, err := ownerFor(args)
		if err != nil {
			return background.Spec{}, err
		}
		return plugin.Service("daemon", owner), nil
	})
	return cmd
}
