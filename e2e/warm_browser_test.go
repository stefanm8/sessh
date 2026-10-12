//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stefanm8/sessh/internal/background"
)

// seedSession installs only synthetic fake-tenant credentials, so the warm
// lifecycle tests do not need login windows or read a user's real profile.
func (e *env) seedSession(tn *tenant, profile string) {
	e.t.Helper()
	token := "synthetic-" + profile
	tn.mu.Lock()
	tn.sessions[token] = true
	tn.mu.Unlock()
	seed, err := json.Marshal(map[string]any{"cookies": []map[string]any{{
		"name": sessionCookie, "value": token, "domain": tn.host, "path": "/",
		"expires": time.Now().Add(time.Hour).Unix(), "httpOnly": true, "secure": false, "sameSite": "Lax",
	}}, "origins": []any{}})
	if err != nil {
		e.t.Fatal(err)
	}
	dir := filepath.Join(e.home, "profiles", profile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seed.json"), seed, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) worker(profile string) background.Spec {
	return background.Spec{Dir: filepath.Join(e.home, "run"), Key: "daemon:" + profile, Name: "daemon (profile " + profile + ")"}
}

func (e *env) status(profile string) (background.Status, bool) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, live, err := e.worker(profile).Inspect(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return st, live
}

func (e *env) warmPID(profile string) int {
	e.t.Helper()
	var st background.Status
	waitFor(e.t, "the automatic daemon is running for "+profile, 15*time.Second, func() bool {
		var live bool
		st, live = e.status(profile)
		return live && st.State == "running"
	})
	if !st.Detached || st.PID == 0 {
		e.t.Fatalf("expected a detached worker: %+v", st)
	}
	return st.PID
}

// All workers are per-test and rooted beneath e.home, including custom profile
// workers. Stop them before deleting their sockets and the temporary binary.
func (e *env) stopWorkers() {
	files, _ := filepath.Glob(filepath.Join(e.home, "run", "service-*.json"))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var st background.Status
		if json.Unmarshal(data, &st) != nil || st.Key == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		spec := background.Spec{Dir: filepath.Join(e.home, "run"), Key: st.Key, Name: st.Name}
		_, err = spec.Stop(ctx)
		cancel()
		if err != nil {
			e.t.Errorf("cleanup worker %s: %v", st.Name, err)
		}
	}
}

func TestAutomaticWarmBrowser(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	e.seedSession(jira, "atlassian")

	started := time.Now()
	cold := e.run("jira", "project", "list", "--json", "--no-login")
	coldDuration := time.Since(started)
	cold.expect(0, `"key": "DEMO"`, "")
	if cold.stderr.String() != "" {
		t.Fatalf("automatic startup leaked bookkeeping into stderr: %s", cold.stderr.String())
	}
	pid := e.warmPID("atlassian")
	if pid == cold.cmd.Process.Pid {
		t.Fatal("the command retained ownership instead of leaving a background browser")
	}

	var warmDuration time.Duration
	for range 3 {
		identityBefore := jira.identityRequests()
		started = time.Now()
		c := e.run("jira", "project", "list", "--json", "--no-login")
		warmDuration += time.Since(started)
		c.expect(0, `"key": "DEMO"`, "")
		if c.stderr.String() != "" {
			t.Fatalf("warm command emitted unexpected stderr: %s", c.stderr.String())
		}
		if got := e.warmPID("atlassian"); got != pid {
			t.Fatalf("sequential command relaunched the daemon: pid %d -> %d", pid, got)
		}
		if probes := jira.identityRequests() - identityBefore; probes != 1 {
			t.Fatalf("warm command made %d identity requests; want one fresh authentication check", probes)
		}
	}
	t.Logf("cold command %s; warm mean %s; retained daemon pid %d", coldDuration, warmDuration/3, pid)

	e.run("daemon", "stop", "jira").expect(0, "Stopped", "")
	if st, live := e.status("atlassian"); live || st.State != "stopped" {
		t.Fatalf("explicit stop did not finish browser cleanup: %+v, live %v", st, live)
	}
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	if got := e.warmPID("atlassian"); got == pid {
		t.Fatalf("a stopped daemon was reused (pid %d)", pid)
	}
	crashedPID := e.warmPID("atlassian")
	if err := syscall.Kill(crashedPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the crashed worker is unreachable", 5*time.Second, func() bool {
		_, live := e.status("atlassian")
		return !live
	})
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	if got := e.warmPID("atlassian"); got == crashedPID {
		t.Fatalf("the crashed worker was reused: pid %d", got)
	}
	e.run("auth", "logout", "jira").expect(0, "Logged out of profile atlassian", "")
	if st, live := e.status("atlassian"); live || st.State != "stopped" {
		t.Fatalf("logout left the warm daemon running: %+v, live %v", st, live)
	}
	if _, err := os.Stat(filepath.Join(e.home, "profiles", "atlassian")); !os.IsNotExist(err) {
		t.Fatalf("logout did not remove the warm browser profile: %v", err)
	}
}

