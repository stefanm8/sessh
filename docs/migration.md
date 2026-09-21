# Migrating from zai

The command is now `sessh`. Replace `ZAI_HOME` / `ZAI_PROFILE` with
`SESSH_HOME` / `SESSH_PROFILE`; old variables are no longer read.

1. Stop old commands, daemons, and captures; privately back up `~/.zai`.
2. Move `~/.zai` to `~/.sessh` only if the destination does not exist.
   For custom storage, set `SESSH_HOME` to its location.
3. Rename each profile's `zai.json` to `sessh.json`; the format is unchanged.
4. Set your Bitbucket workspace and Jira/Confluence sites in `config.yaml` or
   environment variables. Company-specific defaults have been removed.
5. Check imported permissions, then run `sessh auth list` and a service command.

Do not run old and new versions against the same profile: their lock names
differ. Existing destination directories must be reconciled manually.
Plugin-specific environment variables are unchanged; Teams storage now follows
`SESSH_HOME` unless overridden by `TEAMS_DATA_DIR` or `TEAMS_MESSAGES_FILE`.
