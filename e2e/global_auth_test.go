//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Cover the cookie-only and browser-storage plugins as well as the
// account-based plugins in auth_latency_test.go. All profiles are isolated;
// Bitbucket rejects an empty cookie jar without contacting its real service.
func TestGlobalUnauthenticatedCommandsFailFast(t *testing.T) {
	for _, product := range []struct {
		name    string
		command []string
	}{
		{"bitbucket", []string{"bitbucket", "repo", "list"}},
		{"teams", []string{"teams", "capture", "--once", "--no-scroll"}},
	} {
		t.Run(product.name, func(t *testing.T) {
			for _, warm := range []bool{false, true} {
				name := "cold_browser"
				if warm {
					name = "shared_daemon"
				}
				t.Run(name, func(t *testing.T) {
					e := newEnv(t, nil, nil)
					var headed atomic.Int32
					if product.name == "teams" {
						srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if !strings.Contains(r.UserAgent(), "HeadlessChrome") {
								headed.Add(1)
							}
							if r.URL.Path != "/login" {
								http.Redirect(w, r, "/login", http.StatusFound)
								return
							}
							w.Header().Set("Content-Type", "text/html")
							fmt.Fprint(w, `<!doctype html><title>Sign in</title><h1>Sign in</h1>`)
						}))
						t.Cleanup(srv.Close)
						e.vars = append(e.vars, "TEAMS_URL="+srv.URL,
							"TEAMS_DATA_DIR=", "TEAMS_MESSAGES_FILE=", "TEAMS_CONVERSATIONS_FILE=")
					} else {
						e.vars = append(e.vars, "BITBUCKET_WORKSPACE=auth-latency-fixture")
					}
					profile := "global-auth"
					pid := 0
					if warm {
						started := e.start("daemon", "start", product.name, "--profile", profile, "--no-inherit", "--no-login")
						started.mustExit(10 * time.Second)
						started.expect(0, "Started in the background", "")
						pid = e.warmPID(profile)
					}
					args := append(append([]string{}, product.command...), "--profile", profile, "--no-inherit", "--no-login", "--no-keep-alive")
					check := func() {
						t.Helper()
						started := time.Now()
						c := e.start(args...)
						c.mustExit(10 * time.Second)
						c.expect(1, "", "sessh auth login "+product.name)
						// Capture deliberately logs its startup; API commands must
						// not emit result data after failed authentication.
						if got := strings.TrimSpace(c.stdout.String()); product.name == "bitbucket" && got != "" {
							t.Fatalf("unauthenticated command emitted output: %s", got)
						}
						if got := headed.Load(); got != 0 {
							t.Fatalf("--no-login loaded the fixture in a headed browser %d time(s)", got)
						}
						t.Logf("sessh %s returned its auth hint in %s", strings.Join(args, " "), time.Since(started))
					}
					check()
					if warm {
						check()
						if got := e.warmPID(profile); got != pid {
							t.Fatalf("signed-out command restarted its shared daemon: %d -> %d", pid, got)
						}
					} else if st, live := e.status(profile); live {
						t.Fatalf("cold command left a live worker: %+v", st)
					}
				})
			}
		})
	}
}
