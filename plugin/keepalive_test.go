package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stefanm8/sessh/internal/background"
	"github.com/stefanm8/sessh/session"
)

func TestKeepAlivePolicy(t *testing.T) {
	previous := globalFlags
	t.Cleanup(func() { globalFlags = previous })
	for _, tc := range []struct {
		name, flag, env string
		disabled        bool
		want            time.Duration
		enabled, bad    bool
	}{
		{name: "default", want: 30 * time.Minute, enabled: true},
		{name: "environment", env: "5m", want: 5 * time.Minute, enabled: true},
		{name: "flag wins", flag: "2m", env: "5m", want: 2 * time.Minute, enabled: true},
		{name: "indefinite", flag: "0", enabled: true},
		{name: "off flag", flag: "off", env: "5m"},
		{name: "off environment", env: "off"},
		{name: "disabled wins", flag: "broken", disabled: true},
		{name: "negative", flag: "-1s", bad: true},
		{name: "bad duration", env: "later", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			globalFlags = &Flags{KeepAlive: tc.flag, NoKeepAlive: tc.disabled}
			t.Setenv("SESSH_KEEP_ALIVE", tc.env)
			idle, enabled, err := keepAlivePolicy()
			if (err != nil) != tc.bad || idle != tc.want || enabled != tc.enabled {
				t.Fatalf("policy = (%v, %v, %v), want (%v, %v, error=%v)", idle, enabled, err, tc.want, tc.enabled, tc.bad)
			}
		})
	}
}

func TestWarmArgsOnlyLaunchDaemon(t *testing.T) {
	p := session.Profile{Name: "different profile"}
	opts := session.AcquireOptions{Seed: session.SeedOptions{From: "parent profile", NoInherit: true}}
	got := warmArgs(&testPlugin{}, p, opts, 0)
	want := []string{"daemon", "test", "--profile=different profile", "--idle=0s", "--no-login", "--no-keep-alive", "--from=parent profile", "--no-inherit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

type warmSnapshot struct {
	state  string
	live   bool
	pid    int
	detail string
}

type fakeWarmBackend struct {
	connectErrors []error
	connectCalls  int
	acquireCalls  int
	startCalls    int
	inspectCalls  int
	startErrors   []error
	statuses      []warmSnapshot
	holder        int
	login         bool
	args          []string
	spec          background.Spec
	onStart       func()
	onConnect     func()
}

func (b *fakeWarmBackend) Connect(context.Context, session.Profile, session.AcquireOptions) (*session.Handle, error) {
	b.connectCalls++
	if b.onConnect != nil {
		b.onConnect()
	}
	if len(b.connectErrors) > 0 {
		err := b.connectErrors[0]
		b.connectErrors = b.connectErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	return &session.Handle{Routed: true}, nil
}
func (b *fakeWarmBackend) Acquire(context.Context, session.Profile, session.AcquireOptions) (*session.Handle, error) {
	b.acquireCalls++
	return &session.Handle{}, nil
}
func (b *fakeWarmBackend) HolderPID(string) int  { return b.holder }
func (b *fakeWarmBackend) LoggingIn(string) bool { return b.login }
func (b *fakeWarmBackend) Inspect(_ context.Context, spec background.Spec) (background.Status, bool, error) {
	b.inspectCalls++
	b.spec = spec
	st := warmSnapshot{state: "stopped"}
	if len(b.statuses) > 0 {
		st = b.statuses[0]
		b.statuses = b.statuses[1:]
	}
	return background.Status{State: st.state, PID: st.pid, Detail: st.detail}, st.live, nil
}
func (b *fakeWarmBackend) Start(_ context.Context, spec background.Spec, args []string) (background.Status, bool, error) {
	b.startCalls++
	b.spec, b.args = spec, args
	if b.onStart != nil {
		b.onStart()
	}
	if len(b.startErrors) > 0 {
		err := b.startErrors[0]
		b.startErrors = b.startErrors[1:]
		if err != nil {
			return background.Status{}, false, err
		}
	}
	return background.Status{State: "starting", PID: 100}, false, nil
}

func TestAcquireWarmLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		backend                     fakeWarmBackend
		starts, direct, connections int
		wantError                   string
	}{
		{name: "warm socket is immediate", connections: 1},
		{name: "cold starts daemon", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable}}, starts: 1, connections: 2},
		{name: "auth failure preserved", backend: fakeWarmBackend{connectErrors: []error{session.Authf("test", "expired")}}, connections: 1, wantError: "expired"},
		{name: "other connection failure preserved", backend: fakeWarmBackend{connectErrors: []error{errors.New("broken response")}}, connections: 1, wantError: "broken response"},
		{name: "idle shutdown during verification", backend: fakeWarmBackend{connectErrors: []error{fmt.Errorf("daemon request: %w", io.EOF)}}, connections: 2},
		{name: "closed connection during verification", backend: fakeWarmBackend{connectErrors: []error{net.ErrClosed}}, connections: 2},
		{name: "repeated connection loss is bounded", backend: fakeWarmBackend{connectErrors: []error{io.EOF, io.EOF}}, connections: 2, wantError: "EOF"},
		{name: "login owns profile", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable}, login: true}, wantError: "sign-in or logout"},
		{name: "capture owns profile", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable}, holder: 77}, direct: 1, connections: 1},
		{name: "already starting daemon", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable}, holder: 100, statuses: []warmSnapshot{{state: "starting", live: true, pid: 100}}}, connections: 2},
		{name: "concurrent first start", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable, session.ErrUnavailable}, startErrors: []error{fmt.Errorf("busy: %w", syscall.EWOULDBLOCK)}, statuses: []warmSnapshot{{}, {state: "starting", live: true, pid: 100}}}, starts: 1, connections: 3},
		{name: "startup failure reported", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable, session.ErrUnavailable}, statuses: []warmSnapshot{{}, {state: "failed", detail: "browser launch failed"}}}, starts: 1, connections: 2, wantError: "browser launch failed"},
		{name: "spawn failure reported", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable}, startErrors: []error{errors.New("executable missing")}}, starts: 1, connections: 1, wantError: "executable missing"},
		{name: "idle shutdown race retries", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable, session.ErrUnavailable}}, starts: 2, connections: 3},
		{name: "repeated early exits bounded", backend: fakeWarmBackend{connectErrors: []error{session.ErrUnavailable, session.ErrUnavailable, session.ErrUnavailable}}, starts: 2, connections: 3, wantError: "stopped before"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SESSH_HOME", t.TempDir())
			_, err := acquireWarmUsing(context.Background(), &testPlugin{}, session.Profile{Name: "custom"}, session.AcquireOptions{}, time.Minute, &tc.backend)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if tc.backend.startCalls != tc.starts || tc.backend.acquireCalls != tc.direct || tc.backend.connectCalls != tc.connections {
				t.Fatalf("calls: start=%d direct=%d connect=%d; want %d %d %d", tc.backend.startCalls, tc.backend.acquireCalls, tc.backend.connectCalls, tc.starts, tc.direct, tc.connections)
			}
			if tc.starts != 0 && tc.backend.spec.Key != "daemon:custom" {
				t.Fatalf("wrong service key: %q", tc.backend.spec.Key)
			}
		})
	}
}

