package plugin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stefanm8/sessh/session"
)

type mutationRequests struct {
	session.Session
	results []error
	methods []string
}

func (s *mutationRequests) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	s.methods = append(s.methods, req.Method)
	if n := len(s.methods) - 1; n < len(s.results) && s.results[n] != nil {
		return nil, s.results[n]
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

func TestMutationRecovery(t *testing.T) {
	expired := &session.AuthError{Profile: "test", Status: http.StatusUnauthorized}
	denied := &session.AuthError{Profile: "test", Status: http.StatusForbidden}
	network := errors.New("response connection lost")
	for _, tt := range []struct {
		name       string
		methods    []string
		results    []error
		wantLogins int
		wantCalls  int
		wantErr    error
		wantGuard  bool
	}{
		{"preflight can sign in", []string{"GET", "POST"}, []error{expired}, 1, 3, nil, false},
		{"write refused", []string{"POST"}, []error{expired}, 0, 1, expired, true},
		{"write forbidden", []string{"PUT"}, []error{denied}, 0, 1, denied, true},
		{"write outcome unknown", []string{"POST"}, []error{network}, 0, 1, network, true},
		{"read fails after successful write", []string{"POST", "GET"}, []error{nil, expired}, 0, 2, expired, true},
		{"later write fails", []string{"PUT", "POST"}, []error{nil, expired}, 0, 2, expired, true},
		{"successful write", []string{"POST"}, nil, 0, 1, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seams := &seams{}
			stubSeams(t, seams)
			requests := &mutationRequests{results: tt.results}
			acquire := acquireFn
			acquireFn = func(ctx context.Context, p Plugin, opts session.AcquireOptions) (*session.Handle, error) {
				h, err := acquire(ctx, p, opts)
				if h != nil {
					h.Session = requests
				}
				return h, err
			}
			run := WithMutationSession(&testPlugin{}, func(ctx context.Context, s session.Session, _ *cobra.Command, _ []string) error {
				for _, method := range tt.methods {
					req, _ := session.Request(ctx, method, "/api/items", nil)
					res, err := s.Do(ctx, req)
					if err != nil {
						return err
					}
					res.Body.Close()
				}
				return nil
			})
			err := run(&cobra.Command{}, nil)
			matches := errors.Is(err, tt.wantErr)
			if tt.wantErr == denied {
				matches = err != nil && strings.Contains(err.Error(), "access denied (HTTP 403)") && !session.IsAuth(err)
			}
			if !matches || attemptedMutation(err) != tt.wantGuard {
				t.Fatalf("error = %v, guarded = %v; want %v, %v", err, attemptedMutation(err), tt.wantErr, tt.wantGuard)
			}
			if len(seams.logins) != tt.wantLogins || len(requests.methods) != tt.wantCalls {
				t.Fatalf("logins = %d, requests = %v; want %d logins, %d requests", len(seams.logins), requests.methods, tt.wantLogins, tt.wantCalls)
			}
		})
	}
}
