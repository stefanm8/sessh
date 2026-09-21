# sessh command reference

See the [README](../README.md) for installation and initial configuration.
All accounts, names, and tenant URLs in the examples are fictional.

## Profiles

A login belongs to a **profile** (a Chromium user-data directory under
`~/.sessh/profiles/<name>`), not to a plugin. `bitbucket`, `jira` and `confluence`
all declare `atlassian`, so they are one login; `teams` declares `microsoft`.

What survives between commands is that directory — the cookie jar — not a
process. Kill every `sessh` process and the next command is still authenticated;
delete `~/.sessh/profiles/atlassian` and no daemon can save you.

```console
$ sessh auth list
PROFILE    SEEDED FROM             PLUGINS                      ORIGINS                                              IDENTITY            VERIFIED       IN USE BY
atlassian  /path/to/browser-profile  bitbucket, confluence, jira  https://bitbucket.org https://acme.atlassian.net  Alex Example   4 minutes ago
microsoft  /path/to/teams-profile      teams                        https://teams.microsoft.com                          alex@example.com  2 hours ago    pid 4711: sessh teams capture
work       default                 jira                         https://acme.atlassian.net                        Alex Example   1 day ago

3 profiles under /Users/you/.sessh
```

`seeded from` is the parent profile's name, or — for an imported directory —
its last two path segments. `in use by` is the current lock holder, so a busy
profile is something you can look up rather than discover by colliding.

### Which profile a command uses

First hit wins:

1. `--profile <name>` on the command
2. `SESSH_PROFILE`
3. `~/.sessh/config.yaml` → `plugins.<plugin>.profile`
4. `~/.sessh/config.yaml` → `profile`
5. the plugin's compiled-in name (`atlassian`, `microsoft`)

Only the *directory* is overridable. Origin, login URL and the verification hook
always come from the plugin — they describe the service, not the identity. Two
plugins resolving to the same name share one cookie jar and one lock.

### New profiles inherit `default`