func TestAcquireWarmCancellationDoesNotStartAgain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &fakeWarmBackend{connectErrors: []error{session.ErrUnavailable}, onStart: cancel}
	_, err := acquireWarmUsing(ctx, &testPlugin{}, session.Profile{Name: "custom"}, session.AcquireOptions{}, time.Minute, b)
	if !errors.Is(err, context.Canceled) || b.startCalls != 1 || b.connectCalls != 1 {
		t.Fatalf("err=%v starts=%d connections=%d", err, b.startCalls, b.connectCalls)
	}
}

func TestAcquireWarmWaitsWhenLoginStopsVerification(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%v", canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := &fakeWarmBackend{connectErrors: []error{errors.New("session daemon request failed: EOF")}}
			b.onConnect = func() {
				b.login = true
				if canceled {
					cancel()
				}
			}
			_, err := acquireWarmUsing(ctx, &testPlugin{}, session.Profile{Name: "custom"}, session.AcquireOptions{}, time.Minute, b)
			var busy *session.ErrBusy
			if canceled && !errors.Is(err, context.Canceled) || !canceled && !errors.As(err, &busy) {
				t.Fatalf("error = %v; want cancellation or wait-for-login error", err)
			}
			if b.startCalls != 0 || b.acquireCalls != 0 || b.connectCalls != 1 {
				t.Fatalf("another acquisition started during login: %+v", b)
			}
		})
	}
}

func TestNoKeepAliveRespectsActiveLogin(t *testing.T) {
	t.Setenv("SESSH_HOME", t.TempDir())
	previous := globalFlags
	globalFlags = &Flags{NoKeepAlive: true}
	t.Cleanup(func() { globalFlags = previous })
	release, _, err := session.LockLogin(context.Background(), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Acquire(context.Background(), &testPlugin{}, session.AcquireOptions{})
	var busy *session.ErrBusy
	if !errors.As(err, &busy) || busy.Profile != "test" {
		t.Fatalf("error = %v; want active-login error without launching a browser", err)
	}
}
