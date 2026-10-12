package plugin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/session"
)

type testPlugin struct {
	verify func(context.Context, session.Session) error
}

func (p *testPlugin) Name() string            { return "test" }
func (p *testPlugin) Command() *cobra.Command { return &cobra.Command{Use: "test"} }
func (p *testPlugin) Profile() session.Profile {
	return session.Profile{Name: "test", Origin: "https://tenant.example", Verify: p.verify}
}

// seams replaces the browser-facing parts of runSession for one test.
type seams struct {
	acquireErrs []error // returned by successive acquisitions, then success
	logins      []error // the failures relogin was asked to recover from
	loginErr    error
}

func stubSeams(t *testing.T, s *seams) {
	t.Helper()
	t.Setenv("SESSH_HOME", t.TempDir())
	t.Setenv("DISPLAY", ":0") // a window could open, even on a headless Linux runner
	flags, acquire, relogin := *globalFlags, acquireFn, reloginFn
	t.Cleanup(func() { *globalFlags, acquireFn, reloginFn = flags, acquire, relogin })
	acquireFn = func(_ context.Context, owner Plugin, _ session.AcquireOptions) (*session.Handle, error) {
		if len(s.acquireErrs) > 0 {
			err := s.acquireErrs[0]
			s.acquireErrs = s.acquireErrs[1:]
			if err != nil {
				return nil, err
			}
		}
		return &session.Handle{Profile: owner.Profile()}, nil
	}
	reloginFn = func(_ context.Context, _ Plugin, cause error) error {
		s.logins = append(s.logins, cause)
		return s.loginErr
	}
}

func TestRunSessionRecovery(t *testing.T) {
	expired := session.Authf("test", "no session for this profile")
	refused := &session.AuthError{Profile: "test", Detail: "not authenticated (HTTP 401)",
		Status: http.StatusUnauthorized, URL: "https://tenant.example/api/items?page=2"}
	denied := &session.AuthError{Profile: "test", Detail: "not authenticated (HTTP 403)",
		Status: http.StatusForbidden, URL: "https://tenant.example/api/private?page=2"}
	signedIn := func(context.Context, session.Session) error { return nil }
	signedOut := func(context.Context, session.Session) error { return expired }

	tests := []struct {
		name        string
		acquireErrs []error
		results     []error // returned by successive runs of the command
		verify      func(context.Context, session.Session) error
		noLogin     bool
		loginErr    error
		wantRuns    int
		wantLogins  int
		wantErr     string // "" for success
		wantAuth    bool
	}{
		{name: "a session found signed out logs in, then runs the command",
			acquireErrs: []error{expired}, wantRuns: 1, wantLogins: 1},
		{name: "a request refused with 401 logs in and runs the command again",
			results: []error{refused}, wantRuns: 2, wantLogins: 1},
		{name: "a second refusal is reported without another login",
			results: []error{refused, refused}, wantRuns: 2, wantLogins: 1,
			wantErr: "HTTP 401", wantAuth: true},
		{name: "--no-login reports a signed-out session",
			acquireErrs: []error{expired}, noLogin: true,
			wantErr: "no session", wantAuth: true},
		{name: "a failed login is reported and the command is not rerun",
			acquireErrs: []error{expired}, loginErr: errors.New("timed out waiting for login"),
			wantLogins: 1, wantErr: "timed out"},
		{name: "other failures do not log in",
			results: []error{errors.New("HTTP 500")}, wantRuns: 1, wantErr: "HTTP 500"},
		{name: "a 403 while signed in is a permission error, not a login",
			results: []error{denied}, verify: signedIn, wantRuns: 1,
			wantErr: "access denied (HTTP 403) for https://tenant.example/api/private: profile test is signed in"},
		{name: "a 403 from a session that no longer verifies logs in",
			results: []error{denied}, verify: signedOut, wantRuns: 2, wantLogins: 1},
		{name: "a 403 with an unreachable verifier does not open a login",
			results: []error{denied}, verify: func(context.Context, session.Session) error { return errors.New("connection lost") },
			wantRuns: 1, wantErr: "could not check the session after HTTP 403: connection lost"},
		{name: "a 403 with a timed out verifier does not open a login",
			results: []error{denied}, verify: func(context.Context, session.Session) error { return context.DeadlineExceeded },
			wantRuns: 1, wantErr: "context deadline exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &seams{acquireErrs: tt.acquireErrs, loginErr: tt.loginErr}
			stubSeams(t, s)
			globalFlags.NoLogin = tt.noLogin
			runs := 0
			err := runSession(context.Background(), &testPlugin{verify: tt.verify}, func(context.Context, session.Session) error {
				runs++
				if runs <= len(tt.results) {
					return tt.results[runs-1]
				}
				return nil
			})
			if runs != tt.wantRuns || len(s.logins) != tt.wantLogins {
				t.Errorf("ran %d time(s) with %d login(s), want %d and %d", runs, len(s.logins), tt.wantRuns, tt.wantLogins)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("err = %v, want success", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want one containing %q", err, tt.wantErr)
			case err != nil && session.IsAuth(err) != tt.wantAuth:
				t.Errorf("IsAuth(%v) = %v, want %v", err, session.IsAuth(err), tt.wantAuth)
			}
		})
	}
}

