package session

import (
	"errors"
	"fmt"
)

// ErrAuth is the sentinel every "you are not logged in" condition surfaces as:
// a 401/403, a redirect to a login origin, or a Verify that could not mint a
// session. cmd/sessh turns it into `run: sessh auth login <plugin>` rather than a
// stack trace.
var ErrAuth = errors.New("not authenticated")

// AuthError carries the profile that needs logging in, so the top level can
// name the right command.
type AuthError struct {
	Profile string
	Detail  string
	// Status and URL identify the response that reported the failure: 401 or
	// 403, or 0 for a redirect to a login page. Both are empty when a Verify
	// hook reported it.
	Status int
	URL    string
}

func (e *AuthError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("not authenticated for profile %q", e.Profile)
	}
	return e.Detail
}

func (e *AuthError) Unwrap() error { return ErrAuth }

// Authf builds an ErrAuth-wrapped error.
func Authf(profile, format string, args ...any) error {
	return &AuthError{Profile: profile, Detail: fmt.Sprintf(format, args...)}
}

// IsAuth reports whether err is (or wraps) ErrAuth.
func IsAuth(err error) bool { return errors.Is(err, ErrAuth) }

// ErrBusy is returned when a profile is locked by another process and no
// socket was available to route through.
type ErrBusy struct {
	Profile string
	Holder  string
	PID     int // the holder's process, or 0 when unknown
}

func (e *ErrBusy) Error() string {
	if e.Holder != "" {
		return fmt.Sprintf("profile %q is busy (held by %s)", e.Profile, e.Holder)
	}
	return fmt.Sprintf("profile %q is busy", e.Profile)
}
