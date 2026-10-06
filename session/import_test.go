package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func importSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestImportRefusesAnActiveTargetBeforeReadingSource(t *testing.T) {
	isolatedHome(t)
	source := importSource(t)
	guard, err := acquireLock("target")
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	err = importProfile("target", source, func(string) (json.RawMessage, error) {
		t.Fatal("opened source while target was active")
		return nil, nil
	})
	var busy *ErrBusy
	if !errors.As(err, &busy) || busy.Profile != "target" {
		t.Fatalf("import = %v; want busy target", err)
	}
	if _, err := os.Stat(filepath.Join(ProfileDir("target"), seedFile)); !os.IsNotExist(err) {
		t.Fatalf("busy target received a seed: %v", err)
	}
}

func TestImportOwnsTargetUntilCredentialsSaved(t *testing.T) {
	isolatedHome(t)
	source := importSource(t)
	created := time.Now().Add(-time.Hour)
	if err := WriteMeta(&Meta{Name: "target", Created: created, LastVerified: time.Now(), LastIdentity: "previous user", Origins: []string{"https://old.example"}}); err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"cookies":[],"origins":[]}`)
	err := importProfile("target", source, func(got string) (json.RawMessage, error) {
		if got != source {
			t.Fatalf("source = %q, want %q", got, source)
		}
		other, err := acquireLock("target")
		if other != nil {
			other.Release()
		}
		var busy *ErrBusy
		if !errors.As(err, &busy) {
			t.Fatalf("target was unprotected while exporting source: %v", err)
		}
		return state, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(ProfileDir("target"), seedFile))
	if err != nil || string(got) != string(state) {
		t.Fatalf("seed = %s, %v", got, err)
	}
	meta, err := ReadMeta("target")
	if err != nil || meta.Parent != source || !meta.Created.Equal(created) || !meta.LastVerified.IsZero() || meta.LastIdentity != "" || len(meta.Origins) != 0 {
		t.Fatalf("metadata still describes old identity: %+v, %v", meta, err)
	}
	guard, err := acquireLock("target")
	if err != nil {
		t.Fatalf("import did not release target: %v", err)
	}
	guard.Release()
}

func TestImportExportFailurePreservesExistingSeed(t *testing.T) {
	isolatedHome(t)
	source := importSource(t)
	if err := os.MkdirAll(ProfileDir("target"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ProfileDir("target"), seedFile)
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := errors.New("could not read source")
	err := importProfile("target", source, func(string) (json.RawMessage, error) { return nil, expected })
	if !errors.Is(err, expected) {
		t.Fatalf("import = %v, want original source error", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "original" {
		t.Fatalf("failed export changed seed: %s, %v", got, err)
	}
}