func TestConcurrentAutomaticWarmStart(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	e.seedSession(jira, "atlassian")
	commands := make([]*command, 6)
	for i := range commands {
		commands[i] = e.start("jira", "project", "list", "--json", "--no-login")
	}
	for _, c := range commands {
		c.expect(0, `"key": "DEMO"`, "")
	}
	pid := e.warmPID("atlassian")
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	if got := e.warmPID("atlassian"); got != pid {
		t.Fatalf("concurrent starts did not converge on one worker: %d -> %d", pid, got)
	}
	data, err := os.ReadFile(filepath.Join(e.home, "profiles", "atlassian", ".sessh-lock"))
	if err != nil || !strings.HasPrefix(string(data), fmt.Sprint(pid)+" ") {
		t.Fatalf("warm daemon does not own the profile: %q, %v", data, err)
	}
}

func TestAutomaticWarmIdleExpiry(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	e.seedSession(jira, "atlassian")
	e.run("jira", "project", "list", "--keep-alive", "2s", "--no-login").expect(0, "DEMO", "")
	pid := e.warmPID("atlassian")
	conn, err := net.DialTimeout("unix", filepath.Join(e.home, "run", "atlassian.sock"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"id": 1, "method": "do", "origin": jira.URL(), "params": map[string]string{
		"method": "GET", "path": jira.URL() + "/rest/api/3/myself",
	}}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&response); err != nil || response.Error != "" {
		t.Fatalf("holding an active client: %v, %s", err, response.Error)
	}
	// A command can be doing local work between API calls. Its browser must
	// stay alive until it releases the connection, even beyond the idle limit.
	time.Sleep(3 * time.Second)
	if st, live := e.status("atlassian"); !live || st.PID != pid || st.State != "running" {
		t.Fatalf("idle expiry interrupted a connected command: %+v, live %v", st, live)
	}
	conn.Close()
	// Status polling must not count as activity and postpone the idle timeout.
	waitFor(t, "the idle automatic daemon stops", 15*time.Second, func() bool {
		st, live := e.status("atlassian")
		return !live && st.State == "stopped"
	})
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	if got := e.warmPID("atlassian"); got == pid {
		t.Fatalf("idle daemon was not restarted: pid %d", pid)
	}
}

func TestAutomaticWarmOptOutAndProfiles(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	e.seedSession(jira, "atlassian")
	e.run("jira", "project", "list", "--no-keep-alive", "--no-login").expect(0, "DEMO", "")
	if st, live := e.status("atlassian"); live {
		t.Fatalf("opt-out left a live worker: %+v", st)
	}
	e.vars = append(e.vars, "SESSH_KEEP_ALIVE=off")
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	if st, live := e.status("atlassian"); live {
		t.Fatalf("environment opt-out left a live worker: %+v", st)
	}
	e.vars = append(e.vars, "SESSH_KEEP_ALIVE=")
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	defaultPID := e.warmPID("atlassian")
	e.seedSession(jira, "alternate")
	e.run("jira", "project", "list", "--profile", "alternate", "--no-login").expect(0, "DEMO", "")
	if got := e.warmPID("alternate"); got == defaultPID {
		t.Fatalf("custom profile reused the other profile's worker: %d", got)
	}
	e.run("jira", "project", "list", "--profile", "inherited", "--from", "atlassian", "--no-login").expect(0, "DEMO", "")
	if got := e.warmPID("inherited"); got == defaultPID {
		t.Fatalf("inherited profile did not get its own browser: %d", got)
	}
	// No-inherit must remain empty even while other authenticated profiles are
	// warm; this command must not silently borrow their credentials.
	e.run("jira", "project", "list", "--profile", "empty", "--from", "atlassian", "--no-inherit", "--no-login").expect(1, "", "no Jira session")
	if got := e.warmPID("atlassian"); got != defaultPID {
		t.Fatalf("another profile disturbed the original daemon: %d -> %d", defaultPID, got)
	}
}

