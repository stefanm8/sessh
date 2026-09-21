package plugin

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/session"
)

func cobraErr() io.Writer { return os.Stderr }

// LoginOptions tunes the one headed code path in the binary.
type LoginOptions struct {
	Timeout time.Duration
	Out     io.Writer
}

// Login opens the plugin's profile headed at its LoginURL and waits for
// Profile.Verify to pass. We never script the login form: it is
// SSO/2FA and changes under us. The human logs in; we only persist the result.
//
// Because login is per *profile*, this also signs in every other plugin that
// shares the same profile name — which is the whole "log in once" property.
func Login(ctx context.Context, owner Plugin, opts LoginOptions) error {
	if err := Validate(owner); err != nil {
		return err
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	p := ProfileFor(owner)
	fresh := !session.Exists(p.Name)

	shared := SharingProfile(p.Name, owner.Name())
	if fresh {
		fmt.Fprintf(out, "%s\n", format.Bold("First run: opening a browser so you can log in."))
	} else {
		fmt.Fprintf(out, "%s\n", format.Bold("Opening a browser to refresh the session."))
	}
	fmt.Fprintf(out, "%s\n", format.Gray(fmt.Sprintf("Profile: %s  •  Origin: %s", p.Name, p.Origin)))
	if len(shared) > 0 {
		fmt.Fprintf(out, "%s\n", format.Gray("This also signs in: "+joinNames(shared)))
	}

	h, err := session.Acquire(ctx, p, session.AcquireOptions{
		Headed:     true,
		SkipVerify: true,
		Seed:       session.SeedOptions{From: globalFlags.From, NoInherit: globalFlags.NoInherit},
		Notify:     func(msg string) { fmt.Fprintln(out, format.Gray("sessh: "+msg)) },
	})
	if err != nil {
		return err
	}
	defer h.Close()

	if p.LoginURL != "" {
		if _, err := h.Session.Navigate(ctx, p.LoginURL); err != nil {
			return err
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Log in in the browser window that just opened.")
	fmt.Fprintln(out, "  Waiting for authentication (Ctrl+C to cancel)...")
	fmt.Fprintln(out)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.Verify == nil {
			break
		}
		if err := p.Verify(ctx, h.Session); err == nil {
			id, _ := h.Session.Whoami(ctx)
			session.TouchVerified(p.Name, p.Origin, id)
			name := id.Name()
			if name == "" {
				name = p.Origin
			}
			fmt.Fprintf(out, "%s Logged in as %s\n", format.Green("✓"), format.Bold(name))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if p.Verify == nil {
		fmt.Fprintf(out, "%s Session saved for profile %s\n", format.Green("✓"), format.Bold(p.Name))
		return nil
	}
	return fmt.Errorf("timed out waiting for login")
}

// SharingProfile lists the other plugins that resolve to the same profile
// name, so login can say what else it just signed in.
func SharingProfile(profileName, except string) []string {
	var out []string
	for _, other := range All() {
		if other.Name() == except {
			continue
		}
		if ProfileFor(other).Name == profileName {
			out = append(out, other.Name())
		}
	}
	return out
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	s := ""
	for i, n := range names {
		switch {
		case i == 0:
			s = n
		case i == len(names)-1:
			s += " and " + n
		default:
			s += ", " + n
		}
	}
	return s
}
