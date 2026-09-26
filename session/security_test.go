package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateState(t *testing.T) {
	// Short path also fits macOS's Unix socket path limit.
	home, err := os.MkdirTemp("/tmp", "sessh-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("SESSH_HOME", filepath.Join(home, "state"))
	lock, err := acquireLock("work")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	server, err := serve(nil, "work")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := WriteMeta(&Meta{Name: "work"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{Root(), ProfileDir("work"), RunDir(), SocketPath("work"), lockPath("work"), metaPath("work")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is accessible to other users: %o", path, info.Mode().Perm())
		}
	}
}

func TestValidateSiteSetting(t *testing.T) {
	for _, value := range []string{"", "tenant.example", "file:///tmp/site", "https://user:pass@tenant.example", "https://tenant.example/wiki", "https://tenant.example?token=secret", "https://tenant.example#fragment"} {
		if err := ValidateSiteSetting("jira", value, "JIRA_SITE"); err == nil {
			t.Errorf("accepted invalid site %q", value)
		}
	}
	for _, value := range []string{"https://tenant.example", "https://tenant.example/", "http://localhost:8080"} {
		if err := ValidateSiteSetting("jira", value, "JIRA_SITE"); err != nil {
			t.Errorf("rejected valid site %q: %v", value, err)
		}
	}
}
