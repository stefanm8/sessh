package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

// `sessh auth login` is the only headed code path in the binary.
// Everything else runs headless and takes the profile as it finds it.

func newAuthCommand() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth <login|status|logout|list>",
		Short: "Manage browser login sessions",
		Long: `Log in once, use the session everywhere.

A login belongs to a *profile*, not to a plugin: every plugin that names the
same profile shares it. Nothing but ` + format.Bold("auth login") + ` ever opens a browser window.`,
	}

	auth.AddCommand(&cobra.Command{
		Use:   "login [plugin]",
		Short: "Log in (opens a browser once)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, err := resolvePluginArg(args)
			if err != nil {
				return err
			}
			timeout, _ := cmd.Flags().GetDuration("timeout")
			return plugin.Login(cmd.Context(), owner, plugin.LoginOptions{Timeout: timeout})
		},
	})
	auth.Commands()[0].Flags().Duration("timeout", 5*time.Minute, "how long to wait for the login to complete")

	auth.AddCommand(&cobra.Command{
		Use:   "status [plugin]",
		Short: "Check whether the saved session still works",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return statusAll(cmd.Context())
			}
			owner, err := resolvePluginArg(args)
			if err != nil {
				return err
			}
			ok, err := statusOne(cmd.Context(), owner, true)
			if err != nil {
				return err
			}
			if !ok {
				return plugin.Exit(1)
			}
			return nil
		},
	})

	auth.AddCommand(&cobra.Command{
		Use:   "logout [plugin]",
		Short: "Forget the saved session (deletes the browser profile)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("profile")
			if name == "" {
				owner, err := resolvePluginArg(args)
				if err != nil {
					return err
				}
				name = plugin.ProfileFor(owner).Name
			}
			shared := plugin.SharingProfile(name, "")
			if err := session.Forget(name); err != nil {
				return err
			}
			fmt.Printf("%s Logged out of profile %s (local session cleared).\n",
				format.Green("✓"), format.Bold(name))
			if len(shared) > 1 {
				fmt.Println(format.Gray("  This profile was shared with: " + strings.Join(shared, ", ")))
			}
			return nil
		},
	})

	importCmd := &cobra.Command{
		Use:   "import <chromium-user-data-dir>",
		Short: "Adopt an existing browser profile's session",
		Long: `Seed a sessh profile from a Chromium user-data directory you are already
logged in with. Close the source browser and back up its profile first.

Only credentials are copied (cookies and localStorage), never IndexedDB or
caches. Chromium opens the source to read it and may update browser metadata.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			into, _ := cmd.Flags().GetString("into")
			if into == "" {
				owner, err := resolvePluginArg(nil)
				if err != nil {
					return err
				}
				into = plugin.ProfileFor(owner).Name
			}
			src, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			if err := session.ImportProfile(into, src); err != nil {
				return err
			}
			fmt.Printf("%s Imported the session from %s into profile %s\n",
				format.Green("✓"), format.Bold(src), format.Bold(into))
			fmt.Println(format.Gray("  Verify it with: sessh auth status"))
			return nil
		},
	}
	importCmd.Flags().String("into", "", "profile to seed (default: the profile the plugins resolve to)")
	auth.AddCommand(importCmd)

	auth.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List browser profiles and where they came from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listProfiles()
		},
	})

	return auth
}

// resolvePluginArg turns an optional plugin name into a plugin. With no
// argument we take the first registered plugin whose profile is the one the
// user is pointing at, which makes `sessh auth login` on its own do the obvious
// thing.
func resolvePluginArg(args []string) (plugin.Plugin, error) {
	if len(args) == 1 {
		p, ok := plugin.Lookup(args[0])
		if !ok {
			return nil, fmt.Errorf("unknown plugin %q (have: %s)", args[0], strings.Join(pluginNames(), ", "))
		}
		return p, nil
	}
	all := plugin.All()
	if len(all) == 0 {
		return nil, fmt.Errorf("no plugins are registered")
	}
	// Prefer a plugin already pointed at the resolved default profile.
	want := session.ResolveProfileName("", plugin.GlobalFlags().Profile, session.DefaultProfile)
	for _, p := range all {
		if plugin.ProfileFor(p).Name == want {
			return p, nil
		}
	}
	return all[0], nil
}

func pluginNames() []string {
	var names []string
	for _, p := range plugin.All() {
		names = append(names, p.Name())
	}
	sort.Strings(names)
	return names
}

func statusAll(ctx context.Context) error {
	allOK := true
	for _, p := range plugin.All() {
		ok, err := statusOne(ctx, p, false)
		if err != nil {
			fmt.Printf("%-11s %s\n", p.Name(), format.Red(err.Error()))
			allOK = false
			continue
		}
		if !ok {
			allOK = false
		}
	}
	if !allOK {
		return plugin.Exit(1)
	}
	return nil
}

func statusOne(ctx context.Context, owner plugin.Plugin, verbose bool) (bool, error) {
	if err := plugin.Validate(owner); err != nil {
		return false, err
	}
	p := plugin.ProfileFor(owner)
	label := fmt.Sprintf("%-11s", owner.Name())

	if !session.Exists(p.Name) {
		fmt.Printf("%s %s %s\n", label, format.Yellow("not logged in"),
			format.Gray(fmt.Sprintf("(profile %s) — run: sessh auth login %s", p.Name, owner.Name())))
		return false, nil
	}

	h, err := plugin.Acquire(ctx, owner, session.AcquireOptions{})
	if err != nil {
		if session.IsAuth(err) {
			fmt.Printf("%s %s %s\n", label, format.Yellow("session expired"),
				format.Gray(fmt.Sprintf("(profile %s) — run: sessh auth login %s", p.Name, owner.Name())))
			return false, nil
		}
		return false, err
	}
	defer h.Close()

	id, _ := h.Session.Whoami(ctx)
	name := id.Name()
	line := fmt.Sprintf("%s %s", label, format.Green("✓ logged in"))
	if name != "" {
		line += " as " + format.Bold(name)
	}
	line += format.Gray(fmt.Sprintf("  (profile %s", p.Name))
	if h.Routed {
		line += format.Gray(", via a running sessh process")
	}
	line += format.Gray(")")
	fmt.Println(line)
	if verbose {
		fmt.Println(format.Gray("  Origin:  " + p.Origin))
		fmt.Println(format.Gray("  Profile: " + session.ProfileDir(p.Name)))
	}
	return true, nil
}

func listProfiles() error {
	metas, err := session.ListProfiles()
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		fmt.Println("No profiles yet. Run: sessh auth login")
		return nil
	}

	// Which plugins point at each profile right now.
	users := map[string][]string{}
	for _, p := range plugin.All() {
		n := plugin.ProfileFor(p).Name
		users[n] = append(users[n], p.Name())
	}

	rows := make([][]string, 0, len(metas))
	for _, m := range metas {
		verified := ""
		if !m.LastVerified.IsZero() {
			verified = format.RelativeTo(m.LastVerified, time.Now())
		}
		// A profile that is currently held shows who has it, so "busy" is
		// something you can look up rather than only discover by colliding.
		held := ""
		if holder := session.Holder(m.Name); holder != "" {
			held = format.Yellow(holder)
		}
		rows = append(rows, []string{
			format.Green(m.Name),
			format.Gray(shortParent(m.Parent)),
			strings.Join(users[m.Name], ", "),
			format.Gray(strings.Join(m.Origins, " ")),
			format.Gray(m.LastIdentity),
			format.Gray(verified),
			held,
		})
	}
	fmt.Println(format.Table(rows,
		[]string{"profile", "seeded from", "plugins", "origins", "identity", "verified", "in use by"}))
	fmt.Println(format.Gray("\n" + format.Count(len(metas), "profile", "profiles") + " under " + session.Root()))
	return nil
}

// shortParent keeps the inheritance column readable: a parent profile shows as
// its name, an imported directory as its last two path segments.
func shortParent(p string) string {
	if p == "" || !strings.Contains(p, string(filepath.Separator)) {
		return p
	}
	dir, base := filepath.Split(strings.TrimRight(p, string(filepath.Separator)))
	return filepath.Join(filepath.Base(strings.TrimRight(dir, string(filepath.Separator))), base)
}
