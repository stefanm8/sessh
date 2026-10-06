//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"
)

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
