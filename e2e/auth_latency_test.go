//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestUnauthenticatedCommandsFailFast(t *testing.T) {
	for _, product := range []struct {
		name string
		args []string
	}{
		{"jira", []string{"jira", "project", "list"}},
		{"confluence", []string{"confluence", "search", "devops"}},
	} {
		t.Run(product.name, func(t *testing.T) {
			for _, keepAlive := range []bool{false, true} {
				name := "no_keep_alive"
				if keepAlive {
					name = "automatic_keep_alive"
				}
				t.Run(name, func(t *testing.T) {
					tn := newTenant(t, product.name, "127.0.0.1")
					var e *env
					if product.name == "jira" {
						e = newEnv(t, tn, nil)
					} else {
						e = newEnv(t, nil, tn)
					}
					args := append(append([]string{}, product.args...), "--no-login")
					if !keepAlive {
						args = append(args, "--no-keep-alive")
					}
					check := func() {
						t.Helper()
						started := time.Now()
						c := e.start(args...)
						// Includes Chromium startup and RPC routing, but remains
						// shorter than the old 20-second silent SSO polling loop.
						c.mustExit(10 * time.Second)
						elapsed := time.Since(started)
						c.expect(1, "", "sessh auth login "+product.name)
						if got := strings.TrimSpace(c.stdout.String()); got != "" {
							t.Fatalf("unauthenticated command emitted output: %s", got)
						}
						if n := tn.headedLogins(); n != 0 {
							t.Fatalf("--no-login opened %d login window(s)", n)
						}
						t.Logf("sessh %s returned the auth hint in %s", strings.Join(args, " "), elapsed)
					}
					check()
					if keepAlive {
						pid := e.warmPID("atlassian")
						check()
						if got := e.warmPID("atlassian"); got != pid {
							t.Fatalf("signed-out warm command restarted the daemon: %d -> %d", pid, got)
						}
					} else if st, live := e.status("atlassian"); live {
						t.Fatalf("--no-keep-alive left a live worker: %+v", st)
					}
				})
			}
		})
	}
}