func TestWarmTabsReuseAndOriginIsolation(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	e.seedSession(jira, "atlassian")
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<!doctype html><title>Warm tab fixture</title>")
	})
	originA, originB := httptest.NewServer(page), httptest.NewServer(page)
	defer originA.Close()
	defer originB.Close()
	open := func() net.Conn {
		t.Helper()
		conn, err := net.DialTimeout("unix", filepath.Join(e.home, "run", "atlassian.sock"), 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	eval := func(conn net.Conn, origin, js string) map[string]any {
		t.Helper()
		if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
			t.Fatal(err)
		}
		request := map[string]any{"id": 1, "method": "eval", "origin": origin, "params": map[string]string{"js": js}}
		if err := json.NewEncoder(conn).Encode(request); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Result map[string]any `json:"result"`
			Error  string         `json:"error"`
		}
		if err := json.NewDecoder(conn).Decode(&response); err != nil || response.Error != "" {
			t.Fatalf("eval at %s failed: %v, %s", origin, err, response.Error)
		}
		return response.Result
	}
	first := open()
	got := eval(first, originA.URL, `() => { window.__sesshWarmMarker = "first-client"; return {origin: location.origin}; }`)
	if got["origin"] != originA.URL {
		t.Fatalf("first Eval used holder origin instead of caller: %+v", got)
	}
	first.Close()
	// Give the server's EOF handler a moment to return the exclusive page lease.
	time.Sleep(150 * time.Millisecond)
	second := open()
	got = eval(second, originA.URL, `() => ({origin: location.origin, marker: window.__sesshWarmMarker})`)
	if got["origin"] != originA.URL || got["marker"] != "first-client" {
		t.Fatalf("sequential commands did not reuse their open tab: %+v", got)
	}
	other := open()
	got = eval(other, originB.URL, `() => ({origin: location.origin, marker: window.__sesshWarmMarker || "empty"})`)
	if got["origin"] != originB.URL || got["marker"] != "empty" {
		t.Fatalf("a different origin borrowed the previous origin's tab: %+v", got)
	}
	concurrent := open()
	got = eval(concurrent, originA.URL, `() => { window.__sesshWarmMarker = "concurrent-client"; return {origin: location.origin}; }`)
	if got["origin"] != originA.URL {
		t.Fatalf("concurrent client used the wrong origin: %+v", got)
	}
	got = eval(second, originA.URL, `() => ({marker: window.__sesshWarmMarker})`)
	if got["marker"] != "first-client" {
		t.Fatalf("simultaneous clients shared one page: %+v", got)
	}
}

func TestImportAppliesSeedAfterWarmSession(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	e.seedSession(jira, "source")
	// Materialize and cleanly close a Chromium profile carrying a different
	// synthetic session so auth import uses the actual source browser storage.
	e.run("jira", "project", "list", "--profile", "source", "--no-keep-alive", "--no-login").expect(0, "DEMO", "")
	e.seedSession(jira, "atlassian")
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	previousPID := e.warmPID("atlassian")
	// Only the incoming source credential will remain valid. Reusing the
	// previous warm context after import would fail the next command.
	jira.mu.Lock()
	jira.sessions["synthetic-atlassian"] = false
	jira.mu.Unlock()
	e.run("auth", "import", filepath.Join(e.home, "profiles", "source"), "--into", "atlassian").expect(0, "Imported the session", "")
	if st, live := e.status("atlassian"); live {
		t.Fatalf("import left the previous warm browser running: %+v", st)
	}
	e.run("jira", "project", "list", "--no-login").expect(0, "DEMO", "")
	if got := e.warmPID("atlassian"); got == previousPID {
		t.Fatalf("import reused the previous browser pid %d", got)
	}
}
