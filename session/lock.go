package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
		holder := readHolder(path)
		_ = f.Close()
		return nil, &ErrBusy{Profile: name, Holder: holder}
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

func readHolder(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	if line == "" {
		return ""
	}
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 2 {
		if pid, err := strconv.Atoi(parts[0]); err == nil {
			return fmt.Sprintf("pid %d: %s", pid, parts[1])
		}
	}
	return line
}

// Holder describes the process currently holding a profile, or "" when it is
// free. It probes without blocking, so it is safe to call for display.
func Holder(name string) string {
	path := lockPath(name)
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return ""
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return readHolder(path)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return ""
}
