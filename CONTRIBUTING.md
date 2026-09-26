# Contributing to sessh

Use synthetic examples in code, tests, screenshots, and issues. Never submit
browser profiles, session cookies, captured messages, real tenant identifiers,
or organization-specific configuration.

## Local development

Go 1.25.4+ is required. Unit tests use fixtures and local test workers; they do
not need service accounts, Node.js, Chromium, or a network login.

```sh
make check
make install
sessh --help
```

`make e2e` checks the login flow end to end: it runs the binary and a real
Chromium against a local fake tenant, so it needs the browser setup below and a
display. Login windows open and close on their own. Run it when changing
sessions or login.

For live browser testing, install Node.js/npm, run `sh scripts/setup-browser.sh`,
and sign in to your own service. Use an isolated `SESSH_HOME` and
`--profile test --no-inherit` when appropriate. Copy `config.example.yaml` into
that directory and supply your own workspace/site. Do not commit the result.
Teams capture opens chats and marks them read unless `--no-scroll` is supplied.

Describe the behavior changed and the checks run in a pull request. Prefer
focused changes and regression tests for bugs. A brief sanitized CLI example
is more useful than a dump of an account response.

## Service providers

Providers live in `plugins/`; the shared contract lives in `plugin/plugin.go`.
Each provider implements `Name`, `Profile`, and `Command`, and registers itself
with `plugin.Register` in `init`. The CLI imports it in `cmd/sessh/main.go`.

Use `plugin.WithSession` for browser-backed commands and ordinary Cobra handlers
for local queries. Keep browser lifecycle and shared-session routing in `session`.
Configure tenant URLs, workspace names, and identities through environment
variables or `plugins.<name>` in the user's config file. Validate required
settings before launching a browser. Do not add a developer's organization as a
fallback. Use fictitious names and reserved example domains in test fixtures.
In documentation, label resource names, project keys, and IDs as placeholders.
Use `your-workspace` and `https://your-tenant.atlassian.net` when showing the
shape of user configuration; neither is a built-in account or default tenant.
Provider-wide URLs such as `bitbucket.org` and `teams.microsoft.com` are service
endpoints, not user configuration.

## Releases

`make dist VERSION=X.Y.Z` builds macOS/Linux archives for amd64/arm64, including
licenses, docs, and the example config. It also generates installer scripts and
SHA-256 checksums. `SESSH_REPOSITORY=owner/repo` lets the installer target a fork;
the Go module path remains the upstream import path unless a fork deliberately
renames its imports.

CI runs formatting, module, vet, race, packaging, and installation checks on
macOS and Linux. Tags `vX.Y.Z` on `main` publish after those checks pass; suffixed
tags are prereleases. Tagging publishes through the configured GitHub Actions
workflow, so only tag when the contents are ready for distribution.

Before the first public release, review the repository and its history for
private material, confirm the dependency notices, enable private vulnerability
reporting in the repository settings, and check the public release download
links without authentication. A source cleanup does not change repository
visibility. Live SSO/MFA and vendor web UI behavior require manual validation.
