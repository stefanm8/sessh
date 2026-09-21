---
name: sessh
description: Install and use sessh to read Bitbucket, Jira, Confluence, and Microsoft Teams through saved browser sessions and local Teams captures.
---

# sessh

## Install

Requires macOS/Linux (amd64/arm64) and authenticated GitHub CLI with access to
the private `stefanm8/sessh` repository. If `sessh` is already available, check
`sessh --version` before installing or updating it.

```sh
gh release download -R stefanm8/sessh -p install.sh -O - | sh
export PATH="$HOME/.local/bin:$PATH"
sessh --version
```

For live browser commands, Node.js/npm must be available. Install the matching
driver and Chromium with:

```sh
gh release download -R stefanm8/sessh -p setup-browser.sh -O - | sh
```

Go is unnecessary for release binaries. `SESSH_INSTALL_DIR` changes the binary
destination. If repository access fails, have the user authenticate `gh` with
an authorized account; do not change repository visibility.

## Use existing sessions

Run `sessh auth list`, then `sessh auth status <service>`. Configuration lives
in `~/.sessh/config.yaml`; `SESSH_HOME` overrides that root. Use the user's
`BITBUCKET_WORKSPACE`, `JIRA_SITE`, and `CONFLUENCE_SITE` settings, never guess a
tenant. Atlassian services share `atlassian`; Teams uses `microsoft` by default.

If signed out, the user can complete SSO/MFA with `sessh auth login <service>`.
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
`sessh teams capture` (long-running; stop with Ctrl+C), not another login.
Use `--source /path/to/messages.jsonl` for an existing capture. Do not start
capture unless the task needs it. Keep credentials, profiles, and message
contents out of diagnostics and source control; summarize live tests by result.