A profile that does not exist yet is created on first use and seeded from
`default`: the parent's cookies and localStorage are copied in before the first
navigation. Deliberately *not* a directory copy — that would drag along the
`SingletonLock`, the caches, and IndexedDB, and IndexedDB is exactly what must
not be inherited (the teams plugin's captured messages live there). Inheritance
carries credentials, never data, and it is a one-time copy: afterwards the
profiles diverge.

```sh
sessh auth login bitbucket --profile default # -> profiles/default
sessh jira issue list --profile work        # -> profiles/work, seeded from default,
                                          #    so silent SSO mints the tenant session
sessh jira issue list --profile work --from atlassian   # seed from something else
sessh jira issue list --profile work --no-inherit       # start empty
```

Seeding reads the parent's storage state through the running process when the
parent is busy, so it never requires stopping a capture. A missing parent is a
note on stderr, not an error:

```
sessh: new profile "work" starts empty (no "default" profile to inherit from)
```

### Global flags

Every command accepts these (they are persistent flags on the root):

| Flag | Meaning |
| --- | --- |
| `--profile <name>` | browser profile to use (default: the plugin's own) |
| `--from <name>` | when creating a profile, seed it from this one instead of `default` |
| `--no-inherit` | when creating a profile, start it empty |
| `--login` | open a headed window if the session has lapsed, instead of failing |
| `--verbose` | print session bookkeeping to stderr |

## Commands

### auth

| Command | Notes |
| --- | --- |
| `sessh auth login [plugin]` | opens a browser; `--timeout <d>` (default `5m`) |
| `sessh auth status [plugin]` | headless verify; no argument = every plugin; exits 1 if any is not logged in |
| `sessh auth logout [plugin]` | deletes the profile directory; honours `--profile` |
| `sessh auth import <dir>` | seed a profile from an existing Chromium user-data dir; `--into <profile>` (default: the profile the plugins resolve to) |
| `sessh auth list` | profiles, what seeded them, which plugins use them, who holds them |

```console
$ sessh auth status
bitbucket   ✓ logged in as Alex Example  (profile atlassian)
confluence  ✓ logged in as Alex Example  (profile atlassian)
jira        ✓ logged in as Alex Example  (profile atlassian)
teams       session expired (profile microsoft) — run: sessh auth login teams
$ echo $?
1
```

```console
$ sessh auth status jira
jira        ✓ logged in as Alex Example  (profile atlassian)
  Origin:  https://acme.atlassian.net
  Profile: /Users/you/.sessh/profiles/atlassian
```

`auth logout` names the plugins that shared what it just deleted:

```console
$ sessh auth logout jira
✓ Logged out of profile atlassian (local session cleared).
  This profile was shared with: bitbucket, confluence, jira
```

### daemon

Sharing does not need a daemon. Every command that launches a browser serves its
profile on `~/.sessh/run/<profile>.sock` for as long as it runs, so a second
command on the same profile routes through the first instead of colliding with
its `SingletonLock` — including commands from a different plugin. A running
`sessh teams capture` is just a process that holds the lock for hours:

```console
$ sessh teams capture &
$ sessh auth status teams
teams       ✓ logged in as Alex Example  (profile microsoft, via a running sessh process)
```

`sessh daemon` is only about *warmth*: it keeps a profile open past the last
command so later ones skip the ~1–2 s browser launch. Start one when you are
about to run a burst of commands against the same profile; skip it otherwise.
Nothing depends on it for correctness.

```sh
sessh daemon                 # the default profile's plugin
sessh daemon jira            # hold `atlassian` open
sessh daemon teams --idle 2h # exit after 2h with no signal
```

| Flag | Meaning |
| --- | --- |
| `--plugin <name>` | which plugin's profile to hold open (same as the positional arg) |
| `--idle <duration>` | exit after this long (`0` = run until interrupted) |

```console
$ sessh daemon jira
✓ Serving profile atlassian on /Users/you/.sessh/run/atlassian.sock
  Ctrl+C to stop.
```

While that runs, `sessh bitbucket`, `sessh jira` and `sessh confluence` commands all
reuse its browser — same profile, one launch.

### bitbucket

Alias: `bb`. Set `BITBUCKET_WORKSPACE` or `plugins.bitbucket.workspace`; there is no default workspace.

| Command | Args | Flags |
| --- | --- | --- |
| `repo list` (`ls`) | — | `--json` `--limit <n>` `--web` |
| `repo view` (`show`) | `<repo>` | `--json` `--web` |
| `pr list` (`ls`) | `<repo>` | `--json` `--limit <n>` `--state <s>` `--web` |
| `pr view` (`show`) | `<repo> <id>` | `--json` `--web` |
| `pr checks` (`check`, `pipelines`) | `<repo> <id>` | `--json` `--limit <n>` `--web` |

`repo`/`pr` also answer to `repos`/`prs`. `--state` takes `open` (default),
`merged`, `declined`, `closed` or `all`.

```console
$ sessh bitbucket pr list example-repo
ID   TITLE                                       STATE  BRANCH                    AUTHOR             UPDATED
#412 Fix flaky deploy step on retry              OPEN   fix/deploy-retry → main   Alex Example  2 hours ago
#409 Bump base image to node:22                  OPEN   chore/node-22 → main      Sam Example        1 day ago

2 pull requests in acme/example-repo
```

```sh
sessh bitbucket repo list --limit 20 --json
sessh bitbucket pr list example-repo --state all --limit 50
sessh bitbucket pr view example-repo 412
sessh bitbucket pr checks example-repo 412          # exits 0 / 1 / 8 — see Scripting
```

`pr checks` resolves the PR's source commit and merges two Bitbucket surfaces:
commit build statuses (external CI — Azure Pipelines, Jenkins) and native
Pipelines runs for that commit, de-duplicated by URL so a pipeline that also
publishes a status is counted once. `--json` keeps both raw arrays plus the
normalized `checks` list.

### jira

Set `JIRA_SITE` or `plugins.jira.site`; there is no default tenant.

| Command | Args | Flags |
| --- | --- | --- |
| `project list` (`ls`) | — | `--json` `--limit <n>` `--web` |
| `project view` (`show`) | `<key>` | `--json` `--web` |
| `issue list` (`ls`) | `[project]` | `--json` `--limit <n>` `--status <s>` `--assignee <a>` `--jql <q>` `--web` |
| `issue view` (`show`) | `<KEY-123>` | `--json` `--web` |

`project` also answers to `projects`; `issue` to `issues`, `task` and `tasks`.

```console
$ sessh jira issue list ENG --status "In Progress"
KEY      SUMMARY                                    STATUS       TYPE   ASSIGNEE           UPDATED
ENG-482  Split the deploy pipeline per environment  In Progress  Story  Alex Example  3 hours ago
ENG-471  Retry transient artifact pushes            In Progress  Bug    Sam Example        1 day ago

2 issues
```

```sh
sessh jira issue list                                   # your issues
sessh jira issue list ENG --assignee me --limit 20
sessh jira issue list --jql 'project = ENG AND status = Done ORDER BY updated DESC'
sessh jira issue view ENG-482 --json
```

`issue list` with no project, status, assignee or JQL defaults to
`assignee = currentUser()`: Jira's modern search endpoint rejects an unbounded
query, so something must bound it.

### confluence

Alias: `wiki`. Set `CONFLUENCE_SITE` or `plugins.confluence.site`; the wiki mount is
`/wiki`.

| Command | Args | Flags |
| --- | --- | --- |
| `space list` (`ls`) | — | `--json` `--limit <n>` `--web` |
| `space view` (`show`) | `<KEY>` | `--json` `--web` |
| `page list` (`ls`) | `[SPACE]` | `--json` `--limit <n>` (default 50) `--space <KEY>` `--web` |
| `page view` (`show`, `get`) | `<ref>` | `--json` `--format <f>` `--out <file>` `--body-only` `--web` |
| `page children` (`child`) | `<ref>` | `--json` `--limit <n>` |
| `page tree` | `<SPACE\|ref>` | `--json` `--depth <n>` (default 3) `--space <KEY>` |
| `page attachments` (`files`) | `<ref>` | `--json` `--limit <n>` |
| `search` (`find`) | `<text>` | `--json` `--limit <n>` (default 25) `--space <KEY>` `--type <t>` `--cql <q>` `--web` |

`page tree` is also mounted as `space tree`. `--format` accepts `md` (default),
`storage` (raw XHTML), `view` (server-rendered HTML), `adf`, or `json` — the
last returns metadata plus both the raw body and the converted Markdown, which
is the one you want when scripting. `--type` accepts `page` (default),
`blogpost`, `comment`, `attachment`.

A `<ref>` is anything that identifies a page — a copied URL, a tiny link, a page
id, `SPACE:Title`, or free text that resolves to the best match:

```sh
sessh confluence page view https://acme.atlassian.net/wiki/spaces/ENG/pages/123456/WIP+RFC+0010
sessh confluence page view 123456 --out rfc-0010.md
sessh confluence page view "release management sdlc" --body-only
sessh confluence page tree ENG --depth 2
sessh confluence search "deploy" --space ENG --limit 10
sessh confluence search --cql 'label = "rfc" AND type = page'
```

`page view` writes Markdown to stdout with a metadata header (title, space,
version, breadcrumb, labels, URL); `--body-only` drops it, and `--out` writes to
a file with colors stripped. Unquoted multi-word refs are joined, so
`sessh confluence page view ENG:WIP RFC 0010` works without quoting.

Note that an anonymous Confluence caller gets 403 rather than 401, so an empty
listing can mean "not logged in" — `sessh auth status confluence` is the quick
check.

### teams

`sessh teams capture` is the long-running half: it holds the `microsoft` profile
open, reads Teams' own IndexedDB on a timer — the only place a complete message
body reliably lands, since live WebSocket traffic carries notification envelopes
— and appends what is new to `messages.jsonl`, keeping `conversations.jsonl` in
step. Between harvests it opens and scrolls conversations to widen what Teams
keeps cached. The amount of available history depends on what Teams has loaded.
Ctrl+C is a clean shutdown.

The query commands are read-only over the captured JSONL files and
**never open a browser** — so querying works while capture holds the profile,
and works with no profile at all.

```sh
sessh auth login teams
sessh teams capture                  # leave running
sessh teams capture --once           # one harvest, then exit
```

`capture` flags:

| Flag | Meaning |
| --- | --- |
| `--data-dir <dir>` | directory holding the capture files (default `$TEAMS_DATA_DIR` or `$SESSH_HOME/teams` (default `~/.sessh/teams`)) |
| `--out <file>` | messages file to append to (default `<data-dir>/messages.jsonl`) |
| `--headed` | show the browser window (needs the profile to itself) |
| `--no-scroll` | do not open and scroll conversations to widen the harvest |
| `--idb-interval <d>` | how often to re-read Teams' IndexedDB (default `1m`, floor 5s) |
| `--poll-interval <d>` | how often to drain the in-page live capture (default `5s`, floor 1s) |
| `--once` | harvest IndexedDB once and exit |
| `--verbose` | log every captured message |

The query commands:

| Command | Args | Flags beyond the shared filter set |
| --- | --- | --- |
| `messages` (`msgs`) | — | `--sort` `-o/--order` `-n/--limit` `--offset` `--head` `--tail` + output |
| `search` | `<text>` | same as `messages` |
| `conversations` (`convos`) | — | `--sort last\|count\|title` `-o/--order` `-n/--limit` `--offset` + output |
| `senders` | — | `-o/--order` `-n/--limit` + output |
| `show` | `<id>` | `--raw` `--pretty` `-f/--format` `--fields` |
| `stats` | — | `-f/--format` `--pretty` `--count` |
| `schema` | — | `-f/--format` `--pretty` |

Shared filter flags: `-c/--conversation <id|name>`, `-s/--sender <name>`,
`--since <when>`, `--until <when>`, `-t/--type <messageType>`,
`-g/--grep <pattern>`, `--capture-source <idb|ws|rest>`, `--only-messages`
(default on), `--include-system`. Output flags: `-f/--format`, `--fields`,
`--raw`, `--count`, `--pretty`. Every query command except `schema` also takes
`--source <path>`.

`messages`/`search` default to `--sort time --order desc --limit 50`;
`conversations` to `--sort last --order desc` and no limit; `senders` to
`--order desc` and no limit. `--limit 0` and `--limit all` both mean unlimited.
`--head N` / `--tail N` are sugar for `--order asc|desc --limit N`. Sorting is a
total order — ties break by message id — so identical input produces
byte-identical output.

Repeating a filter flag ORs within itself (two `--sender` means either sender)
and different flags AND across each other; `--grep` is the exception — repeating
it ANDs, because a second pattern is narrowing. `--only-messages` is on by
default and drops `ThreadActivity/*` and `Event/*` membership churn and call
notifications; an explicit `--type ThreadActivity/…` overrides it rather than
silently returning nothing.

`--conversation` takes a full `conversationId`, an id prefix, or — when
`conversations.jsonl` is present — a chat, meeting, team or channel name, or a
member's name. A term matching more than one conversation is a usage error that
lists the candidates rather than guessing:

```console
$ sessh teams messages -c backend
ambiguous --conversation "backend" matches 2 conversations:
  - 19:a1b2…@thread.v2  (Backend Guild)
  - 19:c3d4…@thread.v2  (Backend / Deploys)
Narrow the term or pass a full conversationId
```

```sh
sessh teams messages --tail 20
sessh teams messages -c "Release Engineering" --since 7d
sessh teams messages -s "Sam" --since 24h -f json
sessh teams search "deploy failed" --since 3d --fields time,sender,body -f csv
sessh teams stats --since today
sessh teams show 1781773639693 --raw --pretty
sessh teams schema -f json | jq .          # field docs + valid --type values
```

Output is `table` on a terminal and `json` when piped; `-f` overrides with
`json | jsonl | table | text | csv`. JSON is compact unless `--pretty`, keeps a
stable key order, and carries a derived ISO-8601 `time` field so nothing
downstream has to parse Teams' epoch-milliseconds-as-a-string. `--fields`
projects columns (`time,sender,body`); `sessh teams schema` lists every field name
and the known `messageType` values, which is the same list `--type` accepts.

Exit codes are a contract:

| Exit | Meaning |
| --- | --- |
| `0` | success, **including an empty result** |
| `2` | usage error — bad flag, bad date, ambiguous `--conversation` |
| `3` | no data file to read |
| `4` | `show <id>`: no message with that id |

"Nothing matched your filter" (0) and "that id does not exist" (4) are different
answers and do not share a code.

`--since`/`--until` accept four shapes, and reject anything else loudly rather
than guessing:

```
ISO-8601:     2026-07-06  |  2026-07-06T14:00:00Z
relative:     30m, 2h, 7d, 2w  (ago)
keywords:     today, yesterday, now
epoch:        seconds or milliseconds (auto-detected)
```

**Data source**, resolved in order — the first *configured* candidate is
authoritative, so a typo in `--source` is an error rather than a silent fallback
to the default file:

1. `--source <path>`
2. `$TEAMS_MESSAGES_FILE`
3. `~/.sessh/teams/messages.jsonl` (directory overridable with `$TEAMS_DATA_DIR`)

`conversations.jsonl` is read from beside the messages file (capture honours
`$TEAMS_CONVERSATIONS_FILE`); it is what turns a `conversationId` GUID into a
readable chat, meeting, team or channel name. A missing sidecar degrades to
id-only output, never an error. Reads are streaming and a malformed line is
skipped and counted, not fatal — `--verbose` reports the resolved paths, the
skipped lines and how many rows matched.

## Scripting

Output follows `gh`. On a TTY: aligned, colorized columns with an uppercase
header row. Piped or redirected: no header, no color, plain tab-separated
values.

```sh
sessh bitbucket repo list | cut -f1
sessh jira issue list ENG | awk -F'\t' '$3 == "Done" {print $1}'
```

`NO_COLOR` (any value) disables color even on a TTY.

Every `bitbucket`, `jira` and `confluence` command above takes `--json`. It
prints indented JSON and, where the underlying API's raw shape is richer than
the table, prints that raw shape rather than the flattened one.

`teams` is the exception, because scripts already parse its output: it keeps the
five output formats (`-f json|jsonl|table|csv|text`, default `json` when
piped and `table` on a terminal), so its piped output is JSON rather than TSV and
its `table` is a padded, ruled table rather than tab-separated columns.

`sessh bitbucket pr checks` is a CI gate and follows `gh`'s exit-code contract,
with `--json` included:

| Exit | Meaning |
| --- | --- |
| `0` | every reported check passed |
| `1` | a check failed, **or** no checks were reported |
| `8` | checks are still pending |

A failure takes precedence over a pending check.

```sh
sessh bitbucket pr checks example-repo 412 >/dev/null
case $? in
  0) echo green ;;
  8) echo "still running" ;;
  *) echo "failed or no checks"; exit 1 ;;
esac
```

`sessh auth status` exits 1 when any plugin is not logged in, which makes it usable
as a precondition in scripts. Authentication and "profile busy" failures print a
one-line message to stderr and exit 1 — no stack trace.

## Configuration

`~/.sessh/config.yaml` (override the whole root directory with `SESSH_HOME`). A
missing or unparseable file is treated as empty. Jira, Confluence, and Bitbucket
require explicit site or workspace settings before using a browser.

```yaml
profile: default            # profile used when a plugin names none
plugins:
  jira:
    profile: work           # this plugin uses a different browser identity
    site: https://acme.atlassian.net
  bitbucket:
    workspace: acme
  confluence:
    site: https://acme.atlassian.net
  teams:
    url: https://teams.microsoft.com/v2/
    profile: microsoft
```

Precedence for plugin settings is **env var → config → compiled-in default**; a
flag beats both.

| Variable | Effect |
| --- | --- |
| `SESSH_HOME` | root directory for profiles, config, sockets, and Teams data; default `~/.sessh` |
| `SESSH_PROFILE` | profile for every plugin (below `--profile`, above config) |
| `NO_COLOR` | disable color |
| `PLAYWRIGHT_DRIVER_PATH` | use this Playwright driver directory |
| `BITBUCKET_WORKSPACE` | Bitbucket workspace |
| `JIRA_SITE` | Jira tenant URL |
| `CONFLUENCE_SITE` | Confluence tenant URL (`/wiki` is appended) |
| `TEAMS_URL` | where capture points the browser |
| `TEAMS_PROFILE` | the teams plugin's profile name |
| `TEAMS_DATA_DIR` | directory holding `messages.jsonl` / `conversations.jsonl` |
| `TEAMS_MESSAGES_FILE` | explicit messages file path |
| `TEAMS_CONVERSATIONS_FILE` | explicit sidecar path (capture) |
| `SCROLL_DISABLE=1` | same as `sessh teams capture --no-scroll` |

`sessh teams capture`'s auto-scroller keeps the `SCROLL_*` knobs of the Node
capture (`SCROLL_INTERVAL_MS`, `SCROLL_STEPS`, `SCROLL_MAX`, `SCROLL_SHUFFLE`,
`SCROLL_DRY`, …); see `scrollerConfigFromEnv` in `plugins/teams/capture.go` for
the full list and defaults.

On-disk layout:

```
~/.sessh/
├── config.yaml
├── profiles/
│   ├── atlassian/        Chromium user-data dir + sessh.json (parent, created, origins, lastVerified)
│   └── microsoft/
├── run/
│   └── atlassian.sock    the lock holder's session socket
└── teams/
    ├── messages.jsonl
    └── conversations.jsonl
```

## Migration

See [the migration guide](migration.md) for previous builds and browser profiles.

## Troubleshooting

**`profile "atlassian" is busy (held by pid 4711: sessh daemon jira)`**

Rare: a holder normally serves its socket and this command routes through it
automatically. You see this only when the holder failed to open a socket, which
the message says outright ("It is not serving a session socket, so this command
cannot share it"). Stop the holder, or run against a different profile:

```sh
sessh jira issue list --profile work
```

`sessh auth list`'s `in use by` column tells you who holds a profile before you
collide with it.

**Headed login while the profile is busy.** `sessh auth login bitbucket` needs the profile to
itself, because Chromium cannot open a window in an already-running headless
context, and there is no hand-off in this version:

```
profile "atlassian" is busy (held by pid 4711: sessh daemon jira).
A headed login needs the profile to itself — Chromium cannot open a window in an
already-running headless context. Stop the holder and retry, or use --profile <name>
```

**`sessh auth logout` refuses.** `profile "atlassian" is in use by a running sessh
process; stop it first` — same cause, same fix.

**Session expired.** Any 401/403, or a redirect to `id.atlassian.com` /
`login.microsoftonline.com`, surfaces as one yellow line and exit 1:

```
no Jira session for https://acme.atlassian.net. Run: sessh auth login jira
```

Expiry never auto-opens a window — that would break cron, CI and headless boxes.
For an interactive shell, `--login` opts into headed re-auth in place:

```sh
sessh jira issue list ENG --login
```

**Driver not found.** See [Install](#the-one-prerequisite-a-playwright-driver).
Both `npm i -g playwright-core@1.61.1` and `PLAYWRIGHT_DRIVER_PATH` fix it; `node`
must also be on `$PATH`, since the driver is a Node process.

**Empty results that should not be empty.** Confluence answers 403 to anonymous
callers (Jira answers 401), so "no results" and "not logged in" can look alike.
Run `sessh auth status`, and `--verbose` to see which profile was resolved and
whether the command was routed through another process.

## How it works

| Layer | Where |
| --- | --- |
| Entry point; builds the command tree from the registry | `cmd/sessh/` |
| Profile manager, lock-or-connect, Chromium lifecycle, session impl | `session/` |
| The plugin contract (`Name`/`Profile`/`Command`) and `WithSession` | `plugin/` |
| The four tools | `plugins/{bitbucket,jira,confluence,teams}/` |
| `gh`-style tables, colors, relative time | `format/` |

Registration is compile-time: each plugin calls `plugin.Register` from `init()`
and `cmd/sessh` blank-imports it. Adding a tool means writing its commands and
nothing about browsers.

Reads go through Playwright's `APIRequestContext`, a real HTTP client that
carries the profile's cookies and has no CORS to dodge — not in-page `fetch`.
The teams plugin additionally uses `Eval` to read IndexedDB, which no HTTP API
exposes. Both are served identically whether the session is in-process or
JSON-RPC'd over the holder's socket, which is why nothing in a plugin knows or
cares which one it got.
