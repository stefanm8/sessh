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
sessh teams capture             # starts a daemon; returns to your shell
sessh teams capture status      # starting, running, or a login/error message
sessh teams capture monitor     # watch state, counts, and last successful work
sessh teams capture status --json # machine-readable status
sessh teams capture logs -f     # follow progress; Ctrl+C only stops log viewing
sessh teams messages --since 24h # starts capture if needed, waits, then reads
sessh teams capture stop        # clean shutdown
```

When a command finds its session signed out or expired, it opens a browser
window at the service's login page, waits up to five minutes for you to sign in,
and then finishes. Sign-in messages go to stderr, so `--json` output stays clean.
Commands started meanwhile share that one window. `--no-login` fails immediately
instead. Background workers never open a window; a login that needs the profile
of a running `daemon` or `capture` worker pauses it and restarts it afterwards.

All plugins share a 15-second authentication-check budget across requests,
navigation, and polling. Known sign-in pages stop the check immediately;
network errors and timeouts are reported without opening a login window.
Interactive sign-in keeps its separate five-minute deadline.

Teams capture keeps its browser open, so it needs no separate daemon. Repeating
`capture` or `capture start` reuses the running capture. Capture opens and scrolls chats to load
history, which marks them read; add `--no-scroll` to only read cached messages.
Use `sessh teams capture --once` for a single foreground refresh, or
`sessh teams capture --foreground` for continuous foreground capture with Ctrl+C
to stop. `--background` / `-d` remains an alias for the default background mode.
Background logs and status are private files under `~/.sessh/run`;
`logs` shows the latest run. `status` reports browser startup separately from
active capture, and retains failures after a worker exits. It includes the PID,
start time, message and conversation counts, and the last successful poll and
harvest. `monitor` (or `status --watch`) refreshes every two seconds; use
`--interval 5s` to change that or `--json` for one JSON object per refresh.
Ctrl+C ends monitoring while capture continues. Monitoring stays open across
worker failures and restarts. Use the same `--profile` as the capture command.

`sessh teams messages` (also `msgs`) starts capture automatically when needed,
even when saved messages already exist. It waits for the daemon's first
successful IndexedDB harvest before querying; an empty completed harvest is a
valid empty result. An already-ready daemon is reused. Startup progress goes to
stderr so JSON output stays clean. Use `--capture-timeout 3m` to change the
default two-minute wait, and `--no-scroll` to disable chat scrolling for a newly
started capture. Existing workers keep their original options.

`messages --offline` reads saved data without starting or waiting for capture.
An explicit `--source /path/to/messages.jsonl` also stays offline. Other Teams
queries (`search`, `show`, `stats`, and directory queries) remain local reads.
If capture fails or writes a different file than the query's configured store,
the command reports the problem instead of silently returning stale data.
Cancelling a waiting query leaves the shared daemon running.

Browser-backed commands automatically leave a headless browser and its tabs
open for 30 minutes after the last request. Later commands reuse the same
browser, including across plugins sharing a profile. Requests still fetch
current data; query results are not cached. Concurrent first commands start
one worker per profile.

```sh
sessh jira issue list --limit 25          # starts or reuses the warm browser
sessh daemon status jira                 # see its state and PID
sessh daemon logs jira                   # inspect startup or browser errors
sessh daemon stop jira                   # close it now
sessh jira issue list --keep-alive 10m    # idle timeout for a newly started worker
sessh jira issue list --keep-alive 0      # keep a newly started worker indefinitely
sessh jira issue list --no-keep-alive     # do not start a background browser
```

Set `SESSH_KEEP_ALIVE=10m` for a persistent default, or `SESSH_KEEP_ALIVE=off`
to disable automatic startup. An explicit `--keep-alive` overrides the environment;
`--no-keep-alive` disables startup regardless. An already-running browser retains
its original timeout and is still reused; stop it to apply a new timeout.
Use the same `--profile` when checking or stopping a custom profile.
`sessh auth logout` and `sessh auth import` close the target profile's detached
daemon before changing its saved login; foreground commands and captures must
be stopped separately. Import overlays saved cookies and localStorage; for an
account switch, log out of the target before importing from a separate source
profile.

`sessh daemon start jira` (or `bitbucket` or `confluence`) explicitly keeps a
browser open until stopped, with `--idle 30m` available for an idle timeout.
Plain `sessh daemon jira` runs in the foreground. Capture allows one worker per
profile and one writer per messages file.

### Jira daily work

Jira commands use your configured `JIRA_SITE` (or `plugins.jira.site`) and the
shared Atlassian browser session, which stays warm automatically for repeated
queries. A bare issue list shows your assigned work.
Use a finite limit for quick daily searches:

```sh
sessh jira issue list --limit 25
sessh jira issue list PROJECT --status "In Progress" --type Bug --priority High --limit 25
sessh jira issue list PROJECT --unassigned --label triage --limit 25
sessh jira issue list --reporter me --text 'login' --limit 25
sessh jira issue list --jql 'project = PROJECT ORDER BY priority DESC' --limit 25 --json
sessh jira issue view PROJECT-123 --fields summary,customfield_10000 --json
sessh jira issue comments PROJECT-123 --limit 20
sessh jira issue history PROJECT-123 --limit 20
sessh jira issue transitions PROJECT-123 --json
```

`--jql` takes precedence over generated filters and controls ordering. Otherwise
results are ordered by most recently updated. `--unassigned` and `--assignee`
are mutually exclusive for generated queries. `--text` takes a Jira text-search
expression. `--fields` accepts comma-separated Jira field IDs; human output also
requests its standard display fields. JSON preserves returned custom fields.
Empty lists are `[]`. Existing issue/project list defaults remain unlimited;
new paginated lists default to 50, and `--limit 0` explicitly reads all available
results. Pagination stops with an error if Jira repeats pages or exceeds the
2,000-page safety bound. Enhanced issue search is used; the retired legacy
search endpoint is no longer retried.

Discover IDs and the fields allowed by your tenant before making changes:

```sh
sessh jira user search 'Example User' --limit 10 --json
sessh jira field list --limit 50
sessh jira project types PROJECT
sessh jira issue create-meta PROJECT 10001 --json
sessh jira issue edit-meta PROJECT-123 --json
sessh jira board list --project PROJECT --type scrum --limit 10
sessh jira board view 42
sessh jira board issues 42 --limit 25
sessh jira sprint list 42 --state active,future
sessh jira sprint view 84
sessh jira sprint issues 84 --limit 25
```

IDs above are placeholders. User search returns active users within Jira's first
1,000 matching-user range, subject to account visibility. Transitions and edit
metadata return the complete metadata object/list. Board and sprint commands
require Jira Software access; sprint listings require a compatible board.

Create, edit, assign, transition, and comment commands accept `--dry-run`. It
prints the exact method, path, and JSON payload without writing. Resolving `me`,
a named issue type, or an available transition can read identity/metadata.
Remove `--dry-run` to perform the requested change; no second prompt appears.

```sh
sessh jira issue create PROJECT --type 10001 --summary 'Improve sign-in' --description-file description.txt --labels triage --dry-run
sessh jira issue edit PROJECT-123 --summary 'Investigate sign-in delay' --priority High --dry-run
sessh jira issue assign PROJECT-123 me --dry-run
sessh jira issue assign PROJECT-123 unassigned --dry-run
sessh jira issue transition PROJECT-123 31 --fields-json transition-fields.json --dry-run
printf '%s\n' 'Checked the latest build.' | sessh jira issue comment add PROJECT-123 --body-file - --dry-run
```

Create accepts an issue type ID or a unique matching type name. Assignees are
explicit account IDs, current-user aliases (`me`, `@me`, `current`,
`currentuser`), or `unassigned`; display names are never guessed. A transition
uses an available transition ID from `issue transitions`, not a status name.
Use `--fields-json` for fields required by that transition. Jira validates the
resulting state, including values already present on the issue.

Descriptions and comments are plain text, converted to Atlassian Document
Format (ADF). Use `--description-file PATH` or `--body-file PATH` for multiline
text, or `-` to read stdin. Inline equivalents are `--description TEXT` and
`--body TEXT`. Only one input may consume stdin. Files must be valid UTF-8 and
at most 1 MiB.

Create/edit/transition `--fields-json PATH` reads a direct fields object such as
`{"customfield_10000": 3}` or `{"resolution": {"id": "1"}}`; `-` reads stdin.
It does not accept a whole request envelope. Duplicate field keys or a field
specified by both a named flag and the JSON file are errors. Omitted fields
stay unchanged. On edit, `--description ''` clears the description,
`--labels ''` clears labels, and `--parent ''` uses Jira's documented parent
removal operation (where supported by the issue type). Structured JSON can
supply explicit `null` or empty arrays where Jira allows them; `parent: null`
on edit is translated to the same documented removal operation. For rich text
in structured fields, supply ADF objects rather than Markdown strings.

Writes require the corresponding Jira project permissions. Authentication is
recovered before a write when possible, but a command is never replayed after
a write was attempted. On an uncertain network or server failure, inspect the
issue before trying again. Successful 204 responses produce a confirmation
(or `{"success":true,"key":"PROJECT-123"}` with `--json`) without a follow-up
read. Created issues/comments return the Jira response with `--json`.

The [v0.2.0 scope and acceptance tracker](docs/jira-v0.2.0.md) records tested
features, API references, and deferred work.

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
