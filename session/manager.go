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

	// Someone may already be holding this profile with a socket up. Prefer it:
	// it is warm, and it is the only way to share a single-process profile.
	if !opts.Headed {
		if cl, err := dialProfile(p.Name); err == nil {
			cl.profile = p
			h := &Handle{Session: cl, Profile: p, Routed: true, closers: []func(){func() { _ = cl.Close() }}}
			notify(fmt.Sprintf("routed through the process holding profile %q", p.Name))
			if !opts.SkipVerify {
				if err := verify(ctx, p, cl); err != nil {
					h.Close()
					return nil, err
				}
			}
			return h, nil
		}
	}

	lock, err := acquireLock(p.Name)
	if err != nil {
		var busy *ErrBusy
		if errors.As(err, &busy) {
			if opts.Headed {
				return nil, fmt.Errorf(
					"%w.\nA headed login needs the profile to itself — Chromium cannot open a window in an\n"+
						"already-running headless context. Stop the holder and retry, or use --profile <name>", err)
			}
			// The holder may still be starting its socket; give it a moment.
			for i := 0; i < 20; i++ {
				time.Sleep(150 * time.Millisecond)
				if cl, derr := dialProfile(p.Name); derr == nil {
					cl.profile = p
					h := &Handle{Session: cl, Profile: p, Routed: true, closers: []func(){func() { _ = cl.Close() }}}
					if !opts.SkipVerify {
						if verr := verify(ctx, p, cl); verr != nil {
							h.Close()
							return nil, verr
						}
					}
					return h, nil
				}
			}
			return nil, fmt.Errorf("%w.\nIt is not serving a session socket, so this command cannot share it", err)
		}
		return nil, err
	}

	s, closer, err := launch(p, launchOptions{Headed: opts.Headed})
	if err != nil {
		lock.Release()
		return nil, err
	}
	h := &Handle{Session: s, Profile: p, closers: []func(){closer, lock.Release}}

	if !opts.NoServe {
		if srv, err := serve(s, p.Name); err == nil {
			h.closers = append([]func(){srv.Close}, h.closers...)
		} else if opts.Serve {
			// Only worth reporting when the caller asked for sharing on
			// purpose; for an ordinary command it is a missed optimisation.
			notify(fmt.Sprintf("could not open the session socket for %q: %v", p.Name, err))
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
	if _, err := dialProfile(name); err == nil {
		return fmt.Errorf("profile %q is in use by a running sessh process; stop it first", name)
	}
	lock, err := acquireLock(name)
	if err != nil {
		return err
	}
	lock.Release()
	return os.RemoveAll(ProfileDir(name))
}
