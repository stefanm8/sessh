package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultProfile is the profile used when nothing names one, and the parent
// every new profile is seeded from.
const DefaultProfile = "default"

// Profile describes the browser identity a plugin needs. Name selects the
// directory (profiles sharing a name share cookies and a lock); Origin,
// LoginURL and Verify describe the service and always come from the plugin.
type Profile struct {
	Name     string
	Origin   string
	LoginURL string

	// Verify checks that the profile really carries a live session for this
	// origin, and may mint one (silent SSO) before returning. It is allowed to
	// navigate the page. Called once per acquire; returning an ErrAuth-wrapped
	// error means "the user must log in".
	Verify func(ctx context.Context, s Session) error

	// Whoami backs Session.Whoami for this profile's origin.
	Whoami func(ctx context.Context, s Session) (Identity, error)
}

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

// Root is ~/.sessh (override with SESSH_HOME).
func Root() string {
	if v := os.Getenv("SESSH_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sessh"
	}
	return filepath.Join(home, ".sessh")
}

// ProfileDir is the Chromium user-data directory for a profile name.
func ProfileDir(name string) string {
	return filepath.Join(Root(), "profiles", name)
}

// RunDir holds sockets and pid files.
func RunDir() string { return filepath.Join(Root(), "run") }

// SocketPath is where the lock holder for a profile listens.
func SocketPath(name string) string {
	return filepath.Join(RunDir(), name+".sock")
}

// Exists reports whether a profile has ever been launched (Chromium writes a
// Default/ subdirectory on first run).
func Exists(name string) bool {
	_, err := os.Stat(filepath.Join(ProfileDir(name), "Default"))
	return err == nil
}

// ---------------------------------------------------------------------------
// Per-profile metadata (~/.sessh/profiles/<name>/sessh.json)
// ---------------------------------------------------------------------------

// Meta records where a profile came from and what it has seen, so
// `sessh auth list` can render the inheritance tree.
type Meta struct {
	Name         string    `json:"name"`
	Parent       string    `json:"parent,omitempty"`
	Created      time.Time `json:"created"`
	Origins      []string  `json:"origins,omitempty"`
	LastVerified time.Time `json:"lastVerified,omitempty"`
	LastIdentity string    `json:"lastIdentity,omitempty"`
}

func metaPath(name string) string { return filepath.Join(ProfileDir(name), "sessh.json") }

// ReadMeta loads a profile's metadata; a missing file is not an error.
func ReadMeta(name string) (*Meta, error) {
	b, err := os.ReadFile(metaPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return &Meta{Name: name}, nil
		}
		return nil, err
	}
	m := &Meta{}
	if err := json.Unmarshal(b, m); err != nil {
		return &Meta{Name: name}, nil
	}
	m.Name = name
	return m, nil
}

// WriteMeta persists a profile's metadata.
func WriteMeta(m *Meta) error {
	if err := os.MkdirAll(ProfileDir(m.Name), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath(m.Name), append(b, '\n'), 0o600)
}

// TouchVerified records a successful verification against an origin.
func TouchVerified(name, origin string, id Identity) {
	m, err := ReadMeta(name)
	if err != nil {
		return
	}
	if m.Created.IsZero() {
		m.Created = time.Now()
	}
	m.LastVerified = time.Now()
	if n := id.Name(); n != "" {
		m.LastIdentity = n
	}
	if origin != "" {
		seen := false
		for _, o := range m.Origins {
			if o == origin {
				seen = true
				break
			}
		}
		if !seen {
			m.Origins = append(m.Origins, origin)
		}
	}
	_ = WriteMeta(m)
}

// ListProfiles enumerates the profiles on disk, newest metadata first-come.
func ListProfiles() ([]*Meta, error) {
	dir := filepath.Join(Root(), "profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := ReadMeta(e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading profile %s: %w", e.Name(), err)
		}
		out = append(out, m)
	}
	return out, nil
}