func TestReloginIsToldWhichRequestFailed(t *testing.T) {
	refused := &session.AuthError{Profile: "test", Status: http.StatusUnauthorized, URL: "https://tenant.example/api/items"}
	s := &seams{}
	stubSeams(t, s)
	runs := 0
	_ = runSession(context.Background(), &testPlugin{}, func(context.Context, session.Session) error {
		if runs++; runs == 1 {
			return refused
		}
		return nil
	})
	var ae *session.AuthError
	if len(s.logins) != 1 || !errors.As(s.logins[0], &ae) || ae.URL != refused.URL {
		t.Fatalf("relogin got %v, want the refused request", s.logins)
	}
}

func TestRunSessionWaitsForAnotherLogin(t *testing.T) {
	s := &seams{acquireErrs: []error{&session.ErrBusy{Profile: "test"}}}
	stubSeams(t, s)
	release, _, err := session.LockLogin(context.Background(), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	const held = 300 * time.Millisecond
	go func() {
		time.Sleep(held)
		release()
	}()
	start, runs := time.Now(), 0
	err = runSession(context.Background(), &testPlugin{}, func(context.Context, session.Session) error {
		runs++
		return nil
	})
	if err != nil || runs != 1 || len(s.logins) != 0 {
		t.Fatalf("err %v, runs %d, logins %d; want one run and no login of its own", err, runs, len(s.logins))
	}
	if time.Since(start) < held {
		t.Fatal("ran before the other login finished")
	}
}

func TestRunSessionWaitsForLoginAfterOwnRecovery(t *testing.T) {
	s := &seams{acquireErrs: []error{session.Authf("test", "expired"), &session.ErrBusy{Profile: "test"}}}
	stubSeams(t, s)
	reloginFn = func(ctx context.Context, _ Plugin, _ error) error {
		// A second caller acquires the login lock after our window closes to
		// confirm that it has been signed in too.
		release, _, err := session.LockLogin(ctx, "test", nil)
		if err != nil {
			return err
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			release()
		}()
		return nil
	}
	runs := 0
	err := runSession(context.Background(), &testPlugin{}, func(context.Context, session.Session) error {
		runs++
		return nil
	})
	if err != nil || runs != 1 {
		t.Fatalf("error=%v runs=%d; want successful command after waiting", err, runs)
	}
}

func TestRunSessionRetriesLoginThatAlreadyFinished(t *testing.T) {
	s := &seams{acquireErrs: []error{loginBusy("test")}}
	stubSeams(t, s)
	runs := 0
	err := runSession(context.Background(), &testPlugin{}, func(context.Context, session.Session) error {
		runs++
		return nil
	})
	if err != nil || runs != 1 || len(s.logins) != 0 {
		t.Fatalf("error=%v runs=%d logins=%d; want one run without a new login", err, runs, len(s.logins))
	}
}

func TestNoWindowWithoutADisplay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux can lack a display")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if interactive() {
		t.Fatal("a login window would be opened without a display")
	}
}

// answering is a session whose every request ends with err, or succeeds.
type answering struct {
	session.Session
	err error
}

func (a answering) Do(context.Context, *http.Request) (*http.Response, error) {
	if a.err != nil {
		return nil, a.err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func TestProbe(t *testing.T) {
	const url = "https://tenant.example/api/items"
	for _, tc := range []struct {
		name string
		url  string
		err  error
		want bool
	}{
		{"nothing to re-check", "", errors.New("unreachable"), true},
		{"the request gets through", url, nil, true},
		{"still refused", url, &session.AuthError{Status: http.StatusUnauthorized, URL: url}, false},
		{"now forbidden, a permission question", url, &session.AuthError{Status: http.StatusForbidden, URL: url}, true},
		{"the service cannot be reached", url, errors.New("connection refused"), false},
	} {
		if got := probeOK(context.Background(), answering{err: tc.err}, tc.url); got != tc.want {
			t.Errorf("%s: probeOK = %v, want %v", tc.name, got, tc.want)
		}
	}
}
