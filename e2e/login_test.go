//go:build e2e

// Package e2e drives the sessh binary and a real Chromium against a fake
// Atlassian-style tenant, to check the login flow a lapsed session starts.
//
// Run it with `make e2e`. It needs the browser setup (scripts/setup-browser.sh)
// and a display: login windows open on screen and close by themselves.
package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sessh-e2e-bin-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "sessh")
	build := exec.Command("go", "build", "-o", binary, "github.com/stefanm8/sessh/cmd/sessh")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is one isolated sessh home configured for the given tenants.
type env struct {
	t    *testing.T
	home string
	vars []string
}

func newEnv(t *testing.T, jira, confluence *tenant) *env {
	t.Helper()
	// Short on purpose: Unix socket paths under it must stay below ~100 bytes.
	home, err := os.MkdirTemp("", "se2e")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, home: home}
	e.vars = append(os.Environ(), "SESSH_HOME="+home, "SESSH_PROFILE=", "SESSH_KEEP_ALIVE=", "BITBUCKET_WORKSPACE=", "JIRA_SITE=", "CONFLUENCE_SITE=")
	if jira != nil {
		e.vars = append(e.vars, "JIRA_SITE="+jira.URL())
	}
	if confluence != nil {
		e.vars = append(e.vars, "CONFLUENCE_SITE="+confluence.URL())
	}
	t.Cleanup(func() {
		e.stopWorkers()
		os.RemoveAll(home)
	})
	return e
}

type buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// command is a running sessh process.
type command struct {
	t              *testing.T
	args           []string
	cmd            *exec.Cmd
	stdout, stderr buffer
	done           chan struct{}
	code           int
}

func (e *env) start(args ...string) *command {
	c := &command{t: e.t, args: args, done: make(chan struct{})}
	c.cmd = exec.Command(binary, args...)
	c.cmd.Env = e.vars
	c.cmd.Stdout, c.cmd.Stderr = &c.stdout, &c.stderr
	if err := c.cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	go func() {
		err := c.cmd.Wait()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			c.code = exit.ExitCode()
		} else if err != nil {
			c.code = -1
		}
		close(c.done)
	}()
	e.t.Cleanup(func() {
		if c.running() {
			_ = c.cmd.Process.Kill()
			<-c.done
		}
	})
	return c
}

func (e *env) run(args ...string) *command {
	c := e.start(args...)
	c.mustExit(3 * time.Minute)
	return c
}

func (c *command) running() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// wait returns the exit code, or -2 if the command is still running.
func (c *command) wait(timeout time.Duration) int {
	select {
	case <-c.done:
		return c.code
	case <-time.After(timeout):
		return -2
	}
}

func (c *command) mustExit(timeout time.Duration) int {
	c.t.Helper()
	code := c.wait(timeout)
	if code == -2 {
		c.t.Fatalf("sessh %s still running after %s\nstderr:\n%s", strings.Join(c.args, " "), timeout, c.stderr.String())
	}
	return c.code
}

