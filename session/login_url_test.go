package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoginURLs(t *testing.T) {
	for _, rawurl := range []string{
		"https://id.atlassian.com/login", "https://ID.ATLASSIAN.COM:443/",
		"https://login.microsoftonline.com/common/oauth2/authorize", "https://login.live.com/",
		"https://tenant.example/login", "https://tenant.example/wiki/login.action?os_destination=/wiki/",
		"https://bitbucket.org/account/signin/", "https://teams.microsoft.com/_#/login",
		"https://teams.microsoft.com/_#/login?tenantId=example",
	} {
		if !IsLoginURL(rawurl) {
			t.Errorf("missed sign-in URL %s", rawurl)
		}
	}
	for _, rawurl := range []string{
		"https://tenant.example/wiki/", "https://tenant.example/login-guide",
		"https://tenant.example/?next=https://id.atlassian.com/login",
		"https://id.atlassian.com.example.org/", "https://notlogin.microsoftonline.com/",
	} {
		if IsLoginURL(rawurl) {
			t.Errorf("mistook ordinary URL for sign-in: %s", rawurl)
		}
	}
}

type redirectingSession struct{ Session }

func (redirectingSession) Navigate(context.Context, string) (string, error) {
	return "https://tenant.example/app", nil
}

func (redirectingSession) CurrentURL(context.Context) (string, error) {
	return "https://id.atlassian.com/login", nil
}

func TestSSOObservesRedirectAfterNavigation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := VerifySSO(ctx, redirectingSession{}, "https://tenant.example/app", func(context.Context, Session) (Identity, error) {
		return Identity{}, nil
	})
	if !IsAuth(err) {
		t.Fatalf("late login redirect = %v; want immediate auth error", err)
	}
}

func TestObserveCurrentURLDoesNotNavigate(t *testing.T) {
	page := &pageSession{}
	url, err := currentURL(context.Background(), Observe(page), "")
	if err != nil || url != "https://idp.example/login" || len(page.navigated) != 0 {
		t.Fatalf("URL = %q, error = %v, navigations = %v", url, err, page.navigated)
	}
}

type unsettledURLSession struct {
	redirectingSession
	calls int
	err   error
}

func (s *unsettledURLSession) CurrentURL(context.Context) (string, error) {
	s.calls++
	if s.calls == 1 {
		return "", s.err
	}
	return "https://id.atlassian.com/login", nil
}

func TestSSOCurrentURLFailure(t *testing.T) {
	for _, transient := range []bool{false, true} {
		failure := errors.New("browser disconnected")
		if transient {
			failure = errors.New("Execution context was destroyed, most likely because of a navigation")
		}
		s := &unsettledURLSession{err: failure}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := VerifySSO(ctx, s, "https://tenant.example/app", func(context.Context, Session) (Identity, error) {
			return Identity{}, nil
		})
		cancel()
		if transient {
			if !IsAuth(err) || s.calls != 2 {
				t.Fatalf("transient redirect = %v after %d URL checks", err, s.calls)
			}
		} else if !errors.Is(err, failure) || s.calls != 1 {
			t.Fatalf("browser failure = %v after %d URL checks", err, s.calls)
		}
	}
}
