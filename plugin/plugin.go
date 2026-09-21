// Package plugin is the seam between the browser-session hub and the tools
// built on it. Registration is compile-time: each plugin package calls Register
// from init() and cmd/sessh blank-imports it. One binary, one
// build, static typing across the seam — no .so files, no subprocess protocol.
package plugin

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/spf13/cobra"

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
	Login     bool   // --login: open a headed window if the session has lapsed
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
func WithSession(owner Plugin, fn Runner) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		h, err := Acquire(ctx, owner, session.AcquireOptions{})
		if err != nil {
			return err
		}
		defer h.Close()
		return fn(ctx, h.Session, cmd, args)
	}
}

// Acquire resolves a plugin's profile through the shared flags and hands back
// a live session handle. Long-running commands (which want to serve a socket)
// call this directly rather than through WithSession.
func Acquire(ctx context.Context, owner Plugin, opts session.AcquireOptions) (*session.Handle, error) {
	if err := Validate(owner); err != nil {
		return nil, err
	}
	p := owner.Profile()
	p.Name = session.ResolveProfileName(owner.Name(), globalFlags.Profile, p.Name)
	opts.Seed.From = globalFlags.From
	opts.Seed.NoInherit = globalFlags.NoInherit
	if opts.Notify == nil && globalFlags.Verbose {
		opts.Notify = func(msg string) { fmt.Fprintln(cobraErr(), "sessh:", msg) }
	}

	h, err := session.Acquire(ctx, p, opts)
	if err == nil || !session.IsAuth(err) || !globalFlags.Login {
		return h, err
	}
	// --login: the user asked for a headed re-auth in place rather than the
	// usual "run: sessh auth login".
	if err := Login(ctx, owner, LoginOptions{}); err != nil {
		return nil, err
	}
	return session.Acquire(ctx, p, opts)
}

// ProfileFor resolves the profile a plugin would use right now.
func ProfileFor(owner Plugin) session.Profile {
	p := owner.Profile()
	p.Name = session.ResolveProfileName(owner.Name(), globalFlags.Profile, p.Name)
	return p
}
