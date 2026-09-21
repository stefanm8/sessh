# Security

## Reporting a vulnerability

Do not include vulnerabilities, cookies, tokens, browser profiles, or message
captures in public issues. After this repository is published on GitHub, use
its **Security → Advisories → Report a vulnerability** form when available.
If private reporting is unavailable, open a minimal issue asking for a private
contact channel without disclosing the vulnerability or affected data.

Include the sessh version, operating system, impact, and a reproduction using
synthetic data. There is no guaranteed response time. Security fixes target
the latest release and the main branch; older versions have no maintenance
commitment.

## Local trust model

sessh persists browser cookies and localStorage in Chromium profiles. Those
profiles are credentials. Teams exports can include message bodies, names,
and service metadata. Store them outside your checkout and do not upload
them to issue trackers. Use the services' session controls to revoke access;
deleting a local profile is not remote session revocation.

New profile directories and capture directories use mode `0700`; new capture
files, metadata, and RPC sockets use `0600`. Previously created files keep
their existing permissions. Check permissions when importing or migrating
state, and protect backups with the same care as the originals.

The local RPC socket can access the authenticated session, including page
evaluation. The operating-system user is the trust boundary: software running
as that user can access the same session. Do not expose the socket over a
network or share the state directory between users.

Profiles can inherit cookies and localStorage from the `default` profile.
Use `--no-inherit` for an unrelated identity. Use only accounts and data you
are authorized to access, in accordance with your organization's policies.

sessh is an independent project, unaffiliated with Atlassian or Microsoft.
