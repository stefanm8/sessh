//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// tenant imitates what the jira or confluence plugin touches on an Atlassian
// Cloud site: pages that send anonymous visitors to a login form, and a REST
// API that refuses requests without a session cookie.
//
// The login form never completes by itself. It finishes only after approve,
// and only in a page a headed browser loaded, so a completed login proves a
// visible window was open and the (simulated) user signed in there.
type tenant struct {
	srv     *httptest.Server
	host    string
	product string

	mu       sync.Mutex
	approved bool
	sessions map[string]bool
	search   string // project search: ok, forbid, or revoke-once
	headed   int    // login form loads by a headed browser
	headless int
}

const sessionCookie = "tenant.session.token"

// newTenant serves product ("jira" or "confluence") on host, which should be
// 127.0.0.1 or localhost: two tenants on different hosts keep their cookies
// apart the way two real origins do.
func newTenant(t *testing.T, product, host string) *tenant {
	t.Helper()
	tn := &tenant{host: host, product: product, sessions: map[string]bool{}, search: "ok"}
	mux := http.NewServeMux()
	home := "/jira/"
	if product == "confluence" {
		home = "/wiki/"
		tn.confluence(mux)
	} else {
		tn.jira(mux)
	}
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		headed := !strings.Contains(r.UserAgent(), "HeadlessChrome")
		tn.mu.Lock()
		if headed {
			tn.headed++
		} else {
			tn.headless++
		}
		tn.mu.Unlock()
		cont := r.URL.Query().Get("continue")
		if !strings.HasPrefix(cont, "/") {
			cont = home
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><title>Sign in</title><h1>Sign in</h1><input id="email">
<script>
const headed = %t, cont = %q;
(async function poll() {
  try {
    const j = await (await fetch('/_e2e/approved', {cache: 'no-store'})).json();
    if (j.approved && headed) { location.href = '/login/complete?continue=' + encodeURIComponent(cont); return; }
  } catch (e) {}
  setTimeout(poll, 300);
})();
</script>`, headed, cont)
	})
	mux.HandleFunc("/login/complete", func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		token := hex.EncodeToString(b)
		tn.mu.Lock()
		tn.sessions[token] = true
		tn.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: 86400, HttpOnly: true})
		http.Redirect(w, r, r.URL.Query().Get("continue"), http.StatusFound)
	})
	mux.HandleFunc("/_e2e/approved", func(w http.ResponseWriter, r *http.Request) {
		tn.mu.Lock()
		defer tn.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"approved": tn.approved})
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tn.srv = &httptest.Server{Listener: ln, Config: &http.Server{Handler: mux}}
	tn.srv.Start()
	t.Cleanup(tn.srv.Close)
	return tn
}

func (tn *tenant) URL() string {
	return fmt.Sprintf("http://%s:%d", tn.host, tn.srv.Listener.Addr().(*net.TCPAddr).Port)
}

func (tn *tenant) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return tn.sessions[c.Value]
}

func (tn *tenant) jira(mux *http.ServeMux) {
	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		if !tn.valid(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"errorMessages": []string{"You are not authenticated."}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accountId": "e2e-account", "displayName": "E2E User"})
	})
	mux.HandleFunc("/rest/api/3/project/search", func(w http.ResponseWriter, r *http.Request) {
		if !tn.valid(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"errorMessages": []string{"You are not authenticated."}})
			return
		}
		tn.mu.Lock()
		mode := tn.search
		if mode == "revoke-once" {
			// Valid a moment ago, when Verify passed; revoked server-side now.
			tn.sessions, tn.search = map[string]bool{}, "ok"
		}
		tn.mu.Unlock()
		switch mode {
		case "forbid":
			writeJSON(w, http.StatusForbidden, map[string]any{"errorMessages": []string{"You do not have permission."}})
		case "revoke-once":
			writeJSON(w, http.StatusUnauthorized, map[string]any{"errorMessages": []string{"Session revoked."}})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"isLast": true, "values": []map[string]any{
				{"id": "10000", "key": "DEMO", "name": "Demo Project", "projectTypeKey": "software"},
			}})
		}
	})
	mux.HandleFunc("/jira/", tn.page("/jira/"))
}

func (tn *tenant) confluence(mux *http.ServeMux) {
	mux.HandleFunc("/wiki/rest/api/user/current", func(w http.ResponseWriter, r *http.Request) {
		if !tn.valid(r) {
			// Confluence answers an anonymous caller with 403, not 401.
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Not permitted"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"accountId": "e2e-account", "displayName": "E2E User"})
	})
	mux.HandleFunc("/wiki/api/v2/spaces", func(w http.ResponseWriter, r *http.Request) {
		if !tn.valid(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Unauthorized"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": []map[string]any{
			{"id": "98304", "key": "DOCS", "name": "Docs Space", "type": "global", "status": "current"},
		}})
	})
	mux.HandleFunc("/wiki/", tn.page("/wiki/"))
}

// page is an app page: signed-in visitors see it, others go to the login form.
func (tn *tenant) page(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !tn.valid(r) {
			http.Redirect(w, r, "/login?continue="+url.QueryEscape(path), http.StatusFound)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Home</title><h1>Signed in</h1>`)
	}
}

// approve completes the login in any headed window showing the form, now or later.
func (tn *tenant) approve() {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	tn.approved = true
}

// expect resets approval and the login counters, and sets how project search answers.
func (tn *tenant) expect(search string) {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	tn.approved, tn.search, tn.headed, tn.headless = false, search, 0, 0
}

func (tn *tenant) headedLogins() int {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	return tn.headed
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
