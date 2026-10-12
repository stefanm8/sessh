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
	"sync/atomic"

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
	Profile     string // --profile: override the profile directory
	From        string // --from: seed a new profile from this one instead of `default`
	NoInherit   bool   // --no-inherit: start a new profile empty
	NoLogin     bool   // --no-login: report a lapsed session instead of opening a login window
	Verbose     bool   // --verbose: print session bookkeeping to stderr
	NoKeepAlive bool   // --no-keep-alive: do not automatically start a warm browser
	KeepAlive   string // --keep-alive: idle duration, or off; empty uses env/default
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
	return withSession(owner, fn, false)
}

// WithMutationSession authenticates like WithSession, but never replays a
// command after it has attempted an HTTP write. A failed response can leave
// the server's outcome unknown; retrying the command could duplicate work.
// Use this for command bodies that call Session.Do with POST, PUT, or DELETE.
func WithMutationSession(owner Plugin, fn Runner) func(*cobra.Command, []string) error {
	return withSession(owner, fn, true)
}

func withSession(owner Plugin, fn Runner, mutation bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runSession(ctx, owner, func(ctx context.Context, s session.Session) error {
			if mutation {
				guard := &mutationSession{Session: s}
				err := fn(ctx, guard, cmd, args)
				if err != nil && guard.attempted.Load() {
					return &mutationError{cause: err}
				}
				return err
			}
			return fn(ctx, s, cmd, args)
		})
	}
}

type mutationSession struct {
	session.Session
	attempted atomic.Bool
}

func (s *mutationSession) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		s.attempted.Store(true)
	}
	return s.Session.Do(ctx, req)
}

type mutationError struct{ cause error }

func (e *mutationError) Error() string {
	return "write command was not retried; check the issue before running it again: " + e.cause.Error()
}

func (e *mutationError) Unwrap() error { return e.cause }

func attemptedMutation(err error) bool {
	var mutation *mutationError
	return errors.As(err, &mutation)
}

// Seams for tests: the real ones launch browsers.
var (
	acquireFn = Acquire
	reloginFn = relogin
)

// runSession runs fn, recovering at most once from a lapsed session: by a
// login of its own, or by waiting out one another process has open.
func runSession(ctx context.Context, owner Plugin, fn func(context.Context, session.Session) error) error {
	recovered := false
	for {
		err := attempt(ctx, owner, fn)
		if err == nil || attemptedMutation(err) || !interactive() {
			return err
		}
		// Another caller can briefly own the login lock even after our own
		// login, while checking whether it still needs a window. Waiting for
		// that lock does not spend the one authentication recovery attempt.
		if waitForLogin(ctx, err) {
			continue
		}
		if !recovered && session.IsAuth(err) {
			if err := reloginFn(ctx, owner, err); err != nil {
				return err
			}
			recovered = true
			continue
		}
		return err
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
	err = fn(ctx, h.Session)
	var mutation *mutationError
	if errors.As(err, &mutation) {
		return &mutationError{cause: forbidden(ctx, h, mutation.cause)}
	}
	return forbidden(ctx, h, err)
}

// forbidden tells "signed out" from "not allowed": a 403 from a session that
// still passes Verify is a permission problem, which a login would not fix.
func forbidden(ctx context.Context, h *session.Handle, err error) error {
	var ae *session.AuthError
	if !errors.As(err, &ae) || ae.Status != http.StatusForbidden {
		return err
	}
	if verificationErr := session.Verify(ctx, h.Profile, h.Session); verificationErr != nil {
		if session.IsAuth(verificationErr) {
			return err
		}
		return fmt.Errorf("could not check the session after HTTP 403: %w", verificationErr)
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
	var known *loginBusyError
	if !errors.As(err, &busy) || (!errors.As(err, &known) && !session.LoggingIn(busy.Profile)) {
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

// Remember that the login lock caused acquisition to stop. It may already
// be released by the time runSession receives this error; retrying is still
// necessary, whereas an ordinary foreground profile holder must stay busy.
type loginBusyError struct{ busy *session.ErrBusy }

func (e *loginBusyError) Error() string { return e.busy.Error() }
func (e *loginBusyError) Unwrap() error { return e.busy }

func loginBusy(profile string) error {
	return &loginBusyError{busy: &session.ErrBusy{Profile: profile, Holder: "sign-in or logout in progress"}}
}

// Acquire resolves a plugin's profile through the shared flags and hands back
// a live session handle. Long-running commands (which want to serve a socket)
// call this directly rather than through WithSession. It never opens a window.
func Acquire(ctx context.Context, owner Plugin, opts session.AcquireOptions) (*session.Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := Validate(owner); err != nil {
		return nil, err
	}
	opts.Seed.From = globalFlags.From
	opts.Seed.NoInherit = globalFlags.NoInherit
	if opts.Notify == nil && globalFlags.Verbose {
		opts.Notify = func(msg string) { fmt.Fprintln(cobraErr(), "sessh:", msg) }
	}
	p := ProfileFor(owner)
	// Long-running owners and interactive windows must acquire directly. In
	// particular, the daemon started below must never start another daemon.
	if opts.Serve || opts.Headed || opts.NoServe {
		return session.Acquire(ctx, p, opts)
	}
	if session.LoggingIn(p.Name) {
		return nil, loginBusy(p.Name)
	}
	idle, enabled, err := keepAlivePolicy()
	if err != nil {
		return nil, err
	}
	if !enabled {
		h, err := session.Acquire(ctx, p, opts)
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil && session.LoggingIn(p.Name) {
			return nil, loginBusy(p.Name)
		}
		return h, err
	}
	return acquireWarm(ctx, owner, p, opts, idle)
}

// ProfileFor resolves the profile a plugin would use right now.
func ProfileFor(owner Plugin) session.Profile {
	p := owner.Profile()
	p.Name = session.ResolveProfileName(owner.Name(), globalFlags.Profile, p.Name)
	return p
}
