// Package plugin is the seam between the browser-session hub and the tools
// built on it. Registration is compile-time: each plugin package calls Register
// from init() and cmd/sessh blank-imports it. One binary, one
// build, static typing across the seam — no .so files, no subprocess protocol.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"sync"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/format"
	"github.com/stefanm8/sessh/session"
)

// Plugin is the whole contract. Three methods.
type Plugin interface {
	Name() string             // "bitbucket" -> `sessh bitbucket ...`
	Profile() session.Profile // which browser profile/origin it needs
	Command() *cobra.Command  // its command tree
}

// Validate checks optional plugin configuration before opening a browser.
func Validate(p Plugin) error {
	if v, ok := p.(interface{ Validate() error }); ok {
		return v.Validate()
	}
	return nil
}

var (
	mu         sync.Mutex
	registered = map[string]Plugin{}
	order      []string
)

// Register adds a plugin. Called from init(); panics on a duplicate name,
// because that is a build-time mistake, not a runtime condition.
func Register(p Plugin) {
	mu.Lock()
	defer mu.Unlock()
	name := p.Name()
	if _, dup := registered[name]; dup {
		panic(fmt.Sprintf("plugin %q registered twice", name))
	}
	registered[name] = p
	order = append(order, name)
}

// All returns every registered plugin, sorted by name.
func All() []Plugin {
	mu.Lock()
	defer mu.Unlock()
	names := append([]string(nil), order...)
	sort.Strings(names)
	out := make([]Plugin, 0, len(names))
	for _, n := range names {
		out = append(out, registered[n])
	}
	return out
}

// Lookup finds a plugin by name.
func Lookup(name string) (Plugin, bool) {
	mu.Lock()
	defer mu.Unlock()
	p, ok := registered[name]
	return p, ok
}

// ---------------------------------------------------------------------------
// Session injection
// ---------------------------------------------------------------------------

// Global flags that every session-backed command shares. cmd/sessh binds these
// to the root command's persistent flags.
type Flags struct {
	Profile   string // --profile: override the profile directory
	From      string // --from: seed a new profile from this one instead of `default`
	NoInherit bool   // --no-inherit: start a new profile empty
	NoLogin   bool   // --no-login: report a lapsed session instead of opening a login window
	Verbose   bool   // --verbose: print session bookkeeping to stderr
}

var globalFlags = &Flags{}

// BindFlags lets cmd/sessh hand the shared flag block to the injection point.
func BindFlags(f *Flags) { globalFlags = f }

// GlobalFlags exposes the shared flags to commands that need them directly.
func GlobalFlags() *Flags { return globalFlags }

// Runner is what a session-backed command body looks like.
type Runner func(ctx context.Context, s session.Session, cmd *cobra.Command, args []string) error

// WithSession is the injection point, and the only thing a plugin author must
// learn. It returns a cobra RunE: when the command runs, it resolves the owning
// plugin's profile, acquires a session, and passes it in. Commands that need no
// browser use a plain RunE and none is ever launched.
//
// A session that turns out to be signed out, whether while it is acquired or
// in a request the command makes, opens a login window. The command waits for
// the user to finish and then runs once more. --no-login reports it instead.
func WithSession(owner Plugin, fn Runner) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runSession(ctx, owner, func(ctx context.Context, s session.Session) error {
			return fn(ctx, s, cmd, args)
		})
	}
}

// Seams for tests: the real ones launch browsers.
var (
	acquireFn = Acquire
	reloginFn = relogin
)

// runSession runs fn, recovering at most once from a lapsed session: by a
// login of its own, or by waiting out one another process has open.
func runSession(ctx context.Context, owner Plugin, fn func(context.Context, session.Session) error) error {
	for recovered := false; ; recovered = true {
		err := attempt(ctx, owner, fn)
		if err == nil || recovered || !interactive() {
			return err
		}
		switch {
		case waitForLogin(ctx, err):
		case session.IsAuth(err):
			if err := reloginFn(ctx, owner, err); err != nil {
				return err
			}
		default:
			return err
		}
	}
}

// attempt runs fn with a fresh session. The session is closed before any
// login, which needs the profile to itself.
func attempt(ctx context.Context, owner Plugin, fn func(context.Context, session.Session) error) error {
	h, err := acquireFn(ctx, owner, session.AcquireOptions{})
	if err != nil {
		return err
	}
	defer h.Close()
	return forbidden(ctx, h, fn(ctx, h.Session))
}

// forbidden tells "signed out" from "not allowed": a 403 from a session that
// still passes Verify is a permission problem, which a login would not fix.
func forbidden(ctx context.Context, h *session.Handle, err error) error {
	var ae *session.AuthError
	if !errors.As(err, &ae) || ae.Status != http.StatusForbidden {
		return err
	}
	if h.Profile.Verify != nil && h.Profile.Verify(ctx, h.Session) != nil {
		return err
	}
	where := ae.URL
	if u, perr := url.Parse(ae.URL); perr == nil {
		u.RawQuery = ""
		where = u.String()
	}
	return fmt.Errorf("access denied (HTTP 403) for %s: profile %s is signed in, so the account may lack permission", where, h.Profile.Name)
}

// interactive reports whether a lapsed session may open a login window: not
// with --no-login, and not where no window can open.
func interactive() bool {
	if globalFlags.NoLogin {
		return false
	}
	return runtime.GOOS != "linux" || os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// waitForLogin waits out the one kind of busy profile that resolves itself: a
// login window open in another process, which does not share its session.
// That login decides for this command too.
func waitForLogin(ctx context.Context, err error) bool {
	var busy *session.ErrBusy
	if !errors.As(err, &busy) || !session.LoggingIn(busy.Profile) {
		return false
	}
	fmt.Fprintln(cobraErr(), format.Gray("Waiting for the sign-in already in progress for profile "+busy.Profile+"..."))
	release, _, err := session.LockLogin(ctx, busy.Profile, nil)
	if err != nil {
		return false
	}
	release()
	return true
}

// Acquire resolves a plugin's profile through the shared flags and hands back
// a live session handle. Long-running commands (which want to serve a socket)
// call this directly rather than through WithSession. It never opens a window.
func Acquire(ctx context.Context, owner Plugin, opts session.AcquireOptions) (*session.Handle, error) {
	if err := Validate(owner); err != nil {
		return nil, err
	}
	opts.Seed.From = globalFlags.From
	opts.Seed.NoInherit = globalFlags.NoInherit
	if opts.Notify == nil && globalFlags.Verbose {
		opts.Notify = func(msg string) { fmt.Fprintln(cobraErr(), "sessh:", msg) }
	}
	return session.Acquire(ctx, ProfileFor(owner), opts)
}

// ProfileFor resolves the profile a plugin would use right now.
func ProfileFor(owner Plugin) session.Profile {
	p := owner.Profile()
	p.Name = session.ResolveProfileName(owner.Name(), globalFlags.Profile, p.Name)
	return p
}
