package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifyPreservesResults(t *testing.T) {
	failure := errors.New("browser disconnected")
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"authenticated", nil},
		{"authentication required", Authf("work", "sign in required")},
		{"browser failure", failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			p := Profile{Verify: func(context.Context, Session) error {
				calls++
				return tt.err
			}}
			if err := Verify(context.Background(), p, nil); !errors.Is(err, tt.err) {
				t.Fatalf("Verify = %v, want %v", err, tt.err)
			}
			if calls != 1 {
				t.Fatalf("verification calls = %d, want exactly one", calls)
			}
		})
	}
	if err := Verify(context.Background(), Profile{}, nil); err != nil {
		t.Fatalf("profile with no verification hook: %v", err)
	}
}

func TestVerifyDeadline(t *testing.T) {
	t.Run("every profile gets the shared timeout", func(t *testing.T) {
		started := time.Now()
		var deadline time.Time
		p := Profile{Verify: func(ctx context.Context, _ Session) error {
			deadline, _ = ctx.Deadline()
			return nil
		}}
		if err := Verify(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
		if deadline.Before(started.Add(VerificationTimeout)) || deadline.After(time.Now().Add(VerificationTimeout)) {
			t.Fatalf("verification deadline = %v, want shared timeout %v from start", deadline, VerificationTimeout)
		}
	})
	t.Run("an earlier caller deadline wins", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		want, _ := ctx.Deadline()
		var got time.Time
		p := Profile{Verify: func(ctx context.Context, _ Session) error {
			got, _ = ctx.Deadline()
			return nil
		}}
		if err := Verify(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
		if !got.Equal(want) {
			t.Fatalf("verification deadline = %v, want caller deadline %v", got, want)
		}
	})
}

func TestVerifyCanceledContextDoesNotProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	p := Profile{Verify: func(context.Context, Session) error {
		calls.Add(1)
		return nil
	}}
	if err := Verify(ctx, p, nil); !errors.Is(err, context.Canceled) || IsAuth(err) {
		t.Fatalf("Verify = %v, want cancellation without an auth error", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("canceled verification made %d probes", got)
	}
}

func TestVerifyInterruptsNoncooperativeHook(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		name := "timeout"
		if cancelCaller {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			p := Profile{Verify: func(context.Context, Session) error {
				defer close(finished)
				close(started)
				<-release // Simulates a browser operation that ignores context.
				return nil
			}}
			result := make(chan error, 1)
			go func() { result <- verifyWithin(ctx, p, nil, 50*time.Millisecond) }()
			defer func() {
				close(release)
				<-finished
			}()
			<-started
			want := context.DeadlineExceeded
			if cancelCaller {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) || IsAuth(err) {
					t.Fatalf("Verify = %v, want %v without an auth error", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("verification waited for a hook that ignored its context")
			}
		})
	}
}

type verificationSession struct {
	Session
	navigate   func(context.Context, string) (string, error)
	remembered Identity
}

func (s *verificationSession) Navigate(ctx context.Context, rawurl string) (string, error) {
	return s.navigate(ctx, rawurl)
}

func (s *verificationSession) RememberIdentity(id Identity) { s.remembered = id }

