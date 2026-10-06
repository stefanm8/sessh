package session

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestAcquireProfileAfterHolderExits(t *testing.T) {
	isolatedHome(t)
	holder, err := acquireLock("work")
	if err != nil {
		t.Fatal(err)
	}
	// The holder finishes without ever serving a socket. A waiting command
	// must take the released lock instead of reporting a stale busy error.
	done := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		holder.Release()
		close(done)
	}()
	defer func() { <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lock, client, err := acquireProfile(ctx, "work", false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if lock == nil || client != nil {
		t.Fatal("expected to own the released profile, without a socket client")
	}
}

func TestAcquireProfileRoutesToHolder(t *testing.T) {
	// Keep Unix socket paths below macOS's length limit.
	home, err := os.MkdirTemp("/tmp", "sessh-manager-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("SESSH_HOME", home)
	holder, err := acquireLock("work")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	server, err := serve(nil, "work")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	lock, client, err := acquireProfile(context.Background(), "work", false)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil || lock != nil {
		t.Fatal("expected to share the holder's session")
	}
	defer client.Close()

	_, _, err = acquireProfile(context.Background(), "work", true)
	var busy *ErrBusy
	if !errors.As(err, &busy) {
		t.Fatalf("headed acquisition = %v; want busy instead of sharing", err)
	}
}

func TestAcquireProfileCancellation(t *testing.T) {
	isolatedHome(t)
	holder, err := acquireLock("work")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	lock, client, err := acquireProfile(ctx, "work", false)
	if !errors.Is(err, context.DeadlineExceeded) || lock != nil || client != nil {
		t.Fatalf("acquisition = %v, %v, %v; want cancellation", lock, client, err)
	}
}

func TestConnectMissingDoesNotPrepareProfile(t *testing.T) {
	isolatedHome(t)
	_, err := Connect(context.Background(), Profile{Name: "unused"}, AcquireOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Connect = %v; want unavailable", err)
	}
	if _, err := os.Stat(ProfileDir("unused")); !os.IsNotExist(err) {
		t.Fatalf("Connect created a profile: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Connect(ctx, Profile{}, AcquireOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Connect = %v", err)
	}
}

func TestConnectCancelsUnresponsiveHolder(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sessh-stall-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("SESSH_HOME", home)
	if err := os.MkdirAll(RunDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", SocketPath("work"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Connect(ctx, Profile{Name: "work"}, AcquireOptions{})
	if conn := <-accepted; conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("Connect = %v; want caller deadline", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Connect did not interrupt the holder's stalled ping")
	}
}

func TestConnectVerifiesCallerProfile(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sessh-connect-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("SESSH_HOME", home)
	server, err := serve(nil, DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	verified := 0
	p := Profile{
		Origin: "https://caller.example",
		Verify: func(_ context.Context, s Session) error {
			verified++
			if s.(*rpcClient).profile.Origin != "https://caller.example" {
				t.Fatal("routed session lost caller's origin")
			}
			return nil
		},
		Whoami: func(context.Context, Session) (Identity, error) {
			return Identity{AccountID: "reader"}, nil
		},
	}
	h, err := Connect(context.Background(), p, AcquireOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	h.Close()
	if !h.Routed || h.Profile.Name != DefaultProfile || verified != 1 {
		t.Fatalf("unexpected handle: routed=%v profile=%q verified=%d", h.Routed, h.Profile.Name, verified)
	}
	authErr := Authf(DefaultProfile, "session expired")
	p.Verify = func(context.Context, Session) error { return authErr }
	if _, err := Connect(context.Background(), p, AcquireOptions{}); !errors.Is(err, authErr) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("verification error was lost: %v", err)
	}
	h, err = Connect(context.Background(), p, AcquireOptions{SkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	if _, err := Connect(context.Background(), p, AcquireOptions{Headed: true}); err == nil {
		t.Fatal("headed acquisition must not route to a headless holder")
	}
}
