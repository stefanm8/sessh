package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

// `sessh daemon` eagerly starts the same socket server the
// lock holder already runs, started eagerly and kept warm past the last
// command, which removes the per-command browser launch. Deliberately not
// required for correctness — every command works without it.
func newDaemonCommand() *cobra.Command {
	var (
		pluginName string
		idle       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "daemon [plugin]",
		Short: "Hold a browser profile open so later commands skip the launch",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				pluginName = args[0]
			}
			var owner plugin.Plugin
			if pluginName != "" {
				p, ok := plugin.Lookup(pluginName)
				if !ok {
					return fmt.Errorf("unknown plugin %q", pluginName)
				}
				owner = p
			} else {
				var err error
				owner, err = resolvePluginArg(nil)
				if err != nil {
					return err
				}
			}

			h, err := plugin.Acquire(cmd.Context(), owner, session.AcquireOptions{
				Serve:      true,
				SkipVerify: true,
				Notify:     func(m string) { fmt.Fprintln(os.Stderr, format.Gray("sessh: "+m)) },
			})
			if err != nil {
				return err
			}
			defer h.Close()
			if h.Routed {
				return fmt.Errorf("profile %q is already being served by another process", h.Profile.Name)
			}

			fmt.Printf("%s Serving profile %s on %s\n", format.Green("✓"),
				format.Bold(h.Profile.Name), session.SocketPath(h.Profile.Name))
			if idle > 0 {
				fmt.Println(format.Gray(fmt.Sprintf("  Exiting after %s idle. Ctrl+C to stop.", idle)))
			} else {
				fmt.Println(format.Gray("  Ctrl+C to stop."))
			}

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			var timeout <-chan time.Time
			if idle > 0 {
				timeout = time.After(idle)
			}
			select {
			case <-sig:
			case <-cmd.Context().Done():
			case <-timeout:
			}
			fmt.Println(format.Gray("Shutting down."))
			return nil
		},
	}
	cmd.Flags().StringVar(&pluginName, "plugin", "", "which plugin's profile to hold open")
	cmd.Flags().DurationVar(&idle, "idle", 0, "exit after this long (0 = run until interrupted)")
	return cmd
}
