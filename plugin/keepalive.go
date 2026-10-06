package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/stefanm8/sessh/internal/background"
	"github.com/stefanm8/sessh/session"
)

const defaultKeepAlive = 30 * time.Minute

// An explicit flag wins over the environment. Disabling automatic startup
// still permits sharing a browser another command already owns.
func keepAlivePolicy() (time.Duration, bool, error) {
	if globalFlags.NoKeepAlive {
		return 0, false, nil
	}
	value := globalFlags.KeepAlive
	if value == "" {
		value = os.Getenv("SESSH_KEEP_ALIVE")
	}
	if value == "" {
		return defaultKeepAlive, true, nil
	}
	if strings.EqualFold(value, "off") {
		return 0, false, nil
	}
	idle, err := time.ParseDuration(value)
	if err != nil || idle < 0 {
		return 0, false, fmt.Errorf("invalid keep-alive %q: use a non-negative duration such as 30m, 0 for indefinite, or off (set --keep-alive or SESSH_KEEP_ALIVE)", value)
	}
	return idle, true, nil
}

// warmArgs pins the resolved profile and the first caller's inheritance
// options. Never replay the original command: it might perform a write.
func warmArgs(owner Plugin, p session.Profile, opts session.AcquireOptions, idle time.Duration) []string {
	args := []string{"daemon", owner.Name(), "--profile=" + p.Name, "--idle=" + idle.String(), "--no-login", "--no-keep-alive"}
	if opts.Seed.From != "" {
		args = append(args, "--from="+opts.Seed.From)
	}
	if opts.Seed.NoInherit {
		args = append(args, "--no-inherit")
	}
	return args
}

type warmBackend interface {
	Connect(context.Context, session.Profile, session.AcquireOptions) (*session.Handle, error)
	Acquire(context.Context, session.Profile, session.AcquireOptions) (*session.Handle, error)
	HolderPID(string) int
	LoggingIn(string) bool
	Inspect(context.Context, background.Spec) (background.Status, bool, error)
	Start(context.Context, background.Spec, []string) (background.Status, bool, error)
}

type realWarmBackend struct{}

func (realWarmBackend) Connect(ctx context.Context, p session.Profile, opts session.AcquireOptions) (*session.Handle, error) {
	return session.Connect(ctx, p, opts)
}
func (realWarmBackend) Acquire(ctx context.Context, p session.Profile, opts session.AcquireOptions) (*session.Handle, error) {
	return session.Acquire(ctx, p, opts)
}
func (realWarmBackend) HolderPID(name string) int  { return session.HolderPID(name) }
func (realWarmBackend) LoggingIn(name string) bool { return session.LoggingIn(name) }
func (realWarmBackend) Inspect(ctx context.Context, spec background.Spec) (background.Status, bool, error) {
	return spec.Inspect(ctx)
}
func (realWarmBackend) Start(ctx context.Context, spec background.Spec, args []string) (background.Status, bool, error) {
	return spec.Start(ctx, args)
}

func acquireWarm(ctx context.Context, owner Plugin, p session.Profile, opts session.AcquireOptions, idle time.Duration) (*session.Handle, error) {
	return acquireWarmUsing(ctx, owner, p, opts, idle, realWarmBackend{})
}

// A profile's existing daemon and its startup lock arbitrate concurrent first
// commands. Readiness means the browser socket answers, not merely that the
// process exists. Cancellation never stops a worker another caller may need.
func acquireWarmUsing(ctx context.Context, owner Plugin, p session.Profile, opts session.AcquireOptions, idle time.Duration, backend warmBackend) (*session.Handle, error) {
	spec := background.Spec{Dir: session.RunDir(), Key: "daemon:" + p.Name, Name: "daemon (profile " + p.Name + ")"}
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	started := false
	starts := 0
	retriedConnection := false
	for {
		if err := waitCtx.Err(); err != nil {
			return nil, fmt.Errorf("waiting for the browser for profile %q: %w; see sessh daemon logs %s --profile %s", p.Name, err, owner.Name(), p.Name)
		}
		if backend.LoggingIn(p.Name) {
			return nil, loginBusy(p.Name)
		}
		h, err := backend.Connect(waitCtx, p, opts)
		if err != nil && waitCtx.Err() != nil {
			return nil, waitCtx.Err()
		}
		// A login may have stopped the daemon during verification. No
		// command body has run yet, so waiting for that login is safe.
		if err != nil && backend.LoggingIn(p.Name) {
			return nil, loginBusy(p.Name)
		}
		if !errors.Is(err, session.ErrUnavailable) {
			if !retriedConnection && interruptedAcquisition(err) {
				// The idle timer or an explicit stop may close a socket after
				// its health probe. Only verification has run, so reacquiring
				// once cannot replay a user command or write.
				retriedConnection = true
				continue
			}
			return h, err
		}
		// A login intentionally has no sharing socket. Do not acquire the
		// profile while its window is starting: that would steal ownership.
		if backend.LoggingIn(p.Name) {
			return nil, loginBusy(p.Name)
		}
		st, live, err := backend.Inspect(waitCtx, spec)
		if err != nil {
			return nil, fmt.Errorf("checking warm browser: %w", err)
		}
		if live {
			started = true
			if st.State == "failed" {
				return nil, warmFailure(spec, st)
			}
			// A foreground command or capture may have taken over while an
			// old daemon is stopping. Share the actual profile holder.
			if pid := backend.HolderPID(p.Name); pid != 0 && pid != st.PID {
				return backend.Acquire(waitCtx, p, opts)
			}
		} else {
			if backend.HolderPID(p.Name) != 0 {
				return backend.Acquire(waitCtx, p, opts)
			}
			if started && st.State == "failed" {
				return nil, warmFailure(spec, st)
			}
			// Retry one idle-shutdown race, but do not loop forever when a
			// deliberately tiny timeout makes every new worker exit at once.
			if starts >= 2 {
				return nil, fmt.Errorf("browser for profile %q stopped before it could be used; increase --keep-alive or use --no-keep-alive", p.Name)
			}
			st, _, err = backend.Start(waitCtx, spec, warmArgs(owner, p, opts, idle))
			if err != nil {
				// Another caller may be launching the same worker, or its
				// predecessor may still be cleaning up. Both locks are short
				// lived; wait instead of starting a competing browser.
				if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
					return nil, fmt.Errorf("starting warm browser: %w (use --no-keep-alive to run without a background browser)", err)
				}
			} else {
				started = true
				starts++
				if st.State == "failed" {
					return nil, warmFailure(spec, st)
				}
				if opts.Notify != nil {
					opts.Notify(fmt.Sprintf("keeping profile %q warm (idle timeout %s)", p.Name, idle))
				}
			}
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(75 * time.Millisecond):
		}
	}
}

func interruptedAcquisition(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
}

func warmFailure(spec background.Spec, st background.Status) error {
	return fmt.Errorf("warm browser failed: %s; see %s (or use --no-keep-alive)", st.Detail, spec.LogPath())
}
