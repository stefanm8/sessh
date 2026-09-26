package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExists(t *testing.T) {
	t.Run("a profile that was never launched does not exist", func(t *testing.T) {
		isolatedHome(t)
		if Exists("never-launched") {
			t.Error("Exists() = true for a profile with no directory")
		}
	})
	t.Run("a directory without Chromium's Default subdir does not count", func(t *testing.T) {
		isolatedHome(t)
		if err := os.MkdirAll(ProfileDir("half"), 0o755); err != nil {
			t.Fatal(err)
		}
		if Exists("half") {
			t.Error("Exists() = true for a profile Chromium never wrote to")
		}
	})
	t.Run("a Default subdirectory means it has been launched", func(t *testing.T) {
		isolatedHome(t)
		if err := os.MkdirAll(filepath.Join(ProfileDir("real"), "Default"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !Exists("real") {
			t.Error("Exists() = false for a launched profile")
		}
	})
}

func TestReadMeta(t *testing.T) {
	t.Run("a missing file yields a named empty Meta, not an error", func(t *testing.T) {
		isolatedHome(t)
		m, err := ReadMeta("ghost")
		if err != nil {
			t.Fatalf("ReadMeta() error = %v, want nil", err)
		}
		if m.Name != "ghost" {
			t.Errorf("Name = %q, want %q", m.Name, "ghost")
		}
		if !m.Created.IsZero() || m.Parent != "" || len(m.Origins) != 0 {
			t.Errorf("ReadMeta() = %+v, want a zero Meta", m)
		}
	})

	t.Run("corrupt JSON degrades to an empty Meta rather than failing", func(t *testing.T) {
		isolatedHome(t)
		if err := os.MkdirAll(ProfileDir("broken"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metaPath("broken"), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := ReadMeta("broken")
		if err != nil {
			t.Fatalf("ReadMeta() error = %v, want nil", err)
		}
		if m.Name != "broken" || m.Parent != "" {
			t.Errorf("ReadMeta() = %+v, want a zero Meta named broken", m)
		}
	})

	t.Run("the name on disk never overrides the name asked for", func(t *testing.T) {
		isolatedHome(t)
		if err := WriteMeta(&Meta{Name: "ondisk"}); err != nil {
			t.Fatal(err)
		}
		// Rewrite the same file under a different directory name.
		body, err := os.ReadFile(metaPath("ondisk"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(ProfileDir("asked"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metaPath("asked"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := ReadMeta("asked")
		if err != nil {
			t.Fatal(err)
		}
		if m.Name != "asked" {
			t.Errorf("Name = %q, want the directory name %q", m.Name, "asked")
		}
	})
}

func TestWriteMetaRoundTrip(t *testing.T) {
	isolatedHome(t)

	created := time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)
	verified := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	want := &Meta{
		Name:         "atlassian",
		Parent:       DefaultProfile,
		Created:      created,
		Origins:      []string{"https://tenant.example", "https://bitbucket.org"},
		LastVerified: verified,
		LastIdentity: "Example User",
	}

	t.Run("WriteMeta creates the profile directory", func(t *testing.T) {
		if err := WriteMeta(want); err != nil {
			t.Fatalf("WriteMeta() error = %v", err)
		}
		if _, err := os.Stat(ProfileDir("atlassian")); err != nil {
			t.Errorf("profile dir not created: %v", err)
		}
	})

	t.Run("every field survives a read", func(t *testing.T) {
		got, err := ReadMeta("atlassian")
		if err != nil {
			t.Fatalf("ReadMeta() error = %v", err)
		}
		if got.Name != want.Name || got.Parent != want.Parent || got.LastIdentity != want.LastIdentity {
			t.Errorf("ReadMeta() = %+v, want %+v", got, want)
		}
		if !got.Created.Equal(want.Created) {
			t.Errorf("Created = %s, want %s", got.Created, want.Created)
		}
		if !got.LastVerified.Equal(want.LastVerified) {
			t.Errorf("LastVerified = %s, want %s", got.LastVerified, want.LastVerified)
		}
		if len(got.Origins) != len(want.Origins) {
			t.Fatalf("Origins = %v, want %v", got.Origins, want.Origins)
		}
		for i := range want.Origins {
			if got.Origins[i] != want.Origins[i] {
				t.Errorf("Origins[%d] = %q, want %q", i, got.Origins[i], want.Origins[i])
			}
		}
	})

	t.Run("the metadata file is not world readable", func(t *testing.T) {
		fi, err := os.Stat(metaPath("atlassian"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("mode = %o, want no group/other bits", perm)
		}
	})

	t.Run("a second write replaces rather than appends", func(t *testing.T) {
		if err := WriteMeta(&Meta{Name: "atlassian", LastIdentity: "Someone Else"}); err != nil {
			t.Fatal(err)
		}
		got, err := ReadMeta("atlassian")
		if err != nil {
			t.Fatal(err)
		}
		if got.LastIdentity != "Someone Else" {
			t.Errorf("LastIdentity = %q, want %q", got.LastIdentity, "Someone Else")
		}
		if got.Parent != "" {
			t.Errorf("Parent = %q, want it cleared by the overwrite", got.Parent)
		}
	})
}

func TestTouchVerified(t *testing.T) {
	t.Run("the first touch stamps Created and records the origin", func(t *testing.T) {
		isolatedHome(t)
		TouchVerified("work", "https://tenant.example", Identity{DisplayName: "Example User"})
		m, err := ReadMeta("work")
		if err != nil {
			t.Fatal(err)
		}
		if m.Created.IsZero() {
			t.Error("Created is zero after the first verification")
		}
		if m.LastVerified.IsZero() {
			t.Error("LastVerified is zero after a verification")
		}
		if len(m.Origins) != 1 || m.Origins[0] != "https://tenant.example" {
			t.Errorf("Origins = %v, want one entry", m.Origins)
		}
		if m.LastIdentity != "Example User" {
			t.Errorf("LastIdentity = %q, want %q", m.LastIdentity, "Example User")
		}
	})

	t.Run("the same origin is not recorded twice", func(t *testing.T) {
		isolatedHome(t)
		TouchVerified("work", "https://tenant.example", Identity{})
		TouchVerified("work", "https://tenant.example", Identity{})
		m, _ := ReadMeta("work")
		if len(m.Origins) != 1 {
			t.Errorf("Origins = %v, want exactly one entry", m.Origins)
		}
	})

	t.Run("a second origin is appended, since profiles are shared", func(t *testing.T) {
		isolatedHome(t)
		TouchVerified("atlassian", "https://tenant.example", Identity{})
		TouchVerified("atlassian", "https://bitbucket.org", Identity{})
		m, _ := ReadMeta("atlassian")
		if len(m.Origins) != 2 {
			t.Fatalf("Origins = %v, want two entries", m.Origins)
		}
	})

	t.Run("an empty origin is not recorded", func(t *testing.T) {
		isolatedHome(t)
		TouchVerified("work", "", Identity{})
		m, _ := ReadMeta("work")
		if len(m.Origins) != 0 {
			t.Errorf("Origins = %v, want none", m.Origins)
		}
	})

	t.Run("an anonymous verification does not erase a known identity", func(t *testing.T) {
		isolatedHome(t)
		TouchVerified("work", "https://tenant.example", Identity{DisplayName: "Example User"})
		TouchVerified("work", "https://tenant.example", Identity{})
		m, _ := ReadMeta("work")
		if m.LastIdentity != "Example User" {
			t.Errorf("LastIdentity = %q, want the earlier %q to survive", m.LastIdentity, "Example User")
		}
	})

	t.Run("Created is stamped once and never moved", func(t *testing.T) {
		isolatedHome(t)
		TouchVerified("work", "https://tenant.example", Identity{})
		first, _ := ReadMeta("work")
		time.Sleep(2 * time.Millisecond)
		TouchVerified("work", "https://tenant.example", Identity{})
		second, _ := ReadMeta("work")
		if !second.Created.Equal(first.Created) {
			t.Errorf("Created moved from %s to %s", first.Created, second.Created)
		}
	})
}

func TestListProfiles(t *testing.T) {
	t.Run("no profiles directory yields no profiles and no error", func(t *testing.T) {
		isolatedHome(t)
		got, err := ListProfiles()
		if err != nil {
			t.Fatalf("ListProfiles() error = %v, want nil", err)
		}
		if len(got) != 0 {
			t.Errorf("ListProfiles() = %v, want empty", got)
		}
	})

	t.Run("every profile directory is listed, files are ignored", func(t *testing.T) {
		home := isolatedHome(t)
		for _, name := range []string{"default", "atlassian", "work"} {
			if err := WriteMeta(&Meta{Name: name}); err != nil {
				t.Fatal(err)
			}
		}
		stray := filepath.Join(home, "profiles", "notes.txt")
		if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ListProfiles()
		if err != nil {
			t.Fatalf("ListProfiles() error = %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("ListProfiles() returned %d entries, want 3", len(got))
		}
		seen := map[string]bool{}
		for _, m := range got {
			seen[m.Name] = true
		}
		for _, name := range []string{"default", "atlassian", "work"} {
			if !seen[name] {
				t.Errorf("profile %q missing from %v", name, seen)
			}
		}
	})

	t.Run("a profile directory with no metadata still lists", func(t *testing.T) {
		isolatedHome(t)
		if err := os.MkdirAll(ProfileDir("bare"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := ListProfiles()
		if err != nil {
			t.Fatalf("ListProfiles() error = %v", err)
		}
		if len(got) != 1 || got[0].Name != "bare" {
			t.Errorf("ListProfiles() = %+v, want one entry named bare", got)
		}
	})
}
