package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// The manager keeps at most one Chromium persistent context per profile name
// and decides, per command, whether this process owns the profile or routes
// through the process that does.

// AcquireOptions tunes one acquisition.
type AcquireOptions struct {
	// Headed forces a visible window. Only `sessh auth login` sets this, and it
	// requires ownership of the profile — a headless holder cannot grow a
	// window.
	Headed bool
	// SkipVerify runs the profile's Verify hook off. Used by `auth logout` and
	// by login itself, which polls Verify on its own schedule.
	SkipVerify bool
	// Seed controls inheritance when the profile does not exist yet.
	Seed SeedOptions
	// Serve is implied: whoever holds the lock serves the profile on its socket
	// for as long as the session lives, which is what lets commands sharing a
	// profile run concurrently instead of colliding. The field is
	// kept so long-running commands can say so explicitly.
	Serve bool
	// NoServe opts out, for the rare case where a session must not be shared.
	NoServe bool
	// Notify receives human-readable notes (profile seeded, routed through a
	// daemon). May be nil.
	Notify func(string)
}

// Handle is a live session plus the cleanup that must run when the caller is
// done with it.
type Handle struct {
	Session Session
	Profile Profile
	// Routed reports that this session is served by another process.
	Routed bool

	server  *Server
	closers []func()
	once    sync.Once
}

func (h *Handle) Close() {
	h.once.Do(func() {
		for i := len(h.closers) - 1; i >= 0; i-- {
			h.closers[i]()
		}
	})
}

// ErrUnavailable means there is no reachable shared session for a profile.
// Connect never launches Chromium or creates a profile when this is returned.
var ErrUnavailable = errors.New("no shared session is available")

// Connect joins an existing headless session without acquiring its lock or
// preparing its profile. Authentication and cancellation errors are returned
// unchanged, so callers must not mistake them for a missing daemon.
func Connect(ctx context.Context, p Profile, opts AcquireOptions) (*Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Headed {
		return nil, fmt.Errorf("a headed session cannot connect to a shared browser")
	}
	if p.Name == "" {
		p.Name = DefaultProfile
	}
	cl, err := dialProfileContext(ctx, p.Name)
	if ctxErr := acquireContextError(ctx); ctxErr != nil {
		if cl != nil {
			_ = cl.Close()
		}
		return nil, ctxErr
	}
	if err != nil {
		return nil, fmt.Errorf("%w for profile %q: %v", ErrUnavailable, p.Name, err)
	}
	return routedHandle(ctx, p, opts, cl)
}

// A socket deadline can wake just before the context timer publishes Err.
// Preserve the caller's deadline instead of mistaking this for a missing
// daemon and starting another browser.
func acquireContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func routedHandle(ctx context.Context, p Profile, opts AcquireOptions, cl *rpcClient) (*Handle, error) {
	cl.profile = p
	h := &Handle{Session: cl, Profile: p, Routed: true, closers: []func(){func() { _ = cl.Close() }}}
	if opts.Notify != nil {
		opts.Notify(fmt.Sprintf("routed through the process holding profile %q", p.Name))
	}
	if !opts.SkipVerify {
		if err := verify(ctx, p, cl); err != nil {
			h.Close()
			return nil, err
		}
	}
	return h, nil
}

// Acquire resolves a profile to a usable Session.
//
//   - lock acquired → launch the browser in this process (and, if asked, serve
//     it on the profile's socket so concurrent commands can share it);
//   - lock held → connect to the holder's socket and hand back a Session whose
//     calls are JSON-RPC'd to it. Identical interface; the plugin cannot tell.
func Acquire(ctx context.Context, p Profile, opts AcquireOptions) (*Handle, error) {
	if p.Name == "" {
		p.Name = DefaultProfile
	}
	notify := opts.Notify
	if notify == nil {
		notify = func(string) {}
	}

	if note, err := prepareProfile(p.Name, opts.Seed); err != nil {
		return nil, err
	} else if note != "" {
		notify(note)
	}

	lock, cl, err := acquireProfile(ctx, p.Name, opts.Headed)
	if err != nil {
		return nil, err
	}
	if cl != nil {
		return routedHandle(ctx, p, opts, cl)
	}

	s, closer, err := launch(p, launchOptions{Headed: opts.Headed})
	if err != nil {
		lock.Release()
		return nil, err
	}
	h := &Handle{Session: s, Profile: p, closers: []func(){lock.Release, closer}}

	if !opts.NoServe {
		if srv, err := serve(s, p.Name); err == nil {
			h.server = srv
			h.closers = append(h.closers, srv.Close)
		} else if opts.Serve {
			// Only worth reporting when the caller asked for sharing on
			// purpose; for an ordinary command it is a missed optimisation.
			h.Close()
			return nil, fmt.Errorf("could not open the session socket for %q: %w", p.Name, err)
		}
	}

	if !opts.SkipVerify {
		if err := verify(ctx, p, s); err != nil {
			h.Close()
			return nil, err
		}
	}
	return h, nil
}

// acquireProfile either connects to a warm session or owns the profile lock.
// A busy holder can start serving OR finish during the wait, so retry both
// the socket and the lock. Polling only the socket misses short-lived commands.
func acquireProfile(ctx context.Context, name string, headed bool) (*profileLock, *rpcClient, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !headed {
			if cl, err := dialProfileContext(ctx, name); err == nil {
				return nil, cl, nil
			}
			if err := acquireContextError(ctx); err != nil {
				return nil, nil, err
			}
		}
		lock, err := acquireLock(name)
		if err == nil {
			return lock, nil, nil
		}
		var busy *ErrBusy
		if !errors.As(err, &busy) {
			return nil, nil, err
		}
		if headed {
			return nil, nil, fmt.Errorf(
				"%w.\nA headed login needs the profile to itself — Chromium cannot open a window in an\n"+
					"already-running headless context. Stop the holder and retry, or use --profile <name>", err)
		}
		if attempt == 20 {
			return nil, nil, fmt.Errorf("%w.\nIt is not serving a session socket, so this command cannot share it", err)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func verify(ctx context.Context, p Profile, s Session) error {
	if p.Verify == nil {
		return nil
	}
	if err := p.Verify(ctx, s); err != nil {
		if IsAuth(err) {
			return err
		}
		return err
	}
	id, _ := s.Whoami(ctx)
	TouchVerified(p.Name, p.Origin, id)
	return nil
}

// Forget deletes a profile directory — `sessh auth logout`.
func Forget(name string) error {
	if name == "" {
		return fmt.Errorf("no profile named")
	}
	if cl, err := dialProfile(name); err == nil {
		_ = cl.Close()
		return fmt.Errorf("profile %q is in use by a running sessh process; stop it first", name)
	}
	lock, err := acquireLock(name)
	if err != nil {
		return err
	}
	defer lock.Release()
	return os.RemoveAll(ProfileDir(name))
}

// IdleFor measures inactivity of the shared browser, including in-flight RPCs.
func (h *Handle) IdleFor() time.Duration {
	if h.server == nil {
		return 0
	}
	return h.server.IdleFor()
}
