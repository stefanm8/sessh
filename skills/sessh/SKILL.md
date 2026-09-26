---
name: sessh
description: Install and use sessh to read Bitbucket, Jira, Confluence, and Microsoft Teams through saved browser sessions and local Teams captures.
---

# sessh

## Install

Requires macOS/Linux (amd64/arm64) and curl. Public releases need no GitHub
account. If `sessh` is already available, check
`sessh --version` before installing or updating it.

```sh
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
sessh --version
```

For live browser commands, Node.js/npm must be available. Install the matching
driver and Chromium with:

```sh
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/setup-browser.sh | sh
```

The installer configures PATH for future bash/zsh terminals. The export above
also activates the default install location in the current process. From a
source checkout, `make install` builds and installs to the same `~/.local/bin`
location; then invoke `sessh` directly, not `./bin/sessh`.

Go is unnecessary for release binaries. `SESSH_INSTALL_DIR` changes the binary
destination; `SESSH_REPOSITORY=owner/repo` selects a fork. For a private fork,
use authenticated
`gh release download -R owner/repo -p install.sh -O - | SESSH_REPOSITORY=owner/repo sh`.
Replace `owner/repo` with the fork's repository. Do not change repository visibility.

## Use existing sessions

Run `sessh auth list`, then `sessh auth status <service>`. Configuration lives
in `~/.sessh/config.yaml`; `SESSH_HOME` overrides that root. Use the user's
`BITBUCKET_WORKSPACE`, `JIRA_SITE`, and `CONFLUENCE_SITE` settings, never guess a
tenant. Values such as `your-workspace`, `your-tenant`, and documentation resource
IDs are placeholders; obtain actual identifiers from the user's configuration
or service listings. Atlassian services share `atlassian`; Teams uses `microsoft`
by default.

A command whose session has lapsed opens a browser window and waits up to five
minutes for the user to complete SSO/MFA, then finishes. Tell the user to sign in
in that window, and allow a long enough command timeout. Pass `--no-login` to
fail fast instead; `sessh auth login <service>` signs in ahead of time.
Do not reset or import profiles just to retry a failed command. Importing a
browser profile requires closing the source browser first. `--profile` selects
another identity; `--no-inherit` prevents seeding it from `default`.

Use `sessh <command> --help` to discover commands and flags. Useful read checks:

```sh
sessh bitbucket repo list --limit 1 --json
sessh jira project list --limit 1 --json
sessh confluence space list --limit 1 --json
sessh teams messages --since 24h --limit 20 -f json
```

Teams queries read local captures without a browser. A missing capture needs
`sessh teams capture start` (background, survives closing the terminal), not
another login. Check it with `sessh teams capture status`, inspect progress with
`sessh teams capture logs`, and stop it with `sessh teams capture stop`. Capture
already keeps its browser open and needs no separate daemon. Use `--once` on
plain `capture` for one refresh. Capture opens chats and marks them read unless
`--no-scroll` is passed. If explicitly requested, `sessh autostart enable teams`
adds an idempotent shell startup entry; `autostart disable` removes it.
Use `--source /path/to/messages.jsonl` for an existing capture. Do not start
capture unless the task needs it. Keep credentials, profiles, and message
contents out of diagnostics and source control; summarize live tests by result.
