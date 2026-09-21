# Security

Report vulnerabilities through GitHub's **Security → Advisories → Report a
vulnerability** form when available. Otherwise, open an issue requesting a
private contact channel without disclosing details. Include the version,
impact, and a reproduction using synthetic data.

Browser profiles contain credentials; Teams captures contain private messages.
Keep state and backups outside source control. New directories use `0700` and
metadata, sockets, and capture files use `0600`; existing permissions are preserved.
The OS user is the trust boundary: processes running as you can use the local
session socket. Do not expose it over a network or share state between users.

Use `--no-inherit` to isolate a profile from `default`. Deleting local profiles
does not revoke remote sessions; use the service's session controls for that.
Never include cookies, profiles, or captures in bug reports.
