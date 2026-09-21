// Package teams is the sessh plugin for Microsoft Teams.
//
// It is the plugin that exercises the two hard parts of the hub: Eval (the only
// way to reach IndexedDB, which is the real source of Teams history) and the
// long-lock case — `sessh teams capture` holds its profile for hours, and other
// commands route through its socket instead of failing to launch.
//
// The read-only query commands need no session at all: they read the captured
// messages.jsonl and never open a browser.
package teams

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stefanm8/sessh/plugin"
	"github.com/stefanm8/sessh/session"
)

// TeamsURL is where capture points the browser.
func TeamsURL() string {
	return session.Setting("teams", "url", os.Getenv("TEAMS_URL"), "https://teams.microsoft.com/v2/")
}

// Origin is the profile's origin. Teams v2 runs on teams.microsoft.com and
// redirects into teams.cloud.microsoft; either carries the session.
func Origin() string { return "https://teams.microsoft.com" }

type Teams struct{}

func init() { plugin.Register(&Teams{}) }

func (t *Teams) Name() string { return "teams" }

func (t *Teams) Profile() session.Profile {
	return session.Profile{
		// Teams is a Microsoft login, not an Atlassian one, so it gets its own
		// profile rather than sharing `atlassian`.
		Name:     session.Setting("teams", "profile", os.Getenv("TEAMS_PROFILE"), "microsoft"),
		Origin:   Origin(),
		LoginURL: TeamsURL(),
		Verify:   verify,
		Whoami:   whoami,
	}
}

// verify checks that the profile carries a live Teams web session.
//
// Teams is a single-page app behind an MSAL redirect, so there is no cheap
// authenticated JSON endpoint to poll. The unambiguous signal is the app's own
// storage: a signed-in Teams boots (via teams.microsoft.com →
// teams.cloud.microsoft) and provisions its per-user IndexedDB databases,
// including the `:replychain-manager:` one capture reads. An anonymous visit
// never gets that far — it is bounced to login.microsoftonline.com.
//
// Boot takes the better part of ten seconds and navigates the page more than
// once on the way, so every probe here is a *cheap* expression: a long-running
// evaluate would be torn down mid-flight by those navigations.
func verify(ctx context.Context, s session.Session) error {
	landed, err := s.Navigate(ctx, TeamsURL())
	if err != nil {
		return err
	}
	if isLoginHost(landed) {
		return notSignedIn()
	}

	deadline := time.Now().Add(90 * time.Second)
	for {
		var st struct {
			URL   string `json:"url"`
			Ready bool   `json:"ready"`
		}
		// A failed probe is almost always "the context was destroyed by the
		// app's own redirect", which is progress, not an error.
		if err := s.Eval(ctx, bootProbeJS, nil, &st); err == nil {
			if isLoginHost(st.URL) {
				return notSignedIn()
			}
			if st.Ready {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return session.Authf("microsoft",
				"Teams did not finish loading for this profile — it may be signed out.\n"+
					"Check with: sessh auth login teams")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func notSignedIn() error {
	return session.Authf("microsoft", "not signed in to Teams. Run: sessh auth login teams")
}

// bootProbeJS reports where the page is and whether Teams has provisioned its
// user databases yet.
const bootProbeJS = `async () => ({
  url: location.href,
  ready: (await indexedDB.databases()).some((d) => /:(replychain|conversation)-manager:/.test(d.name || '')),
})`

func isLoginHost(u string) bool {
	l := strings.ToLower(u)
	return strings.Contains(l, "login.microsoftonline.com") ||
		strings.Contains(l, "login.live.com") ||
		strings.Contains(l, "/_#/login")
}

// whoami reads the signed-in account out of the app's own storage: MSAL
// account records when they are in local storage, and otherwise the user and
// tenant GUIDs that Teams bakes into its per-user IndexedDB names
// (`Teams:conversation-manager:react-web-client:<user>:<tenant>:en-us`). It is
// best-effort by design — a missing display name must never downgrade a working
// session to "logged out".
func whoami(ctx context.Context, s session.Session) (session.Identity, error) {
	var out struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		OID      string `json:"oid"`
		Tenant   string `json:"tenant"`
	}
	if err := s.Eval(ctx, whoamiJS, nil, &out); err != nil {
		return session.Identity{}, nil // never let identity lookup fail a command
	}
	if out.Username == "" && out.Name == "" && out.OID == "" {
		return session.Identity{}, nil
	}
	return session.Identity{
		AccountID:   out.OID,
		DisplayName: out.Name,
		Email:       out.Username,
		Username:    out.Username,
	}, nil
}

const whoamiJS = `async () => {
  const pick = (o) => o && (o.username || o.preferred_username || o.upn) ? {
    username: o.username || o.preferred_username || o.upn || '',
    name: o.name || o.displayName || '',
    oid: o.localAccountId || o.homeAccountId || o.oid || '',
    tenant: o.tenantId || '',
  } : null;
  try {
    for (const store of [window.localStorage, window.sessionStorage]) {
      if (!store) continue;
      for (let i = 0; i < store.length; i++) {
        const k = store.key(i);
        if (!k || !/account/i.test(k)) continue;
        let v;
        try { v = JSON.parse(store.getItem(k)); } catch { continue; }
        const hit = pick(v);
        if (hit) return hit;
      }
    }
  } catch {}
  try {
    const guid = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';
    const re = new RegExp('conversation-manager:[^:]+:(' + guid + '):(' + guid + ')', 'i');
    for (const db of await indexedDB.databases()) {
      const m = re.exec(db.name || '');
      if (m) return { username: '', name: '', oid: m[1], tenant: m[2] };
    }
  } catch {}
  return null;
}`

func (t *Teams) Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams <command>",
		Short: "Capture and query Microsoft Teams messages",
		Long: `Query the locally captured Teams messages, and run the capture that fills them.

The query commands are read-only over messages.jsonl and never open a browser.
` + "`sessh teams capture`" + ` is the long-running half: it holds its browser profile
open, harvests message history out of Teams' own IndexedDB, and serves that
profile on a socket so other sessh commands can share it.`,
	}
	cmd.AddCommand(queryCommands(t)...)
	cmd.AddCommand(backfillCommand())
	cmd.AddCommand(captureCommand(t))
	return cmd
}
