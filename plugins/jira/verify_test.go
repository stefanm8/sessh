package jira

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stefanm8/sessh/session"
)

type verifySession struct {
	session.Session
	requests    int
	navigations int
	signedInAt  int
	landed      string
	requestErr  error
	navigateErr error
	remembered  session.Identity
}

func (s *verifySession) RememberIdentity(id session.Identity) { s.remembered = id }

func (s *verifySession) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	s.requests++
	if s.requestErr != nil {
		return nil, s.requestErr
	}
	if s.signedInAt == 0 || s.requests < s.signedInAt {
		return nil, &session.AuthError{Profile: "atlassian", Status: http.StatusUnauthorized}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"accountId":"test-user"}`)),
		Request:    req,
	}, nil
}

func (s *verifySession) Navigate(context.Context, string) (string, error) {
	s.navigations++
	return s.landed, s.navigateErr
}

func TestVerifySession(t *testing.T) {
	t.Setenv("JIRA_SITE", "https://tenant.example")
	tests := []struct {
		name        string
		session     verifySession
		wantAuth    bool
		requests    int
		navigations int
	}{
		{
			name:     "valid session needs no navigation",
			session:  verifySession{signedInAt: 1},
			requests: 1,
		},
		{
			name:        "completed silent SSO needs no polling delay",
			session:     verifySession{signedInAt: 2, landed: "https://tenant.example/jira/"},
			requests:    2,
			navigations: 1,
		},
		{
			name:        "Atlassian sign-in returns auth immediately",
			session:     verifySession{landed: "https://id.atlassian.com/login?continue=example"},
			wantAuth:    true,
			requests:    2,
			navigations: 1,
		},
		{
			name:        "tenant sign-in returns auth immediately",
			session:     verifySession{landed: "https://tenant.example/login"},
			wantAuth:    true,
			requests:    2,
			navigations: 1,
		},
		{
			name:        "cookie minted at login redirect is checked before rejecting it",
			session:     verifySession{signedInAt: 2, landed: "https://id.atlassian.com/login"},
			requests:    2,
			navigations: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Shorter than a polling interval: these paths must not sleep.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := verify(ctx, &tt.session)
			if (tt.wantAuth && !session.IsAuth(err)) || (!tt.wantAuth && err != nil) {
				t.Fatalf("verify = %v; wantAuth = %v", err, tt.wantAuth)
			}
			if tt.session.requests != tt.requests || tt.session.navigations != tt.navigations {
				t.Fatalf("requests = %d, navigations = %d; want %d, %d",
					tt.session.requests, tt.session.navigations, tt.requests, tt.navigations)
			}
			if !tt.wantAuth && tt.session.remembered.AccountID != "test-user" {
				t.Fatal("successful verification did not retain the identity for metadata")
			}
		})
	}
}

func TestVerifyRechecksRememberedIdentity(t *testing.T) {
	t.Setenv("JIRA_SITE", "https://tenant.example")
	s := &verifySession{signedInAt: 1, landed: "https://id.atlassian.com/login"}
	if err := verify(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s.signedInAt = 0
	if err := verify(context.Background(), s); !session.IsAuth(err) {
		t.Fatalf("remembered identity bypassed live verification: %v", err)
	}
}

func TestVerifyPollsPendingSSO(t *testing.T) {
	t.Setenv("JIRA_SITE", "https://tenant.example")
	s := &verifySession{signedInAt: 3, landed: "https://tenant.example/jira/"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := verify(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s.requests != 3 || s.navigations != 1 {
		t.Fatalf("requests = %d, navigations = %d; want 3, 1", s.requests, s.navigations)
	}
}

func TestVerifyStopsOnErrors(t *testing.T) {
	t.Setenv("JIRA_SITE", "https://tenant.example")
	failure := errors.New("connection failed")
	for _, tt := range []struct {
		name    string
		session verifySession
	}{
		{"identity request", verifySession{requestErr: failure}},
		{"navigation", verifySession{navigateErr: failure}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := verify(context.Background(), &tt.session); !errors.Is(err, failure) {
				t.Fatalf("verify = %v; want %v", err, failure)
			}
		})
	}
	t.Run("cancel pending SSO", func(t *testing.T) {
		s := &verifySession{landed: "https://tenant.example/jira/"}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := verify(ctx, s); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("verify = %v; want cancellation", err)
		}
	})
}
