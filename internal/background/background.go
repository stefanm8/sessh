// Package background manages long-running commands without a terminal. A
// private control socket, rather than a saved PID, identifies a live worker.
package background

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Spec struct{ Dir, Key, Name string }
type Status struct {
	Name    string    `json:"name"`
	State   string    `json:"state"`
	Detail  string    `json:"detail,omitempty"`
	PID     int       `json:"pid,omitempty"`
	Started time.Time `json:"started,omitempty"`
	Log     string    `json:"log"`
	// Key and Args let another process find this worker and start it again
	// as it was; Detached tells a worker Start launched from one running in
	// someone's terminal.
	Key      string   `json:"key,omitempty"`
	Args     []string `json:"args,omitempty"`
	Detached bool     `json:"detached,omitempty"`
}

func (s Spec) path(ext string) string {
	sum := sha256.Sum256([]byte(s.Key))
	return filepath.Join(s.Dir, fmt.Sprintf("service-%x.%s", sum[:8], ext))
}
func (s Spec) LogPath() string { return s.path("log") }

func lock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s Spec) request(ctx context.Context, method string) (Status, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", s.path("sock"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost/", nil)
	if err != nil {
		return Status{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return Status{}, err
	}
	defer res.Body.Close()
	var st Status
	err = json.NewDecoder(res.Body).Decode(&st)
	return st, err
}

// Inspect never launches a browser. A dead process cannot appear healthy just
// because its status file (or PID) is still present.
func (s Spec) Inspect(ctx context.Context) (Status, bool, error) {
	if st, err := s.request(ctx, http.MethodGet); err == nil {
		return st, true, nil
	}
	if err := ctx.Err(); err != nil {
		return Status{}, false, err
	}
	st := Status{Name: s.Name, State: "stopped", Log: s.LogPath()}
	b, err := os.ReadFile(s.path("json"))
	if os.IsNotExist(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, false, err
	}
	if st.State != "stopped" && st.State != "failed" {
		st.State, st.Detail = "unreachable", "Control socket is unavailable; check the logs."
	}
	return st, false, nil
}

// Run owns the worker lock until work and all its cleanup have finished.
// report publishes human-readable progress, including recovery after a crash.
func (s Spec) Run(ctx context.Context, work func(context.Context, func(string, string)) error) error {
	guard, err := lock(s.path("lock"))
	if err != nil {
		return fmt.Errorf("%s is already running or cannot be locked: %w", s.Name, err)
	}
	defer guard.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st := Status{Name: s.Name, State: "starting", PID: os.Getpid(), Started: time.Now(), Log: s.LogPath(),
		Key: s.Key, Args: os.Args[1:], Detached: detached()}
	var mu sync.Mutex
	save := func() error {
		b, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		tmp := s.path("json.tmp")
		if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, s.path("json"))
	}
	if err := save(); err != nil {
		return err
	}
	// Status must not stay "starting" when the worker cannot even get going.
	failed := func(err error) error {
		st.State, st.Detail = "failed", err.Error()
		return errors.Join(err, save())
	}
	// The held lock proves that any previous socket is stale.
	if err := os.Remove(s.path("sock")); err != nil && !os.IsNotExist(err) {
		return failed(err)
	}
	ln, err := net.Listen("unix", s.path("sock"))
	if err != nil {
		if path := s.path("sock"); len(path) >= 100 {
			err = fmt.Errorf("%w (the socket path is %d bytes; Unix sockets allow about 100, so use a shorter SESSH_HOME)", err, len(path))
		}
		return failed(err)
	}
	defer ln.Close()
	if err := os.Chmod(s.path("sock"), 0o600); err != nil {
		return failed(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			st.State, st.Detail = "stopping", "Waiting for cleanup."
			_ = save()
			cancel()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})}
	defer srv.Close()
	go func() { _ = srv.Serve(ln) }()
	report := func(state, detail string) {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil || (st.State == state && st.Detail == detail) {
			return
		}
		st.State, st.Detail = state, detail
		_ = save()
	}
	err = work(ctx, report)
	mu.Lock()
	st.State, st.Detail = "stopped", ""
	if err != nil && !errors.Is(err, context.Canceled) {
		st.State, st.Detail = "failed", err.Error()
	}
	saveErr := save()
	mu.Unlock()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return errors.Join(err, saveErr)
}

