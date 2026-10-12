package session

import (
	"context"
	"fmt"
	"time"
)

// VerificationTimeout bounds a whole automatic authentication check, including
// requests, navigation and app startup. Interactive login has its own longer
// deadline; each check while the user signs in still uses this budget.
const VerificationTimeout = 15 * time.Second

// Verify is the entry point for every profile's authentication hook. Plugins
// supply service-specific evidence; the session layer owns the time budget.
// A slow or unreachable service is not evidence that the user is signed out.
func Verify(ctx context.Context, p Profile, s Session) error {
	return verifyWithin(ctx, p, s, VerificationTimeout)
}

func verifyWithin(parent context.Context, p Profile, s Session, timeout time.Duration) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if p.Verify == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Verify(ctx, s) }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if ctx.Err() != nil {
		return fmt.Errorf("authentication check for profile %q timed out after %s: %w", p.Name, timeout, ctx.Err())
	}
	return err
}

// WaitForAuth polls only while a service reports pending initialization. A
// confirmed sign-out or transport failure returns immediately. Its deadline
// cannot extend the enclosing Verify budget.
func WaitForAuth(ctx context.Context, probe func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, VerificationTimeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready, err := probe(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ready || err != nil {
			return err
		}
		timer := time.NewTimer(1500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// CachedIdentity reads only evidence already collected by this session. An
// optional display name must never delay an otherwise authenticated command.
func CachedIdentity(s Session) Identity {
	if cached, ok := s.(interface{ CachedIdentity() Identity }); ok {
		return cached.CachedIdentity()
	}
	return Identity{}
}

func (o observer) CachedIdentity() Identity { return CachedIdentity(o.Session) }

// LookupIdentity is optional enrichment for status/login output, never a
// prerequisite for running a command. These callers release the session next.
func LookupIdentity(ctx context.Context, s Session) Identity {
	if id := CachedIdentity(s); !id.Empty() {
		return id
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	id, _ := s.Whoami(ctx)
	return id
}
