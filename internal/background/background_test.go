package background

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testSpec(t *testing.T) Spec {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sessh-bg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return Spec{Dir: dir, Key: "capture:work", Name: "test capture"}
}

// Re-executed by Start so tests exercise real session detachment, descriptors,
// process exits, flock ownership, and socket cleanup without opening a browser.
func TestWorkerProcess(t *testing.T) {
	if os.Getenv("SESSH_TEST_WORKER") != "1" {
		return
	}
	s := Spec{Dir: os.Getenv("SESSH_TEST_DIR"), Key: "capture:work", Name: "test capture"}
	err := s.Run(context.Background(), func(ctx context.Context, report func(string, string)) error {
		if os.Getenv("SESSH_TEST_FAILURE") == "1" {
			return errors.New("test login expired")
		}
		fmt.Println("worker started")
		report("running", "test is ready")
		<-ctx.Done()
		fmt.Println("worker stopped")
		return nil
	})
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func waitState(t *testing.T, s Spec, state string) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, _, err := s.Inspect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.State == state {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("did not reach state %s", state)
	return Status{}
}

func TestDetachedLifecycle(t *testing.T) {
	s := testSpec(t)
	t.Setenv("SESSH_TEST_WORKER", "1")
	t.Setenv("SESSH_TEST_DIR", s.Dir)
	t.Setenv("SESSH_TEST_FAILURE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Cleanup(func() { _, _ = s.Stop(context.Background()) })
	st, existing, err := s.Start(ctx, []string{"-test.run=^TestWorkerProcess$"})
	if err != nil || existing || st.PID == os.Getpid() {
		t.Fatalf("start = %+v %v %v", st, existing, err)
	}
	waitState(t, s, "running")
	// A login that finds the worker holding its profile can pause and restart it.
	spec, found, ok := Find(ctx, s.Dir, st.PID)
	if !ok || spec != s || !found.Detached || !reflect.DeepEqual(found.Args, []string{"-test.run=^TestWorkerProcess$"}) {
		t.Fatalf("find = %+v %+v %v", spec, found, ok)
	}
	if _, _, ok := Find(ctx, s.Dir, os.Getpid()); ok {
		t.Fatal("found a worker for the wrong process")
	}
	// A hangup from the old terminal does not affect this separate process group.
	pgid, err := syscall.Getpgid(st.PID)
	if err != nil || pgid != st.PID {
		t.Fatalf("worker did not detach: pgid %d, err %v", pgid, err)
	}
	same, existing, err := s.Start(ctx, []string{"-test.run=^TestWorkerProcess$"})
	if err != nil || !existing || same.PID != st.PID {
		t.Fatalf("duplicate start = %+v %v %v", same, existing, err)
	}
	err = s.Run(ctx, func(context.Context, func(string, string)) error { t.Error("duplicate work ran"); return nil })
	if err == nil {
		t.Fatal("duplicate foreground capture accepted")
	}
	for _, ext := range []string{"json", "log", "sock", "lock"} {
		info, err := os.Stat(s.path(ext))
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("insecure %s: %v, %v", ext, info, err)
		}
	}
	var log bytes.Buffer
	if err := s.Logs(ctx, &log, false); err != nil || !strings.Contains(log.String(), "worker started") {
		t.Fatalf("logs: %s %v", log.String(), err)
	}
	if stopped, err := s.Stop(ctx); err != nil || !stopped {
		t.Fatalf("stop = %v %v", stopped, err)
	}
	waitState(t, s, "stopped")
	if _, _, ok := Find(ctx, s.Dir, st.PID); ok {
		t.Fatal("found a stopped worker")
	}
	if stopped, err := s.Stop(ctx); err != nil || stopped {
		t.Fatalf("second stop = %v %v", stopped, err)
	}
	// A previous socket/status file must not prevent restarting.
	if _, _, err := s.Start(ctx, []string{"-test.run=^TestWorkerProcess$"}); err != nil {
		t.Fatal(err)
	}
}

func TestFailureAndStaleProcess(t *testing.T) {
	s := testSpec(t)
	t.Setenv("SESSH_TEST_WORKER", "1")
	t.Setenv("SESSH_TEST_DIR", s.Dir)
	t.Setenv("SESSH_TEST_FAILURE", "1")
	_, _, _ = s.Start(context.Background(), []string{"-test.run=^TestWorkerProcess$"})
	st := waitState(t, s, "failed")
	if st.Detail != "test login expired" {
		t.Fatalf("lost startup failure: %+v", st)
	}
	t.Setenv("SESSH_TEST_FAILURE", "")
	st, _, err := s.Start(context.Background(), []string{"-test.run=^TestWorkerProcess$"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.Stop(context.Background()) })
	if err := syscall.Kill(st.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, "unreachable")
	if stopped, err := s.Stop(context.Background()); err != nil || stopped {
		t.Fatalf("stale stop = %v %v", stopped, err)
	}
	if _, _, err := s.Start(context.Background(), []string{"-test.run=^TestWorkerProcess$"}); err != nil {
		t.Fatal(err)
	}
}

func TestStartupFailureIsRecorded(t *testing.T) {
	s := testSpec(t)
	// A non-empty directory where the control socket belongs cannot be removed.
	if err := os.MkdirAll(filepath.Join(s.path("sock"), "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := s.Run(context.Background(), func(context.Context, func(string, string)) error {
		t.Error("work ran without a control socket")
		return nil
	})
	if err == nil {
		t.Fatal("run succeeded without a control socket")
	}
	if st, live, _ := s.Inspect(context.Background()); live || st.State != "failed" || st.Detail == "" {
		t.Fatalf("status = %+v, live %v; want the failure recorded", st, live)
	}
}
