# Publishing and releases

The repository is [stefanm8/sessh](https://github.com/stefanm8/sessh), and the
module path is `github.com/stefanm8/sessh`. Keep the repository private during
validation. Pushing source or running CI does not approve a public release.

## Before making the repository public

1. Review the source and fixtures for credentials, private messages, company
   data, and third-party material. Confirm that you have the right to publish
   the code under the included MIT license.
2. Complete live-service validation and obtain the maintainer's explicit
   approval before changing the repository visibility to public.
3. Enable private vulnerability reporting in the repository's security
   settings, require the CI check for merges, and enable secret scanning
   and push protection where available.
4. Review `git status --short` and the initial staged diff. Do not force-add
   ignored profiles, exports, environment files, or build outputs.
5. Run `make check` and manually verify login and basic reads for each service
   using dedicated accounts. Automated tests do not verify live services.

The default branch is `main`; the SSH remote is
`git@github.com:stefanm8/sessh.git`. Development pushes remain private while
validation is in progress. Do not publish releases or upload release archives
until the public-release review is complete.

## Build release files

```sh
make check
make dist VERSION=0.1.0
```

The distribution script requires Go, `tar`, and `shasum`. It creates archives
for macOS and Linux on amd64 and arm64, plus SHA-256 checksums in `dist/`.
Archives include the binary, MIT license, third-party license texts, example
configuration, and documentation. Node and Chromium are installed separately
by users and are not bundled. Cross-compilation is not a runtime test on the
target architecture.

Inspect the archives and smoke-test the native binary's `--version`, `--help`,
and offline queries. Refresh bundled dependency license notices if versions
changed. Record user-visible changes and migration notes, then create a tag
such as `v0.1.0` and publish its archives through the GitHub release UI.

After publication, installation can use:

```sh
go install github.com/stefanm8/sessh/cmd/sessh@latest
```

The tag identifies source installed this way; source builds use the `dev`
version string unless a release version is injected with `-ldflags`.
