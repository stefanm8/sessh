package teams

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stefanm8/sessh/session"
)

type verifySession struct {
	session.Session
	landed    string
	probeURL  string
	ready     bool
	evalErr   error
	transient bool
	evals     int
}

func (s *verifySession) Navigate(context.Context, string) (string, error) {
	return s.landed, nil
}

func (s *verifySession) Eval(_ context.Context, _ string, _, out any) error {
	s.evals++
	if s.evalErr != nil && (!s.transient || s.evals == 1) {
		return s.evalErr
	}
	b, err := json.Marshal(struct {
		URL   string `json:"url"`
		Ready bool   `json:"ready"`
	}{s.probeURL, s.ready})
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func TestVerifyStopsAtSignIn(t *testing.T) {
	for _, loginURL := range []string{
		"https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		"https://login.live.com/oauth20_authorize.srf",
		"https://teams.microsoft.com/_#/login",
	} {
		for _, redirectOnProbe := range []bool{false, true} {
			name := loginURL
			if redirectOnProbe {
				name += "/after-probe"
			}
			t.Run(name, func(t *testing.T) {
				s := &verifySession{landed: loginURL, probeURL: loginURL}
				if redirectOnProbe {
					s.landed = "https://teams.microsoft.com/v2/"
				}
				// A signed-out result must arrive before any polling interval.
				ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
				defer cancel()
				if err := verify(ctx, s); !errors.Is(err, session.ErrAuth) {
					t.Fatalf("verify = %v, want immediate authentication error", err)
				}
				wantEvals := 0
				if redirectOnProbe {
					wantEvals = 1
				}
				if s.evals != wantEvals {
					t.Fatalf("evaluations = %d, want %d", s.evals, wantEvals)
				}
			})
		}
	}
}

func TestVerifyReady(t *testing.T) {
	s := &verifySession{landed: "https://teams.microsoft.com/v2/", ready: true}
	if err := verify(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if s.evals != 1 {
		t.Fatalf("evaluations = %d, want 1", s.evals)
	}
}

func TestVerifyHonorsCancellation(t *testing.T) {
	s := &verifySession{landed: "https://teams.microsoft.com/v2/"}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := verify(ctx, s); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("verify = %v, want caller deadline", err)
	}
}

func TestVerifyPreservesBrowserError(t *testing.T) {
	wantErr := errors.New("browser connection closed")
	s := &verifySession{landed: "https://teams.microsoft.com/v2/", evalErr: wantErr}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := verify(ctx, s); !errors.Is(err, wantErr) {
		t.Fatalf("verify = %v, want %v", err, wantErr)
	}
	if s.evals != 1 {
		t.Fatalf("evaluations = %d, want 1", s.evals)
	}
}

func TestVerifyRecoversDuringNavigation(t *testing.T) {
	s := &verifySession{
		landed:    "https://teams.microsoft.com/v2/",
		ready:     true,
		evalErr:   errors.New("Execution context was destroyed, most likely because of a navigation"),
		transient: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := verify(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s.evals != 2 {
		t.Fatalf("evaluations = %d, want 2", s.evals)
	}
}
