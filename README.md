# sessh — session shell

A Go CLI for Bitbucket, Jira, Confluence, and Microsoft Teams. Log in through
Chromium with SSO/MFA, then reuse that session from your terminal. Commands
sharing a profile reuse its browser, including during Teams capture.

```sh
sessh bitbucket pr list my-repo
sessh jira issue list ENG
sessh confluence page view 123456
sessh teams messages --since 24h
```

Early-stage, unofficial project for macOS and Linux (amd64/arm64).
The repository and releases remain private during validation.

## Install

With [GitHub CLI](https://cli.github.com/) authenticated to an account with
repository access:

```sh
gh auth login
gh release download v0.1.0 -R stefanm8/sessh -p install.sh -O - | sh
```

The installer verifies SHA-256 checksums and installs `~/.local/bin/sessh`.
Add `~/.local/bin` to your `PATH`. Use `--bin-dir /your/bin` to change the
location, or `--version v0.1.0` to select a release. You can also download
archives from [Releases](https://github.com/stefanm8/sessh/releases).

Browser commands require Node.js, npm, and Chromium. Set up the pinned driver:

```sh
gh release download v0.1.0 -R stefanm8/sessh -p setup-browser.sh -O - | sh
```

Setup installs in your user cache; Linux may need the system libraries listed
by the script. Interactive login needs a graphical session. Help and offline
Teams queries work without a browser.

## Configure and use

Copy [config.example.yaml](config.example.yaml) to `~/.sessh/config.yaml` and
set your services. Alternatively, set `BITBUCKET_WORKSPACE`, `JIRA_SITE`, and
`CONFLUENCE_SITE`; environment variables override the file.

```sh
sessh auth login bitbucket
sessh bitbucket repo list
sessh auth login teams
sessh teams capture             # Ctrl+C to stop
sessh teams messages -n 20
```

Atlassian services share the `atlassian` profile; Teams uses `microsoft`.
Use `--profile work` for another profile and `--no-inherit` to avoid inheriting
cookies from `default`. `SESSH_HOME` changes the state root (`~/.sessh`);
`TEAMS_DATA_DIR` overrides capture storage. Profiles contain credentials and
captures contain private messages: keep both out of Git and bug reports.

See the [command reference](docs/cli.md), `sessh <command> --help`,
[migration notes](docs/migration.md), and [security policy](SECURITY.md).

## Develop and release

Requires Go 1.25.4 or later:

```sh
make build                     # bin/sessh
make install                   # Go's bin directory
sh scripts/setup-browser.sh    # optional browser setup from source
make check                     # formatting, modules, vet, race tests
make dist VERSION=0.1.1         # four platform archives + checksums in dist/
```

CI runs checks and tests native installation on macOS and Linux. Push a
`vX.Y.Z` tag on a commit from `main` to publish a GitHub release with generated
notes and all assets; suffixed tags become prereleases. Live service login
still needs manual validation. Publishing a release keeps the repository private.

Contributions: run `make check`, describe the change and validation, and use
synthetic fixtures. Keep the Playwright Go binding, `session.DriverVersion`,
and `scripts/setup-browser.sh` version in sync when updating dependencies.

[MIT](LICENSE). Dependency licenses: [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
