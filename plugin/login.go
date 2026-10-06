package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/internal/background"
	"github.com/stefanm8/sessh/session"
)

func cobraErr() io.Writer { return os.Stderr }

// LoginOptions configures the interactive browser login flow.
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
	return login(ctx, owner, opts, loginNeed{})
}

// loginNeed describes a login that a command, rather than `sessh auth login`,
// asked for.
type loginNeed struct {
	command bool   // a command runs again once the user is signed in
	probe   string // a request that failed with 401; it must pass as well
}

// relogin is WithSession's recovery: cause is the authentication failure the
// command hit. Output goes to stderr, leaving stdout to the command's data.
func relogin(ctx context.Context, owner Plugin, cause error) error {
	need := loginNeed{command: true}
	var ae *session.AuthError
	if errors.As(cause, &ae) && ae.Status != http.StatusForbidden {
		need.probe = ae.URL
	}
	if globalFlags.Verbose {
		fmt.Fprintln(cobraErr(), format.Gray("sessh: "+cause.Error()))
	}
	return login(ctx, owner, LoginOptions{Out: cobraErr()}, need)
}

func login(ctx context.Context, owner Plugin, opts LoginOptions, need loginNeed) error {
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
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ended := func(err error) error {
		switch {
		case parent.Err() != nil:
			return fmt.Errorf("login cancelled")
		case ctx.Err() != nil:
			return fmt.Errorf("timed out waiting for login")
		}
		return err
	}

	p := ProfileFor(owner)

	// One login window per profile. A command that had to wait for another
	// process's login first checks whether that one already signed it in.
	release, waited, err := session.LockLogin(ctx, p.Name, func() {
		fmt.Fprintln(out, format.Gray("Waiting for the sign-in already in progress for profile "+p.Name+"..."))
	})
	if err != nil {
		return ended(err)
	}
	defer release()
	if waited && need.command && signedIn(ctx, owner, need.probe) {
		return nil
	}

	meta, _ := session.ReadMeta(p.Name)
	h, resume, err := acquireHeaded(ctx, p, out)
	defer resume() // after the window closes: the worker needs the profile
	if err != nil {
		return ended(err)
	}
	defer h.Close()

	shared := SharingProfile(p.Name, owner.Name())
	if meta == nil || meta.LastVerified.IsZero() {
		fmt.Fprintf(out, "%s\n", format.Bold("First run: opening a browser so you can log in."))
	} else {
		fmt.Fprintf(out, "%s\n", format.Bold("Opening a browser to refresh the session."))
	}
	fmt.Fprintf(out, "%s\n", format.Gray(fmt.Sprintf("Profile: %s  •  Origin: %s", p.Name, p.Origin)))
	if len(shared) > 0 {
		fmt.Fprintf(out, "%s\n", format.Gray("This also signs in: "+joinNames(shared)))
	}

	if p.LoginURL != "" {
		if _, err := h.Session.Navigate(ctx, p.LoginURL); err != nil {
			return ended(err)
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Log in in the browser window that just opened.")
	if need.command {
		fmt.Fprintln(out, "  The command continues once you are signed in.")
	}
	fmt.Fprintln(out, "  Waiting for authentication (Ctrl+C to cancel)...")
	fmt.Fprintln(out)

	if p.Verify == nil {
		fmt.Fprintf(out, "%s Session saved for profile %s\n", format.Green("✓"), format.Bold(p.Name))
		return nil
	}
	// Verify may navigate to mint a session; here that would reload the page
	// the user is signing in on, so it only observes.
	observed := session.Observe(h.Session)
	var verifiedAt time.Time
	hinted := false
	for {
		if p.Verify(ctx, observed) == nil {
			if probeOK(ctx, h.Session, need.probe) {
				id, _ := h.Session.Whoami(ctx)
				session.TouchVerified(p.Name, p.Origin, id)
				name := id.Name()
				if name == "" {
					name = p.Origin
				}
				fmt.Fprintf(out, "%s Logged in as %s\n", format.Green("✓"), format.Bold(name))
				return nil
			}
			if verifiedAt.IsZero() {
				verifiedAt = time.Now()
			}
			if !hinted && time.Since(verifiedAt) > 10*time.Second {
				fmt.Fprintln(out, format.Yellow("  The browser looks signed in, but "+need.probe+" is still refused. Still waiting..."))
				hinted = true
			}
		}
		select {
		case <-ctx.Done():
			return ended(ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// acquireHeaded opens the profile in a visible window. A background worker
// holding it (a daemon or capture started with `start`) is paused, and resume
// starts it again; a command still using the profile gets a moment to finish.
// Anything else holding it is reported.
func acquireHeaded(ctx context.Context, p session.Profile, out io.Writer) (_ *session.Handle, resume func(), _ error) {
	resume = func() {}
	opts := session.AcquireOptions{
		Headed:     true,
		SkipVerify: true,
		// Commands starting meanwhile wait for this login instead of sharing
		// a session that is not signed in yet.
		NoServe: true,
		Seed:    session.SeedOptions{From: globalFlags.From, NoInherit: globalFlags.NoInherit},
		Notify:  func(msg string) { fmt.Fprintln(out, format.Gray("sessh: "+msg)) },
	}
	grace := time.Now().Add(10 * time.Second)
	for waiting, paused := false, false; ; {
		h, err := session.Acquire(ctx, p, opts)
		var busy *session.ErrBusy
		if err == nil || !errors.As(err, &busy) {
			return h, resume, err
		}
		if spec, st, ok := background.Find(ctx, session.RunDir(), busy.PID); ok && st.Detached && !paused {
			if resume, err = pause(ctx, spec, st, p.Name, out); err != nil {
				return nil, resume, err
			}
			paused = true
			continue
		}
		if time.Now().After(grace) {
			return nil, resume, err
		}
		if !waiting {
			fmt.Fprintln(out, format.Gray(fmt.Sprintf("Profile %s is in use (%s); waiting for it to be released...", p.Name, busy.Holder)))
			waiting = true
		}
		select {
		case <-ctx.Done():
			return nil, resume, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// pause stops a background worker for the length of a login. The returned
// function starts it again with its original arguments and waits until it
// holds the profile, so the command being retried shares its session instead
// of racing it for the profile.
func pause(ctx context.Context, spec background.Spec, st background.Status, profile string, out io.Writer) (func(), error) {
	fmt.Fprintln(out, format.Gray(fmt.Sprintf("Pausing %s while you log in; it restarts afterwards.", st.Name)))
	if _, err := spec.Stop(ctx); err != nil {
		return func() {}, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		restarted, _, err := spec.Start(ctx, st.Args)
		if err != nil {
			fmt.Fprintln(out, format.Yellow(fmt.Sprintf("Could not restart %s: %v", st.Name, err)))
			return
		}
		for session.HolderPID(profile) != restarted.PID && ctx.Err() == nil {
			if cur, live, _ := spec.Inspect(ctx); !live || cur.State == "failed" {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Fprintln(out, format.Gray("Restarted "+st.Name+"."))
	}, nil
}

// signedIn checks without a window whether the profile works now.
func signedIn(ctx context.Context, owner Plugin, probe string) bool {
	// The caller owns the login lock. Bypass automatic startup, whose normal
	// callers must wait until that lock is released.
	h, err := session.Acquire(ctx, ProfileFor(owner), session.AcquireOptions{
		Seed: session.SeedOptions{From: globalFlags.From, NoInherit: globalFlags.NoInherit},
	})
	if err != nil {
		return false
	}
	defer h.Close()
	return probeOK(ctx, h.Session, probe)
}

// probeOK reports whether a request that failed with 401 gets through now. A
// lenient Verify can pass on a cookie the service no longer accepts. A 403 is
// a question of permission, not of login.
func probeOK(ctx context.Context, s session.Session, url string) bool {
	if url == "" {
		return true
	}
	req, err := session.Request(ctx, http.MethodGet, url, nil)
	if err != nil {
		return true
	}
	resp, err := s.Do(ctx, req)
	if err == nil {
		resp.Body.Close()
		return true
	}
	var ae *session.AuthError
	return errors.As(err, &ae) && ae.Status == http.StatusForbidden
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
