# sessh — session shell

A CLI for Bitbucket, Jira, Confluence, and Microsoft Teams using persistent
Chromium sessions. Log in with SSO/MFA once, then work from your terminal.
macOS/Linux, amd64/arm64. MIT licensed.

## Install

Install a release with curl:

```sh
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/install.sh | sh
# Browser commands also need Node.js, npm, and Chromium:
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/setup-browser.sh | sh
```

The installer checks SHA-256, installs `~/.local/bin/sessh`, and configures PATH
for new bash/zsh terminals. Then use `sessh` from any directory. If your current
terminal needs a PATH update, the installer prints the command to activate it.

From a source checkout (Go 1.25.4+):

```sh
make install
sessh --help
```

`make install` builds and installs the current checkout to the same location as
the release installer. `sh install.sh --source` is equivalent. For browser
commands, also run `sh scripts/setup-browser.sh` once (requires Node.js/npm).
Add `SESSH_AUTOSTART=teams` to `make install` to enable Teams capture at shell
startup, or use `sh install.sh --source --autostart teams`.

`sh install.sh v0.1.0` installs a specific release; `SESSH_INSTALL_DIR` sets the
destination and `SESSH_SHELL_RC` selects a custom shell startup file. `sessh setup`
can repair the PATH entry later. Set `SESSH_REPOSITORY=owner/repo` to install
from a fork. Public downloads need no GitHub login; authenticated `gh` is a
fallback for private repositories. Download binaries directly from
[Releases](https://github.com/stefanm8/sessh/releases).

## Use

Copy [config.example.yaml](config.example.yaml) to `~/.sessh/config.yaml`, or
set `BITBUCKET_WORKSPACE`, `JIRA_SITE`, and `CONFLUENCE_SITE` as needed.
Replace `your-workspace` and `your-tenant` with your own values. Jira and
Confluence need the site origin only, without `/jira` or `/wiki`.
The repository name `example-repo`, project key `PROJECT`, and page ID `123456`
below are placeholders; use identifiers from your account.

```sh
sessh auth login bitbucket
sessh bitbucket pr list example-repo
sessh jira issue list PROJECT
sessh confluence page view 123456
sessh auth login teams
sessh teams capture start       # runs after you close the terminal
sessh teams capture status      # starting, running, or a login/error message
sessh teams capture logs -f     # follow progress; Ctrl+C only stops log viewing
sessh teams messages --since 24h # reads local captures
sessh teams capture stop        # clean shutdown
```

When a command finds its session signed out or expired, it opens a browser
window at the service's login page, waits up to five minutes for you to sign in,
and then finishes. Sign-in messages go to stderr, so `--json` output stays clean.
Commands started meanwhile share that one window. `--no-login` fails immediately
instead. Background workers never open a window; a login that needs the profile
of a running `daemon` or `capture` worker pauses it and restarts it afterwards.

Teams capture keeps its browser open, so it needs no separate daemon. Repeating
`start` reuses the running capture. Capture opens and scrolls chats to load
history, which marks them read; add `--no-scroll` to only read cached messages.
Use `sessh teams capture --once` for a single refresh, or `sessh teams capture`
for foreground capture with Ctrl+C to stop. `--background` / `-d` is a shortcut
for `start`. Background logs and status are private files under `~/.sessh/run`;
`logs` shows the latest run. `status` reports browser startup separately from
active capture, and retains failures after a worker exits.

Keep other browser sessions ready with `sessh daemon start jira` (or `bitbucket`
or `confluence`), then use `sessh daemon status jira`, `logs jira`, or `stop jira`.
The daemon stays open until stopped; `--idle 30m` optionally exits after 30 minutes
without client activity. Plain `sessh daemon jira` still runs in the foreground.
Use the same `--profile` when starting, checking, or stopping a custom profile.
Capture allows one worker per profile and one writer per messages file.

### Start automatically when opening a shell

After signing in, enable a startup entry for your current bash or zsh shell:

```sh
sessh autostart enable teams            # Teams capture; optionally --no-scroll
sessh autostart status
sessh autostart disable                 # remove entry; running capture stays up
```

The installer can add the same entry:

```sh
curl -fsSL https://github.com/stefanm8/sessh/releases/latest/download/install.sh | sh -s -- --autostart teams
# Or: SESSH_AUTOSTART=teams sh install.sh
```

Use `--autostart jira` (or another plugin) for its browser daemon. There is one
managed entry; enabling another plugin replaces it. The entry starts work only
in interactive shells after a saved login exists, runs silently without delaying
the prompt, and does not start duplicates. It uses the installed binary's absolute
path and the selected profile. Open a new shell to activate it; this is shell
startup, not a machine boot service. An existing worker keeps its original flags
until stopped and started again. Failed workers can be retried with `start` after
fixing the error shown by `status` or `logs`.

The default file is `~/.bashrc` for bash or `${ZDOTDIR:-$HOME}/.zshrc` for zsh.
Use `--rc /path/to/rc` (installer: `SESSH_SHELL_RC`) to choose explicitly. Bash
login shells must source `.bashrc`. Disabling the entry preserves your other
shell settings; use `capture stop` or `daemon stop` separately to stop a worker.

Use `sessh <command> --help` for options. `--profile` selects an identity;
`--no-inherit` avoids copying cookies from `default`. `SESSH_HOME` overrides
`~/.sessh`. Profiles hold credentials and captures hold private messages:
keep both out of Git and bug reports. See [SECURITY.md](SECURITY.md) for private
vulnerability reporting.

## Agents

Install the [sessh skill](skills/sessh/SKILL.md) for Codex:

```sh
npx --yes skills add https://github.com/stefanm8/sessh.git --skill sessh -a codex -g -y
```

Replace `codex` with your agent, or omit `-a codex -y` to choose interactively.
The skill includes CLI installation, browser setup, and session checks.

## Develop

`make build` builds `bin/sessh`; `make check` runs formatting, module, vet, and
race checks. CI also verifies installation on macOS/Linux. `make dist VERSION=X.Y.Z`
builds four release archives. Push `vX.Y.Z` from `main` to publish after CI passes;
suffixed tags are prereleases. Live service login still needs manual validation.
Keep the Playwright versions in `session/driver.go` and `scripts/setup-browser.sh`
matched to the Go binding when updating it. See [CONTRIBUTING.md](CONTRIBUTING.md)
for provider development, configuration, and release checks.

[MIT](LICENSE). [Third-party licenses](THIRD_PARTY_NOTICES.md).
