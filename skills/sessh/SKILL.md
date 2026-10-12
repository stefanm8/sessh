---
name: sessh
description: Install and use sessh for Bitbucket, Jira, Confluence, and Microsoft Teams through saved browser sessions, including requested Jira issue updates.
---

# sessh

## Setup

Check `sessh --version` first. If missing, install on macOS/Linux (amd64/arm64):

```sh
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
```

Browser commands need Node.js/npm and this one-time setup:

```sh
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/setup-browser.sh | sh
```

From a source checkout, use `make install` (Go 1.25.4+) and
`sh scripts/setup-browser.sh`. `SESSH_INSTALL_DIR` changes the binary destination;
`SESSH_REPOSITORY=owner/repo` selects a fork.

## Sessions and queries

Check `sessh auth list` and `sessh auth status <service>`. Use the user's
`~/.sessh/config.yaml` or `BITBUCKET_WORKSPACE`, `JIRA_SITE`, and `CONFLUENCE_SITE`;
example tenants and resource IDs are placeholders. `SESSH_HOME` changes the root.
Atlassian services share the `atlassian` profile; Teams uses `microsoft`.

Expired sessions open a browser and wait up to five minutes for SSO/MFA. Allow
that timeout and tell the user to sign in. `--no-login` fails fast. Use `--profile`
for another identity and `--no-inherit` for a new profile without copied cookies.
Do not reset or import profiles merely to retry a command.

Discover syntax with `sessh <command> --help`. Prefer finite `--limit` values and
`--json` (Teams: `-f json`). Browser commands reuse a worker for 30 idle minutes;
`sessh daemon status|logs|stop <service>` manages it. `--keep-alive` changes the
idle timeout for new workers; `--no-keep-alive` disables automatic startup.

## Teams

`messages` starts capture if needed and waits for its first successful harvest
(default timeout: two minutes; override with `--capture-timeout`). For saved
history, use `messages --offline` or `--source PATH`; other queries stay local.
Capture opens chats and marks them read. `--no-scroll` disables scrolling for a
new worker; existing workers retain their options.

`sessh teams capture` starts a background worker. Its `status`, `monitor`, `logs -f`,
and `stop` subcommands manage it; Ctrl+C in a monitor leaves capture running.
Use `capture --once` for one foreground refresh or `--foreground` for continuous
capture in the terminal. Capture needs no separate daemon. Enable shell startup
with `sessh autostart enable teams` only when requested.

## Jira writes

Perform Jira changes only when the user's task authorizes them.
Use `--dry-run` to inspect a mutation when useful; it can read metadata but does
not write. Removing it performs the change without another prompt. Resolve
account IDs with `user search`, issue types with `project types`, and available
transitions with `issue transitions`. Do not infer IDs from ambiguous names.

`--fields-json` accepts a fields object, not a request envelope. Description,
comment, and JSON file flags accept `-` for stdin; only one input may use stdin.
Omitted edit fields stay unchanged; explicit empty values clear supported fields.
Inspect create/edit metadata for required fields and allowed values.

After an uncertain write result, inspect Jira before retrying. Use fixtures for
write tests and keep credentials, profiles, and captured messages out of diagnostics.
