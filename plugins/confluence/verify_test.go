package confluence

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
	requests      int
	navigations   int
	signedInAt    int
	anonymousJSON bool
	landed        string
	requestErr    error
	requestErrAt  int
	remembered    session.Identity
}

func (s *verifySession) RememberIdentity(id session.Identity) { s.remembered = id }

func (s *verifySession) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	s.requests++
	if s.requestErr != nil && (s.requestErrAt == 0 || s.requests == s.requestErrAt) {
		return nil, s.requestErr
	}
	body := `{"accountId":"test-user","displayName":"Test User"}`
	if s.signedInAt == 0 || s.requests < s.signedInAt {
		if !s.anonymousJSON {
			return nil, &session.AuthError{Profile: "atlassian", Status: http.StatusForbidden}
		}
		// Confluence can return a named anonymous user with HTTP 200. Only
		// accountId establishes the live account required by verification.
		body = `{"type":"anonymous","displayName":"Anonymous"}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (s *verifySession) Navigate(context.Context, string) (string, error) {
	s.navigations++
	return s.landed, nil
}

func TestVerifySession(t *testing.T) {
	t.Setenv("CONFLUENCE_SITE", "https://tenant.example")
	for _, tt := range []struct {
		name        string
		session     verifySession
		wantAuth    bool
		requests    int
		navigations int
	}{
		{
			name:     "valid session remembers identity without navigation",
			session:  verifySession{signedInAt: 1},
			requests: 1,
		},
		{
			name:        "completed silent SSO needs no polling delay",
			session:     verifySession{signedInAt: 2, landed: "https://tenant.example/wiki/"},
			requests:    2,
			navigations: 1,
		},
		{
			name:        "403 followed by Atlassian login returns auth immediately",
			session:     verifySession{landed: "https://id.atlassian.com/login?continue=example"},
			wantAuth:    true,
			requests:    2,
			navigations: 1,
		},
		{
			name:        "anonymous identity followed by login returns auth immediately",
			session:     verifySession{anonymousJSON: true, landed: "https://id.atlassian.com/login"},
			wantAuth:    true,
			requests:    2,
			navigations: 1,
		},
		{
			name:        "cookie minted during login redirect is checked before rejecting it",
			session:     verifySession{signedInAt: 2, landed: "https://id.atlassian.com/login"},
			requests:    2,
			navigations: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Shorter than the 1.5-second polling interval: none of these
			// completed authentication decisions should wait for another poll.
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
			if tt.wantAuth {
				if !tt.session.remembered.Empty() {
					t.Fatalf("remembered anonymous identity: %+v", tt.session.remembered)
				}
			} else if tt.session.remembered.AccountID != "test-user" {
				t.Fatal("successful verification did not retain the identity for metadata")
			}
		})
	}
}

func TestVerifyRechecksRememberedIdentity(t *testing.T) {
	t.Setenv("CONFLUENCE_SITE", "https://tenant.example")
	s := &verifySession{signedInAt: 1, landed: "https://id.atlassian.com/login"}
	if err := verify(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s.signedInAt = 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := verify(ctx, s); !session.IsAuth(err) {
		t.Fatalf("remembered identity bypassed live verification: %v", err)
	}
}

func TestVerifyPreservesTransportError(t *testing.T) {
	t.Setenv("CONFLUENCE_SITE", "https://tenant.example")
	failure := errors.New("connection failed")
	for _, tt := range []struct {
		name        string
		request     int
		navigations int
	}{
		{"initial identity request", 1, 0},
		{"identity request after navigation", 2, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &verifySession{requestErr: failure, requestErrAt: tt.request, landed: "https://id.atlassian.com/login"}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := verify(ctx, s); !errors.Is(err, failure) {
				t.Fatalf("verify = %v; want transport error %v", err, failure)
			}
			if s.requests != tt.request || s.navigations != tt.navigations {
				t.Fatalf("requests = %d, navigations = %d; want %d, %d",
					s.requests, s.navigations, tt.request, tt.navigations)
			}
		})
	}
}
