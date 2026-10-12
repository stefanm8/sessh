package session

import "context"

// VerifySSO checks an account-based session and visits loginURL once to let
// silent SSO mint a missing service cookie. whoami must check the live session
// and return an empty AccountID when signed out. A login page ends the wait
// immediately; only a pending SSO redirect needs polling. Callers can replace
// ErrAuth with a service-specific login hint.
func VerifySSO(ctx context.Context, s Session, loginURL string, whoami func(context.Context, Session) (Identity, error)) error {
	ctx, cancel := context.WithTimeout(ctx, VerificationTimeout)
	defer cancel()
	check := func() (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		id, err := whoami(ctx, s)
		if err != nil {
			return false, err
		}
		if id.AccountID == "" {
			return false, nil
		}
		RememberIdentity(s, id)
		return true, nil
	}
	if ok, err := check(); ok || err != nil {
		return err
	}
	if _, err := s.Navigate(ctx, loginURL); err != nil {
		return err
	}
	return WaitForAuth(ctx, func(ctx context.Context) (bool, error) {
		// Check before rejecting a login redirect: it may have already minted
		// the service cookie, even if the page has not returned to the tenant.
		if ok, err := check(); ok || err != nil {
			return ok, err
		}
		landed, err := Observe(s).Navigate(ctx, "")
		if err != nil {
			if IsNavigationTransient(err) {
				return false, nil
			}
			return false, err
		}
		if IsLoginURL(landed) {
			return false, ErrAuth
		}
		return false, nil
	})
}
