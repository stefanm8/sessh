# sessh

Bitbucket, Jira, Confluence, and Teams from your terminal using persistent
Chromium sessions. Sign in once with SSO/MFA. macOS/Linux, amd64/arm64.

## Install

```sh
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/install.sh | sh
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/setup-browser.sh | sh
```

Browser setup needs Node.js/npm. The installer checks SHA-256, installs to
`~/.local/bin`, and configures PATH. From source: `make install` (Go 1.25.4+),
then `sh scripts/setup-browser.sh`.

## Use

```sh
export BITBUCKET_WORKSPACE=your-workspace
export JIRA_SITE=https://your-tenant.atlassian.net
export CONFLUENCE_SITE=https://your-tenant.atlassian.net

sessh auth login jira
sessh jira issue view PROJECT-123
sessh bitbucket pr list your-repo
sessh confluence page view 123456
sessh teams messages --since 24h --limit 20 -f json
```

Use `sessh <command> --help` for commands and flags. Settings can also live in
`~/.sessh/config.yaml` under `plugins.<service>`; environment variables take precedence.
Expired sessions open a login window; `--no-login` fails fast. Use `--profile`
for another identity and `--no-inherit` to start without copied credentials.
All plugins share a 15-second authentication-check budget across requests,
navigation, and polling. Known sign-in pages stop the check immediately;
network errors and timeouts are reported without opening a login window.
Interactive sign-in keeps its separate five-minute deadline.
`sessh daemon stop <service>` closes a shared browser; `--no-keep-alive` disables
its automatic startup. Jira write commands support `--dry-run`.

Teams `messages` starts capture and waits for a successful harvest. Capture opens
chats and marks them read; `--no-scroll` avoids this for a new worker. Use
`messages --offline` or `--source PATH` for saved data; other Teams queries stay
local. Manage capture with `sessh teams capture status|monitor|logs|stop`.

## Agents

```sh
npx --yes skills add https://github.com/stefanm8/sessh.git --skill sessh -a codex -g -y
```

## Develop

`make build`, `make check`, `make e2e` (Chromium and display required),
`make dist VERSION=X.Y.Z`. Keep Playwright versions in `session/driver.go` and
`scripts/setup-browser.sh` aligned. A `vX.Y.Z` tag from `main` releases after CI.

Profiles, captures, and logs live under `~/.sessh` (`SESSH_HOME` overrides it).
Keep them private and use synthetic test data. Report vulnerabilities through
[private advisories](https://github.com/stefanm8/sessh/security/advisories/new).

[MIT license](LICENSE) · [Third-party notices](THIRD_PARTY_NOTICES.md)
