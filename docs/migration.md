# Migrating to sessh

The project previously used the names `zai-cli` and `zai`. The command is now
`sessh` (session shell), and the Go module is `github.com/stefanm8/sessh`.

| Previous | Current |
| --- | --- |
| `zai ...` | `sessh ...` |
| `cmd/zai` | `cmd/sessh` |
| `ZAI_HOME` | `SESSH_HOME` |
| `ZAI_PROFILE` | `SESSH_PROFILE` |
| `~/.zai` | `~/.sessh` |
| `zai.json` profile metadata | `sessh.json` |
| `.zai-lock` | `.sessh-lock` |

Old environment variables are not read, and state is not migrated
automatically. Plugin-specific variables such as `JIRA_SITE`, `TEAMS_DATA_DIR`,
and `TEAMS_MESSAGES_FILE` are unchanged. Teams data now follows `SESSH_HOME`
unless one of its more specific overrides is set.

## Keep existing profiles and captures

Stop all old CLI processes, daemons, and captures before migrating. Back up
your state directory privately. Do not run both versions against the same
profile: their lock filenames differ.

For the default state directory, first verify that `~/.sessh` does not already
exist, then move the old root:

```sh
test ! -e "$HOME/.sessh" && mv "$HOME/.zai" "$HOME/.sessh"
```

If a destination already exists, stop here and reconcile the directories
manually; do not overwrite profiles. For a custom `ZAI_HOME`, use that source
directory instead and set `SESSH_HOME` to the chosen destination.

Within each migrated profile, rename `zai.json` to `sessh.json`. The JSON
format is unchanged. Old lock files can remain inert; sessh creates its own.
Stale sockets are recovered when the next session starts.

Set your Bitbucket workspace and Jira/Confluence sites in `config.yaml` or the
corresponding environment variables. Earlier builds supplied company-specific
defaults; these have been removed. Review file permissions on the migrated
state, then run:

```sh
sessh auth list
sessh auth status jira      # choose a configured service
sessh teams messages -n 5  # if you have a Teams capture
```

Replace shell aliases and scripts that invoke `zai`. Keep the old executable
until you have verified the new setup, then remove it from your `PATH`.

## Import another Chromium profile

To copy only cookies and localStorage into a new profile:

```sh
sessh auth import /path/to/chromium-user-data --into atlassian
sessh auth status bitbucket
```

Close the source browser first. Import uses Chromium to read the profile, so
back it up beforehand; opening a profile can update Chromium metadata.
The importer does not copy IndexedDB or message captures. Existing Teams
JSONL files can be read with `--source /path/to/messages.jsonl` or selected for
capture with `TEAMS_DATA_DIR=/path/to/data`.