func TestVerifySSOSharesDeadlineAcrossRequestNavigationAndPolling(t *testing.T) {
	// Both the first HTTP request and navigation consume the same budget as
	// the eventual polling loop. A new timeout after either would reset it.
	deadlines := make(chan time.Time, 3)
	record := func(ctx context.Context) {
		deadline, _ := ctx.Deadline()
		deadlines <- deadline
	}
	delay := func(ctx context.Context) error {
		select {
		case <-time.After(10 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := &verificationSession{navigate: func(ctx context.Context, _ string) (string, error) {
		record(ctx)
		return "https://tenant.example/app", delay(ctx)
	}}
	requests := 0
	finished := make(chan struct{})
	p := Profile{Verify: func(ctx context.Context, s Session) error {
		defer close(finished)
		return VerifySSO(ctx, s, "https://tenant.example/app", func(ctx context.Context, _ Session) (Identity, error) {
			record(ctx)
			requests++
			if requests == 1 {
				return Identity{}, delay(ctx)
			}
			<-ctx.Done()
			return Identity{}, ctx.Err()
		})
	}}
	err := verifyWithin(context.Background(), p, s, 100*time.Millisecond)
	<-finished
	if !errors.Is(err, context.DeadlineExceeded) || IsAuth(err) {
		t.Fatalf("VerifySSO = %v, want deadline exceeded without an auth error", err)
	}
	if requests != 2 || len(deadlines) != 3 {
		t.Fatalf("identity requests = %d, phases = %d; want first request, navigation, and poll", requests, len(deadlines))
	}
	first := <-deadlines
	for range 2 {
		if got := <-deadlines; !got.Equal(first) {
			t.Fatalf("verification reset its deadline: first = %v, later = %v", first, got)
		}
	}
}

func TestVerifySSORetainedIdentityNeverSkipsLiveAuthentication(t *testing.T) {
	var requests, navigations int
	signedIn := true
	s := &verificationSession{navigate: func(context.Context, string) (string, error) {
		navigations++
		return "https://id.atlassian.com/login", nil
	}}
	p := Profile{Verify: func(ctx context.Context, s Session) error {
		return VerifySSO(ctx, s, "https://tenant.example/app", func(context.Context, Session) (Identity, error) {
			requests++
			if signedIn {
				return Identity{AccountID: "account"}, nil
			}
			return Identity{}, nil
		})
	}}
	if err := Verify(context.Background(), p, s); err != nil {
		t.Fatal(err)
	}
	if s.remembered.AccountID != "account" || navigations != 0 || requests != 1 {
		t.Fatalf("signed-in check: remembered=%+v, navigations=%d, requests=%d", s.remembered, navigations, requests)
	}
	signedIn = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Verify(ctx, p, s); !IsAuth(err) {
		t.Fatalf("revoked session with remembered identity: %v, want authentication required", err)
	}
	if navigations != 1 || requests != 3 {
		t.Fatalf("revoked session: navigations=%d, requests=%d; want one SSO attempt and two live checks", navigations, requests)
	}
}

func TestWaitForAuthImmediateResults(t *testing.T) {
	failure := errors.New("browser disconnected")
	for _, tt := range []struct {
		name  string
		ready bool
		err   error
	}{
		{"ready", true, nil},
		{"authentication required", false, ErrAuth},
		{"hard browser failure", false, failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			calls := 0
			err := WaitForAuth(ctx, func(context.Context) (bool, error) {
				calls++
				return tt.ready, tt.err
			})
			if !errors.Is(err, tt.err) || calls != 1 {
				t.Fatalf("WaitForAuth = %v after %d probes, want %v after one immediate probe", err, calls, tt.err)
			}
		})
	}
}

func TestWaitForAuthDeadlineIsNotAuthenticationFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	calls := 0
	err := WaitForAuth(ctx, func(context.Context) (bool, error) {
		calls++
		return false, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || IsAuth(err) {
		t.Fatalf("WaitForAuth = %v, want deadline exceeded without an auth error", err)
	}
	if calls != 1 {
		t.Fatalf("WaitForAuth made %d probes before deadline, want one", calls)
	}
}

func TestCachedIdentityNeverRequestsMetadata(t *testing.T) {
	for _, rpc := range []bool{false, true} {
		name := "browser"
		if rpc {
			name = "rpc"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			p := Profile{Whoami: func(context.Context, Session) (Identity, error) {
				calls++
				return Identity{}, errors.New("optional identity endpoint unavailable")
			}}
			var s Session = &browserSession{profile: p}
			if rpc {
				s = &rpcClient{profile: p}
			}
			if id := CachedIdentity(s); !id.Empty() || calls != 0 {
				t.Fatalf("empty identity = %+v with %d metadata requests", id, calls)
			}
			RememberIdentity(s, Identity{AccountID: "verified"})
			if id := CachedIdentity(Observe(s)); id.AccountID != "verified" || calls != 0 {
				t.Fatalf("remembered identity = %+v with %d metadata requests", id, calls)
			}
		})
	}
}
