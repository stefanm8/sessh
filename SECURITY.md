# Security

Report vulnerabilities through the repository's
[private security advisory form](https://github.com/stefanm8/sessh/security/advisories/new).
If private reporting is unavailable, open an issue requesting a private contact
without including vulnerability details, credentials, or account data.

Include the affected sessh version, operating system, a minimal reproduction
with synthetic data, and the impact you observed. Do not attach browser profiles,
cookies, localStorage, access tokens, Teams captures, or verbose account logs.
For a fork, use that fork's private reporting channel.

## Local data

sessh runs browser sessions with the permissions of your signed-in accounts.
Profiles are stored under `~/.sessh/profiles`, local Teams captures under
`~/.sessh/teams`, and worker status/logs under `~/.sessh/run`. `SESSH_HOME` and
provider-specific options can change these paths. Treat all of them as private.
Session sockets and captured files are intended for the current OS user; another
process running as that user may access them.

Background capture can open chats and mark them read. `--no-scroll` avoids those
chat-opening actions. Only capture data you are authorized to access. No captured
messages or credentials are required for the test suite or release build.

## Updates

Use the latest release when reporting a problem. Supported fixes target the
current codebase; there is no commitment to maintain older release branches.
Release archives have SHA-256 checksums. These detect corrupted downloads but
are not an independent publisher signature. Keep Node.js, Chromium, and the
Playwright driver updated together with the versions expected by sessh.
