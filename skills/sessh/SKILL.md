---
name: sessh
description: Install and use sessh to read Bitbucket, Jira, Confluence, and Microsoft Teams and perform requested Jira issue updates through saved browser sessions.
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

Browser-backed commands automatically keep the headless browser and tabs open
for 30 minutes of inactivity. Repeated requests use the same profile worker;
they still fetch current service data. Inspect it with `sessh daemon status
<service>` or `sessh daemon logs <service>`, and close it with `sessh daemon stop
<service>` using the same `--profile`. There is no need to start a daemon first.
`--keep-alive 10m` or `SESSH_KEEP_ALIVE=10m` sets the idle timeout for a newly
started worker; `0` keeps it indefinitely. `--no-keep-alive` or
`SESSH_KEEP_ALIVE=off` prevents automatic startup while still reusing any existing
holder. An existing worker retains its original timeout until stopped.

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


## Jira workflows

Use finite limits for daily queries: `sessh jira issue list --limit 25` shows
current-user work. A project argument and `--status`, `--assignee`, `--type`,
`--priority`, `--label`, `--reporter`, `--text`, and `--unassigned` compose
filters. Raw `--jql` overrides them and is the ordering-control option.
`--fields` selects field IDs on list/view; custom fields remain in `--json`.
Existing issue/project lists default to all results. New paginated lists default
to 50; `--limit 0` requests all available results, within the pagination safety
bound. Prefer a finite limit when investigating performance.

Read context with `issue comments KEY`, `issue history KEY`, and
`issue transitions KEY --json`. Discover valid IDs with `user search QUERY`,
`field list`, `project types PROJECT`, `issue create-meta PROJECT TYPE_ID`, and
`issue edit-meta KEY`. User search is constrained to Jira's first 1,000 matching
users. Use `board list --project PROJECT`, `board view ID`, `board issues ID`,
`sprint list BOARD_ID --state active,future`, `sprint view ID`, and
`sprint issues ID` for planning. IDs are tenant-specific; obtain them from
listings. All these reads support JSON; paginated lists support limits.

Perform Jira mutations only when the user's task authorizes the change. Use
`--dry-run` to inspect the exact method/path/payload first when useful; it never
writes, although identity/type/transition resolution can make read requests.
There is no extra interactive confirmation when dry-run is removed.

```sh
sessh jira issue create PROJECT --type TYPE_ID --summary 'Example task' --description-file description.txt --dry-run
sessh jira issue edit PROJECT-123 --summary 'Updated summary' --dry-run
sessh jira issue assign PROJECT-123 me --dry-run
sessh jira issue transition PROJECT-123 TRANSITION_ID --fields-json transition-fields.json --dry-run
sessh jira issue comment add PROJECT-123 --body-file comment.txt --dry-run
```

Assignees accept account IDs, current-user aliases, or `unassigned`; do not
infer a person from ambiguous names. Type names must resolve uniquely, and
transitions require IDs currently available on that issue. Description and
comment text becomes ADF. The `--description-file`, `--body-file`, and
`--fields-json` flags accept `-` for stdin; only one input can use stdin.
Inputs must be valid UTF-8 and at most 1 MiB.

`--fields-json` supplies a direct fields object, for example
`{"customfield_10000": 3}` or `{"resolution": {"id": "1"}}`, not a request
envelope. Repeating a field in named flags and JSON is an error. Edit omits
unchanged fields; `--description ''`, `--labels ''`, and `--parent ''` explicitly
clear values, with parent removal using Jira's supported update operation.
Rich text in JSON requires ADF. Inspect metadata for allowed custom values.

A mutation is never retried after its first write attempt. If a request times
out or the result is uncertain, inspect Jira before suggesting a retry. Keep
live service development tests read-only; use fixtures to verify writes.
