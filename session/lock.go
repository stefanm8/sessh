package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A Chromium profile is single-process. Rather than let two commands race into
// the same SingletonLock and one of them die (the failure this whole design
// exists to remove), ownership of a profile directory is decided
// by an advisory OS lock: whoever gets it launches the browser and serves a
// socket; everyone else routes through that socket.

type profileLock struct {
	f    *os.File
	path string
}

func lockPath(name string) string { return filepath.Join(ProfileDir(name), ".sessh-lock") }

// acquireLock takes the profile lock without blocking. A failure to acquire is
// reported as *ErrBusy, carrying the holder's description when we can read it.
func acquireLock(name string) (*profileLock, error) {
	if err := os.MkdirAll(ProfileDir(name), 0o700); err != nil {
		return nil, err
	}
	path := lockPath(name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, pid := readHolder(path)
		_ = f.Close()
		return nil, &ErrBusy{Profile: name, Holder: holder, PID: pid}
	}
	// Record who we are, for a friendlier "busy" message elsewhere.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d %s\n", os.Getpid(), strings.Join(os.Args, " "))), 0)
	return &profileLock{f: f, path: path}, nil
}

func (l *profileLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

func readHolder(path string) (string, int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	line := strings.TrimSpace(string(b))
	if line == "" {
		return "", 0
	}
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 2 {
		if pid, err := strconv.Atoi(parts[0]); err == nil {
			return fmt.Sprintf("pid %d: %s", pid, parts[1]), pid
		}
	}
	return line, 0
}

// Holder describes the process currently holding a profile, or "" when it is
// free. It probes without blocking, so it is safe to call for display.
func Holder(name string) string {
	holder, _ := holding(name)
	return holder
}

// HolderPID is the process holding a profile, or 0 when it is free.
func HolderPID(name string) int {
	_, pid := holding(name)
	return pid
}

func holding(name string) (string, int) {
	path := lockPath(name)
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return readHolder(path)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return "", 0
}

// Interactive logins are serialized per profile by a second lock, so commands
// that find the session lapsed at the same time open one browser window
// between them instead of one each.

func loginLockPath(name string) string { return filepath.Join(RunDir(), name+".login.lock") }

// LockLogin takes the profile's login lock, polling until it is free or ctx
// ends. waiting is called once if another process holds it; waited reports
// whether that happened, because the other login may already have signed the
// profile in.
func LockLogin(ctx context.Context, name string, waiting func()) (release func(), waited bool, err error) {
	if err := os.MkdirAll(RunDir(), 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(loginLockPath(name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, waited, nil
		}
		if !waited && waiting != nil {
			waiting()
		}
		waited = true
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, waited, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// LoggingIn reports whether any process is running an interactive login for
// the profile. It probes without blocking.
func LoggingIn(name string) bool {
	f, err := os.OpenFile(loginLockPath(name), os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
