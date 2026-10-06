//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// These requests reach a local fake tenant through a real Chromium session,
// including the same APIRequestContext and mutation runner used in production.
func TestJiraWriteWorkflows(t *testing.T) {
	jira := newTenant(t, "jira", "127.0.0.1")
	e := newEnv(t, jira, nil)
	var mu sync.Mutex
	writes := 0
	drop := false
	var last map[string]any
	issue := map[string]any{"id": "10001", "key": "DEMO-1", "fields": map[string]any{
		"summary": "Synthetic issue", "status": map[string]any{"name": "Open"},
		"issuetype": map[string]any{"name": "Task"}, "description": "Synthetic description",
	}}
	handler := func(w http.ResponseWriter, r *http.Request) {
		if !jira.valid(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "signed out"})
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/transitions") {
			writeJSON(w, http.StatusOK, map[string]any{"transitions": []any{
				map[string]any{"id": "31", "name": "Start", "to": map[string]string{"name": "In Progress"}, "fields": map[string]any{}},
			}})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/DEMO-1" {
			writeJSON(w, http.StatusOK, issue)
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodPut {
			writeJSON(w, http.StatusBadRequest, map[string]any{"errorMessages": []string{"unexpected method"}})
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}
		mu.Lock()
		writes++
		last = payload
		shouldDrop := drop
		mu.Unlock()
		if shouldDrop {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close() // write accepted; response lost
			}
			return
		}
		switch {
		case r.URL.Path == "/rest/api/3/issue":
			writeJSON(w, http.StatusCreated, map[string]string{"id": "10001", "key": "DEMO-1", "self": jira.URL() + "/rest/api/3/issue/10001"})
		case strings.HasSuffix(r.URL.Path, "/comment"):
			writeJSON(w, http.StatusCreated, map[string]any{"id": "20001", "body": payload["body"]})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
	jira.mux.HandleFunc("/rest/api/3/issue", handler)
	jira.mux.HandleFunc("/rest/api/3/issue/", handler)
	jira.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"issues": []any{issue}, "isLast": true})
	})
	jira.approve()
	e.run("auth", "login", "jira").expect(0, "Logged in", "")
	t.Run("human output stays on stdout", func(t *testing.T) {
		for _, args := range [][]string{{"jira", "issue", "list", "--limit", "1"}, {"jira", "issue", "view", "DEMO-1"}} {
			c := e.run(args...)
			c.expect(0, "Synthetic issue", "")
			if c.stderr.String() != "" {
				t.Fatalf("command data leaked to stderr: %s", c.stderr.String())
			}
		}
	})

	commands := [][]string{
		{"jira", "issue", "create", "DEMO", "--summary", "Synthetic issue", "--type", "10001", "--description", "First line\nSecond line"},
		{"jira", "issue", "edit", "DEMO-1", "--summary", "Updated summary"},
		{"jira", "issue", "assign", "DEMO-1", "me"},
		{"jira", "issue", "transition", "DEMO-1", "31"},
		{"jira", "issue", "comment", "add", "DEMO-1", "--body", "Synthetic comment"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args[1:4], " "), func(t *testing.T) {
			mu.Lock()
			before := writes
			mu.Unlock()
			dry := append(append([]string{}, args...), "--dry-run", "--json")
			c := e.run(dry...)
			c.expect(0, `"method":`, "")
			var plan map[string]any
			if err := json.Unmarshal([]byte(c.stdout.String()), &plan); err != nil || plan["payload"] == nil {
				t.Fatalf("dry-run is not an exact JSON payload: %v\n%s", err, c.stdout.String())
			}
			mu.Lock()
			count := writes
			mu.Unlock()
			if count != before {
				t.Fatalf("dry-run performed %d writes", count-before)
			}
			c = e.run(append(append([]string{}, args...), "--json")...)
			c.expect(0, "", "")
			var result any
			if err := json.Unmarshal([]byte(c.stdout.String()), &result); err != nil {
				t.Fatalf("success output is not JSON: %v\n%s", err, c.stdout.String())
			}
			mu.Lock()
			count, payload := writes, last
			mu.Unlock()
			if count != before+1 {
				t.Fatalf("command performed %d writes, want one", count-before)
			}
			planned, _ := json.Marshal(plan["payload"])
			actual, _ := json.Marshal(payload)
			if string(planned) != string(actual) {
				t.Fatalf("dry-run payload %s differs from write %s", planned, actual)
			}
		})
	}

	t.Run("write through a shared browser", func(t *testing.T) {
		// Earlier commands may already have started the automatic daemon.
		e.run("daemon", "start", "jira").expect(0, "", "")
		waitFor(t, "the Jira daemon is running", 20*time.Second, func() bool {
			return strings.Contains(e.run("daemon", "status", "jira").stdout.String(), ": running")
		})
		c := e.run("jira", "issue", "edit", "DEMO-1", "--summary", "Routed update", "--json", "--verbose")
		c.expect(0, `"success": true`, "routed through the process holding profile")
		e.run("daemon", "stop", "jira").expect(0, "", "")
	})

	t.Run("lost write response is never replayed", func(t *testing.T) {
		mu.Lock()
		drop, writes = true, 0
		mu.Unlock()
		c := e.run("jira", "issue", "create", "DEMO", "--summary", "Uncertain outcome", "--type", "10001", "--json")
		c.expect(1, "", "write command was not retried")
		mu.Lock()
		defer mu.Unlock()
		if writes != 1 {
			t.Fatalf("lost response caused %d writes; want exactly one", writes)
		}
	})
}
