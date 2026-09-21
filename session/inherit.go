package session

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/mxschmitt/playwright-go"
)

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

// A new profile inherits `default`: at creation we copy the
// parent's *storage state* — cookies plus per-origin localStorage — and nothing
// else. Deliberately not a directory copy: that would drag along SingletonLock,
// the caches, and IndexedDB, and IndexedDB is exactly what must not be
// inherited (the teams plugin's captured messages live there). Inheritance
// carries credentials, never data.
//
// The seed is written to the child's directory and applied at its first launch,
// which keeps the operation crash-safe: either the seed file is there and gets
// applied, or it isn't and the profile is simply empty.

const seedFile = "seed.json"

// SeedOptions controls where a new profile's credentials come from.
type SeedOptions struct {
	From      string // parent profile name; "" means DefaultProfile
	NoInherit bool
}

func (o SeedOptions) parent() string {
	if o.NoInherit {
		return ""
	}
	if o.From != "" {
		return o.From
	}
	return DefaultProfile
}

// prepareProfile creates the directory for `name` if it does not exist yet,
// seeding it from its parent. Returns a human-readable note about what
// happened, or "" when the profile already existed.
func prepareProfile(name string, opts SeedOptions) (string, error) {
	dir := ProfileDir(name)
	if _, err := os.Stat(dir); err == nil {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	meta := &Meta{Name: name, Created: time.Now()}

	parent := opts.parent()
	if parent == "" || parent == name || !Exists(parent) {
		if err := WriteMeta(meta); err != nil {
			return "", err
		}
		if parent != "" && parent != name && !Exists(parent) {
			return fmt.Sprintf("new profile %q starts empty (no %q profile to inherit from)", name, parent), nil
		}
		return fmt.Sprintf("new profile %q starts empty", name), nil
	}

	state, err := exportStorageState(parent)
	if err != nil {
		// Inheritance is a convenience; never let it block the command.
		if err := WriteMeta(meta); err != nil {
			return "", err
		}
		return fmt.Sprintf("new profile %q starts empty (could not read %q: %v)", name, parent, err), nil
	}
	if err := os.WriteFile(filepath.Join(dir, seedFile), state, 0o600); err != nil {
		return "", err
	}
	meta.Parent = parent
	if err := WriteMeta(meta); err != nil {
		return "", err
	}
	return fmt.Sprintf("new profile %q seeded from %q", name, parent), nil
}

// exportStorageState reads the parent's cookies + localStorage. It routes
// through the daemon when the parent is busy, so seeding never requires
// stopping a long-running capture.
func exportStorageState(parent string) (json.RawMessage, error) {
	if cl, err := dialProfile(parent); err == nil {
		defer cl.Close()
		return cl.storageState()
	}
	lock, err := acquireLock(parent)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	s, closer, err := launch(Profile{Name: parent, Origin: "https://example.invalid"}, launchOptions{})
	if err != nil {
		return nil, err
	}
	defer closer()
	return s.StorageState()
}

// ImportProfile seeds a sessh profile from a *foreign* Chromium user-data
// directory — the `userData/` folders the Node CLIs this replaces have been
// logging in to for months. Migration is much less annoying if
// step one is not "log in again everywhere", and the mechanism is the same
// storage-state copy inheritance already uses: credentials, never data.
func ImportProfile(name, fromDir string) error {
	if _, err := os.Stat(filepath.Join(fromDir, "Default")); err != nil {
		return fmt.Errorf("%s does not look like a Chromium profile (no Default/ inside)", fromDir)
	}
	state, err := exportStorageStateFromDir(fromDir)
	if err != nil {
		return err
	}
	dir := ProfileDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, seedFile), state, 0o600); err != nil {
		return err
	}
	meta, err := ReadMeta(name)
	if err != nil {
		return err
	}
	if meta.Created.IsZero() {
		meta.Created = time.Now()
	}
	meta.Parent = fromDir
	return WriteMeta(meta)
}

func exportStorageStateFromDir(dir string) (json.RawMessage, error) {
	s, closer, err := launchDir(dir, Profile{Name: filepath.Base(dir), Origin: "https://example.invalid"}, launchOptions{})
	if err != nil {
		return nil, err
	}
	defer closer()
	return s.StorageState()
}

// applySeed installs a pending seed into a freshly launched context and
// removes it: inheritance is a one-time copy at creation, not a link.
func applySeed(ctxt playwright.BrowserContext, name string) error {
	path := filepath.Join(ProfileDir(name), seedFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil // nothing to inherit
	}
	var state struct {
		Cookies []struct {
			Name     string   `json:"name"`
			Value    string   `json:"value"`
			Domain   string   `json:"domain"`
			Path     string   `json:"path"`
			Expires  float64  `json:"expires"`
			HTTPOnly bool     `json:"httpOnly"`
			Secure   bool     `json:"secure"`
			SameSite *string  `json:"sameSite"`
			_        struct{} `json:"-"`
		} `json:"cookies"`
		Origins []struct {
			Origin       string `json:"origin"`
			LocalStorage []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"localStorage"`
		} `json:"origins"`
	}
	if err := json.Unmarshal(b, &state); err != nil {
		_ = os.Remove(path)
		return nil
	}

	cookies := make([]playwright.OptionalCookie, 0, len(state.Cookies))
	for _, c := range state.Cookies {
		oc := playwright.OptionalCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   playwright.String(c.Domain),
			Path:     playwright.String(c.Path),
			HttpOnly: playwright.Bool(c.HTTPOnly),
			Secure:   playwright.Bool(c.Secure),
		}
		if c.Expires != 0 {
			oc.Expires = playwright.Float(c.Expires)
		}
		if c.SameSite != nil {
			ss := playwright.SameSiteAttribute(*c.SameSite)
			oc.SameSite = &ss
		}
		cookies = append(cookies, oc)
	}
	if len(cookies) > 0 {
		if err := ctxt.AddCookies(cookies); err != nil {
			return fmt.Errorf("seeding cookies into profile %q: %w", name, err)
		}
	}

	// localStorage cannot be written without being on the origin, so install it
	// as an init script that runs before any page script on a matching origin.
	for _, o := range state.Origins {
		if len(o.LocalStorage) == 0 {
			continue
		}
		payload, err := json.Marshal(o.LocalStorage)
		if err != nil {
			continue
		}
		script := fmt.Sprintf(`(() => { try {
  if (window.location.origin !== %s) return;
  const items = %s;
  for (const it of items) { try { window.localStorage.setItem(it.name, it.value); } catch {} }
} catch {} })()`, mustJSON(o.Origin), string(payload))
		if err := ctxt.AddInitScript(playwright.Script{Content: playwright.String(script)}); err != nil {
			return fmt.Errorf("seeding localStorage into profile %q: %w", name, err)
		}
	}

	_ = os.Remove(path)
	return nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}