func (c *command) expect(code int, stdout, stderr string) {
	c.t.Helper()
	got := c.mustExit(time.Minute)
	if got != code || !strings.Contains(c.stdout.String(), stdout) || !strings.Contains(c.stderr.String(), stderr) {
		c.t.Fatalf("sessh %s: exit %d, want %d with stdout containing %q and stderr %q\nstdout:\n%s\nstderr:\n%s",
			strings.Join(c.args, " "), got, code, stdout, stderr, c.stdout.String(), c.stderr.String())
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting until %s", timeout, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestLapsedSession(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)

	t.Run("a command without a session waits in a login window, then finishes", func(t *testing.T) {
		jira.expect("ok")
		c := e.start("jira", "project", "list", "--json")
		waitFor(t, "a login window opens", 90*time.Second, func() bool { return jira.headedLogins() == 1 })
		// Longer than a Verify poll: the page must not be reloaded under the user.
		time.Sleep(30 * time.Second)
		if !c.running() || c.stdout.String() != "" || jira.headedLogins() != 1 {
			t.Fatalf("while signing in: running %v, stdout %q, %d login page loads; want one window and nothing printed",
				c.running(), c.stdout.String(), jira.headedLogins())
		}
		jira.approve()
		if c.wait(time.Minute) == -2 {
			jira.mu.Lock()
			approved, polls, sessions, myself := jira.approved, jira.polls, len(jira.sessions), jira.myself
			jira.mu.Unlock()
			t.Fatalf("approved login did not finish: approved=%v polls=%d sessions=%d identity probes=%d\nstderr:\n%s", approved, polls, sessions, myself, c.stderr.String())
		}
		c.expect(0, `"key": "DEMO"`, "Logged in as E2E User")
		var projects []map[string]any
		if err := json.Unmarshal([]byte(c.stdout.String()), &projects); err != nil {
			t.Fatalf("stdout is not the command's JSON alone: %v\n%s", err, c.stdout.String())
		}
	})

	t.Run("a request refused after Verify passed logs in and retries", func(t *testing.T) {
		jira.expect("revoke-once")
		c := e.start("jira", "project", "list")
		waitFor(t, "a login window opens", 60*time.Second, func() bool { return jira.headedLogins() == 1 })
		if !c.running() {
			t.Fatalf("exited before the login finished:\n%s", c.stderr.String())
		}
		jira.approve()
		c.expect(0, "DEMO", "The command continues once you are signed in.")
	})

	t.Run("a 403 while signed in is reported without a window", func(t *testing.T) {
		jira.expect("forbid")
		e.run("jira", "project", "list").expect(1, "", "access denied (HTTP 403)")
		if n := jira.headedLogins(); n != 0 {
			t.Fatalf("%d login window(s) opened for a permission error", n)
		}
	})
}

func TestNoLogin(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	e.run("jira", "project", "list", "--no-login").expect(1, "", "sessh auth login jira")
	if n := jira.headedLogins(); n != 0 {
		t.Fatalf("--no-login opened %d login window(s)", n)
	}
}

func TestConcurrentCommandsShareOneLogin(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	a := e.start("jira", "project", "list")
	b := e.start("jira", "project", "list", "--json")
	waitFor(t, "a login window opens", 90*time.Second, func() bool { return jira.headedLogins() >= 1 })
	time.Sleep(5 * time.Second)
	if n := jira.headedLogins(); n != 1 || !a.running() || !b.running() {
		t.Fatalf("%d login windows; running %v %v; want one window and both waiting\nfirst stderr:\n%s\nsecond stderr:\n%s", n, a.running(), b.running(), a.stderr.String(), b.stderr.String())
	}
	jira.approve()
	a.expect(0, "DEMO", "")
	b.expect(0, `"key": "DEMO"`, "")
}

func TestLoginEnds(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)

	t.Run("a login times out", func(t *testing.T) {
		e.run("auth", "login", "jira", "--timeout", "5s").expect(1, "", "timed out waiting for login")
	})

	t.Run("Ctrl+C cancels a pending login and frees the profile", func(t *testing.T) {
		c := e.start("jira", "project", "list")
		waitFor(t, "the command waits for the login", 90*time.Second, func() bool {
			return strings.Contains(c.stderr.String(), "Waiting for authentication")
		})
		if err := c.cmd.Process.Signal(syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		if code := c.wait(15 * time.Second); code != 1 || !strings.Contains(c.stderr.String(), "login cancelled") {
			t.Fatalf("after Ctrl+C: exit %d\n%s", code, c.stderr.String())
		}
		// The profile is free again: a check does not find it busy.
		e.run("jira", "project", "list", "--no-login").expect(1, "", "no Jira session")
	})
}

func TestDaemon(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	confluence := newTenant(t, "confluence", "localhost")
	e := newEnv(t, jira, confluence)
	e.run("daemon", "start", "jira", "--idle", "45m").expect(0, "Started in the background", "")
	waitFor(t, "the daemon holds the profile", 30*time.Second, func() bool {
		return strings.Contains(e.run("daemon", "status", "jira").stdout.String(), ": running")
	})

	t.Run("a login pauses the daemon holding the profile and restarts it", func(t *testing.T) {
		jira.expect("ok")
		c := e.start("jira", "project", "list")
		waitFor(t, "a login window opens", 90*time.Second, func() bool { return jira.headedLogins() == 1 })
		if st := e.run("daemon", "status", "jira").stdout.String(); !strings.Contains(st, ": stopped") {
			t.Fatalf("daemon during the login:\n%s", st)
		}
		jira.approve()
		c.expect(0, "DEMO", "Restarted daemon (profile atlassian).")
		st := e.run("daemon", "status", "jira").stdout.String()
		if !strings.Contains(st, ": running") {
			t.Fatalf("daemon after the login:\n%s", st)
		}
	})

	t.Run("another plugin routed through the daemon uses its own origin", func(t *testing.T) {
		// Confluence lives on another host than the Jira site the daemon
		// was started for; signing in there also goes through the daemon.
		confluence.expect("ok")
		confluence.approve()
		e.run("confluence", "space", "list").expect(0, "DOCS", "Restarted daemon")
		e.run("confluence", "space", "list", "--verbose", "--no-login").expect(0, "DOCS", "routed through the process holding profile")
	})
}
