# sessh — session shell

One login, many tools. **sessh** is a Go CLI for Bitbucket, Jira, Confluence,
and Microsoft Teams, using persistent Chromium sessions to authenticate.
Log in through your browser with SSO and MFA, then use the saved session from
your terminal. Commands sharing a profile reuse its browser through a local
socket, including while a long-running Teams capture is active.

```sh
sessh bitbucket pr list my-repo
sessh jira issue list ENG
sessh confluence page view 123456
sessh teams messages --since 24h
```

- **Bitbucket Cloud:** repositories, pull requests, and CI checks.
- **Jira Cloud:** projects, issues, and JQL queries.
- **Confluence Cloud:** spaces, page trees, search, and Markdown output.
- **Microsoft Teams:** capture messages locally, then search and export them offline.

This is an early-stage, unofficial project. Browser integrations can change
when the upstream services change. macOS and Linux are supported; native
Windows is not supported because profile locking uses Unix `flock` and sockets.

The repository remains private while validation is in progress. A public
release requires completing the checks in [the release guide](docs/releasing.md).

## Install from source

Requires Go **1.25.4 or later**. From a checkout of this repository:

```sh
go build -o sessh ./cmd/sessh
./sessh --help
go install ./cmd/sessh
```

`go install` places the executable in `GOBIN`, or `$(go env GOPATH)/bin` when
`GOBIN` is unset. Add that directory to your `PATH`.

### Browser setup

Browser commands also need Node.js, npm, and the Playwright Chromium browser.
Install the exact driver version used by the Go bindings:

```sh
npm install --global playwright-core@1.61.1
playwright-core install chromium
```

On Linux, `playwright-core install --with-deps chromium` can also install the
required system packages (it may request administrator access). A graphical
session is required for the interactive `auth login` command.

sessh first uses an existing driver in the Playwright Go cache. Otherwise it
finds an exact-version `playwright-core` package near the executable, in
`node_modules` in the working directory or its ancestors, or in `npm root -g`.
It links that package and the Node executable into the cache. It does not
install packages or browsers when a command runs.

Set `PLAYWRIGHT_DRIVER_PATH` to use a custom driver directory containing
`package/cli.js` and a `node` executable. Offline Teams queries, help, and
`auth list` need neither Node nor Chromium.

## Configure and log in

Create `~/.sessh/config.yaml` using [config.example.yaml](config.example.yaml).
Set only the services you plan to use, with your own site and workspace:

```yaml
plugins:
  bitbucket:
    workspace: your-workspace
  jira:
    site: https://your-tenant.atlassian.net
  confluence:
    site: https://your-tenant.atlassian.net
```

You can instead set `BITBUCKET_WORKSPACE`, `JIRA_SITE`, and `CONFLUENCE_SITE`.
There are no built-in company accounts or tenant defaults. Site values must
be HTTP(S) origins, without `/wiki`, credentials, query strings, or fragments.
Environment variables override the configuration file.

```sh
sessh auth login bitbucket     # interactive browser login
sessh bitbucket repo list
sessh auth status bitbucket

sessh auth login teams
sessh teams capture            # Ctrl+C to stop
sessh teams messages -n 20
```

Bitbucket, Jira, and Confluence share the `atlassian` profile by default.
Teams uses `microsoft`. Shared Atlassian cookies allow silent SSO where your
organization permits it; individual services can still require another login.
Use `--profile work` or `SESSH_PROFILE=work` to select another profile.
New profiles inherit cookies and localStorage from `default` when it exists;
use `--no-inherit` to create an isolated identity.

## Local data

`SESSH_HOME` changes the entire state root (default `~/.sessh`):

```text
~/.sessh/
├── config.yaml
├── profiles/           Chromium profiles and sessh.json metadata
├── run/                local session sockets
└── teams/              messages.jsonl and conversations.jsonl
```

`TEAMS_DATA_DIR` overrides the Teams data directory. Teams queries read local
files without starting a browser; use `--source` to select an export.

Browser profiles contain authentication credentials, and captures can contain
private messages. Keep these files out of source control and public bug reports.
New profile directories, sockets, and capture files have owner-only permissions.
Existing files retain their permissions. See [SECURITY.md](SECURITY.md) for
reporting vulnerabilities and the local trust model.

## Documentation

- [Command reference](docs/cli.md): flags, configuration, formats, and scripting.
- [Migration guide](docs/migration.md): previous names and existing browser profiles.
- [Contributing](CONTRIBUTING.md): development workflow and plugin architecture.
- [Release preparation](docs/releasing.md): repository setup and release checks.

## Development

```sh
make check       # formatting, module consistency, vet, and race-enabled tests
make build       # bin/sessh
```

Tests use temporary files and synthetic fixtures; they do not require accounts
or a browser. Live service behavior requires separate manual verification.
GitHub Actions runs the same checks on macOS and Linux.

## License

[MIT](LICENSE). Dependencies retain their own licenses; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
