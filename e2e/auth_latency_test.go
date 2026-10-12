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

// Each service retains its own auth fixture; command timing and daemon checks
// share one harness so every plugin is held to the same fail-fast contract.
func authFixture(t *testing.T, product string) (*env, func() int) {
	t.Helper()
	if product == "jira" || product == "confluence" {
		tn := newTenant(t, product, "127.0.0.1")
		if product == "jira" {
			return newEnv(t, tn, nil), tn.headedLogins
		}
		return newEnv(t, nil, tn), tn.headedLogins
	}
	e := newEnv(t, nil, nil)
	var headed atomic.Int32
	if product == "teams" {
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
		// An empty cookie jar rejects Bitbucket without contacting its service.
		e.vars = append(e.vars, "BITBUCKET_WORKSPACE=auth-latency-fixture")
	}
	return e, func() int { return int(headed.Load()) }
}

func TestUnauthenticatedCommandsFailFast(t *testing.T) {
	for _, product := range []struct {
		name string
		args []string
	}{
		{"jira", []string{"jira", "project", "list"}},
		{"confluence", []string{"confluence", "search", "devops"}},
		{"bitbucket", []string{"bitbucket", "repo", "list"}},
		{"teams", []string{"teams", "capture", "--once", "--no-scroll"}},
	} {
		t.Run(product.name, func(t *testing.T) {
			for _, warm := range []bool{false, true} {
				name := "cold_browser"
				if warm {
					name = "warm_browser"
				}
				t.Run(name, func(t *testing.T) {
					e, headed := authFixture(t, product.name)
					args := append(append([]string{}, product.args...), "--no-login")
					profile := "atlassian"
					shared := product.name == "bitbucket" || product.name == "teams"
					if shared {
						profile = "global-auth"
						args = append(args, "--profile", profile, "--no-inherit")
						if warm {
							c := e.start("daemon", "start", product.name, "--profile", profile, "--no-inherit", "--no-login")
							c.mustExit(10 * time.Second)
							c.expect(0, "Started in the background", "")
						}
					}
					if !warm || shared {
						args = append(args, "--no-keep-alive")
					}
					check := func() {
						t.Helper()
						started := time.Now()
						c := e.start(args...)
						// Includes Chromium startup and RPC routing; shorter than
						// the former 20-second silent SSO polling loop.
						c.mustExit(10 * time.Second)
						c.expect(1, "", "sessh auth login "+product.name)
						// Capture logs startup; API commands must emit no data.
						if got := strings.TrimSpace(c.stdout.String()); product.name != "teams" && got != "" {
							t.Fatalf("unauthenticated output: %s", got)
						}
						if n := headed(); n != 0 {
							t.Fatalf("--no-login opened %d login window(s)", n)
						}
						t.Logf("sessh %s returned the auth hint in %s", strings.Join(args, " "), time.Since(started))
					}
					pid := 0
					if warm && shared {
						pid = e.warmPID(profile)
					}
					check()
					if warm {
						if !shared {
							pid = e.warmPID(profile)
						}
						check()
						if got := e.warmPID(profile); got != pid {
							t.Fatalf("signed-out command restarted its daemon: %d -> %d", pid, got)
						}
					} else if st, live := e.status(profile); live {
						t.Fatalf("cold command left a live worker: %+v", st)
					}
				})
			}
		})
	}
}