// Start re-execs the same binary in a new session with no terminal handles.
// The short startup wait confirms the control server, not browser readiness;
// Inspect reports startup progress and any subsequent authentication failure.
func (s Spec) Start(ctx context.Context, args []string) (Status, bool, error) {
	guard, err := lock(s.path("start-lock"))
	if err != nil {
		return Status{}, false, fmt.Errorf("another start is in progress: %w", err)
	}
	defer guard.Close()
	if st, live, err := s.Inspect(ctx); err != nil || live {
		return st, live, err
	}
	// Do not replace logs or spawn a duplicate if a worker is still cleaning up.
	worker, err := lock(s.path("lock"))
	if err != nil {
		return Status{}, false, fmt.Errorf("%s is busy; retry status shortly", s.Name)
	}
	worker.Close()
	executable, err := os.Executable()
	if err != nil {
		return Status{}, false, err
	}
	log, err := os.OpenFile(s.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Status{}, false, err
	}
	defer log.Close()
	child := exec.Command(executable, args...)
	child.Stdin, child.Stdout, child.Stderr = nil, log, log
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return Status{}, false, err
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return Status{}, false, fmt.Errorf("start interrupted; check status (the worker may be running): %w", ctx.Err())
		case err := <-exited:
			st, _, _ := s.Inspect(ctx)
			return st, false, fmt.Errorf("%s exited during startup (%v); see %s", s.Name, err, s.LogPath())
		case <-timeout.C:
			return Status{}, false, fmt.Errorf("%s is still starting; check status and %s", s.Name, s.LogPath())
		case <-ticker.C:
			if st, err := s.request(ctx, http.MethodGet); err == nil {
				return st, false, nil
			}
		}
	}
}

// detached reports whether this process leads its own session, as the
// workers Start launches do and commands run from a terminal do not.
func detached() bool {
	sid, err := syscall.Getsid(0)
	return err == nil && sid == os.Getpid()
}

// Find returns the live worker in dir whose process is pid.
func Find(ctx context.Context, dir string, pid int) (Spec, Status, bool) {
	files, _ := filepath.Glob(filepath.Join(dir, "service-*.json"))
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var saved Status
		if json.Unmarshal(b, &saved) != nil || saved.PID != pid || saved.Key == "" {
			continue
		}
		spec := Spec{Dir: dir, Key: saved.Key, Name: saved.Name}
		// Only the control socket proves the process is still that worker.
		if st, live, err := spec.Inspect(ctx); err == nil && live && st.PID == pid {
			return spec, st, true
		}
	}
	return Spec{}, Status{}, false
}

func (s Spec) Stop(ctx context.Context) (bool, error) {
	if _, live, err := s.Inspect(ctx); err != nil || !live {
		if err != nil {
			return false, err
		}
		guard, err := lock(s.path("lock"))
		if err != nil {
			return false, fmt.Errorf("%s is locked but its control socket is unavailable; check the logs", s.Name)
		}
		guard.Close()
		return false, nil
	}
	if _, err := s.request(ctx, http.MethodPost); err != nil {
		return false, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, fmt.Errorf("stop requested; cleanup is still pending: %w", ctx.Err())
		case <-ticker.C:
			// Completion means cleanup released the lock, not merely that the socket
			// stopped answering. No signal is ever sent to a potentially reused PID.
			f, err := lock(s.path("lock"))
			if err == nil {
				f.Close()
				return true, nil
			}
		}
	}
}

// Logs prints a bounded tail and optionally follows new output until cancelled.
func (s Spec) Logs(ctx context.Context, out io.Writer, follow bool) error {
	f, err := os.Open(s.LogPath())
	if os.IsNotExist(err) {
		_, err = fmt.Fprintln(out, "No background log yet. Foreground output stays in its terminal.")
		return err
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 64*1024 {
		_, err = f.Seek(-64*1024, io.SeekEnd)
		if err != nil {
			return err
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := io.Copy(out, f); err != nil {
			return err
		}
		if !follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		info, err := f.Stat()
		if err != nil {
			return err
		}
		pos, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if info.Size() < pos {
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
		}
	}
}
