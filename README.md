# sessh — session shell

A CLI for Bitbucket, Jira, Confluence, and Microsoft Teams using persistent
Chromium sessions. Log in with SSO/MFA once, then work from your terminal.
macOS/Linux, amd64/arm64. Private while validation is in progress.

## Install

With GitHub CLI authenticated to an account with repository access:

```sh
gh release download -R stefanm8/sessh -p install.sh -O - | sh
# Browser commands also need Node.js, npm, and Chromium:
gh release download -R stefanm8/sessh -p setup-browser.sh -O - | sh
```

The installer checks SHA-256 and writes `~/.local/bin/sessh`; add that directory
to `PATH`. Download binaries directly from [Releases](https://github.com/stefanm8/sessh/releases).
From source, use `make install` (Go 1.25.4+) and `sh scripts/setup-browser.sh`.
The source installer accepts `sh install.sh v0.1.0`; `SESSH_INSTALL_DIR` sets its destination.

## Use

Copy [config.example.yaml](config.example.yaml) to `~/.sessh/config.yaml`, or
set `BITBUCKET_WORKSPACE`, `JIRA_SITE`, and `CONFLUENCE_SITE` as needed.

```sh
sessh auth login bitbucket
sessh bitbucket pr list my-repo
sessh jira issue list ENG
sessh confluence page view 123456
sessh auth login teams
sessh teams capture             # Ctrl+C to stop
sessh teams messages --since 24h # reads local captures
```

Use `sessh <command> --help` for options. `--profile` selects an identity;
`--no-inherit` avoids copying cookies from `default`. `SESSH_HOME` overrides
`~/.sessh`. Profiles hold credentials and captures hold private messages:
keep both out of Git and bug reports. Report vulnerabilities privately through
GitHub Security Advisories; if unavailable, ask for a private contact in an issue.

## Agents

Install the [sessh skill](skills/sessh/SKILL.md) for Codex (requires repository access):

```sh
npx --yes skills add git@github.com:stefanm8/sessh.git --skill sessh -a codex -g -y
```

Replace `codex` with your agent, or omit `-a codex -y` to choose interactively.
The skill includes CLI installation, browser setup, and session checks.

## Develop

`make build` builds `bin/sessh`; `make check` runs formatting, module, vet, and
race checks. CI also verifies installation on macOS/Linux. `make dist VERSION=X.Y.Z`
builds four release archives. Push `vX.Y.Z` from `main` to publish after CI passes;
suffixed tags are prereleases. Live service login still needs manual validation.
Keep the Playwright versions in `session/driver.go` and `scripts/setup-browser.sh`
matched to the Go binding when updating it.

[MIT](LICENSE). [Third-party licenses](THIRD_PARTY_NOTICES.md).
