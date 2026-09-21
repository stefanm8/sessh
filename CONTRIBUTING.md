# Contributing to sessh

Bug reports, documentation fixes, and focused pull requests are welcome.
Describe the problem, the expected behavior, and a minimal reproduction.
Replace tenant URLs, people, messages, and identifiers with fictional examples.
Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## Development

Use Go 1.25.4 or later on macOS or Linux. Clone your fork, create a branch,
and run:

```sh
make check
make build
./bin/sessh --help
```

The test suite uses temporary storage and synthetic fixtures. It does not
need service accounts, Node, or Chromium. For manual browser checks, follow
the [README](README.md), use a dedicated account/profile, and set `SESSH_HOME`
to a private directory outside the checkout.

Before submitting a pull request, run `make check`, describe what changed and
how you verified it, and add a regression test when changing behavior.
Avoid tests that contact real services. Keep credentials and captured data
out of commits, screenshots, and test fixtures.

## Architecture

| Directory | Responsibility |
| --- | --- |
| `cmd/sessh` | Command tree, authentication, and daemon commands |
| `session` | Configuration, browser profiles, locking, and local RPC |
| `plugin` | Registry, session injection, and login helpers |
| `plugins` | Bitbucket, Jira, Confluence, and Teams commands |
| `format` | Terminal output and browser links |

A plugin implements `Name`, `Profile`, and `Command`, registers itself from
`init`, and is imported by `cmd/sessh/main.go`. Plugins with required settings
also implement `Validate() error`; call it from `PersistentPreRunE` so `--web`
commands validate too. `plugin.Acquire` and `plugin.Login` validate before
opening browser state. Keep help and offline commands usable without a login.

Use `plugin.WithSession` for browser commands. It supplies a `session.Session`
that may run locally or through the process holding the profile lock. Keep
Playwright types inside `session`; RPC method arguments and results must be
serializable. Test additions with synthetic data and preserve documented
output formats and exit codes.

## Dependencies and conduct

Run `go mod tidy` when changing dependencies. Update [third-party notices](THIRD_PARTY_NOTICES.md)
and bundled license texts when their versions or licenses change. Keep the
Playwright Go binding, `session.DriverVersion`, and browser setup instructions
in sync.

Be respectful, critique the work rather than the person, and avoid harassment
or sharing private information. Maintainers may remove abusive contributions.
By contributing, you agree that your contribution is available under this
project's [MIT license](LICENSE).
