# Installing sessh

## Private GitHub release

Install GitHub CLI and run `gh auth login` with an account that can access
`stefanm8/sessh`. Download and run the release-specific installer:

```sh
gh release download v0.1.0 -R stefanm8/sessh -p install.sh -O - | sh
```

The installer downloads the correct macOS/Linux archive for amd64/arm64,
checks its SHA-256 checksum, and installs `~/.local/bin/sessh` without sudo.
It uses authenticated `gh` when available. Public release downloads can use
curl instead, once the repository is public. Releases from a private
repository are not anonymously downloadable.

To inspect the script before running it:

```sh
gh release download v0.1.0 -R stefanm8/sessh -p install.sh
sh install.sh --help
sh install.sh
```

Go is not needed for release binaries. A GitHub release's installer defaults
to that exact version; the installer in the source checkout defaults to the
latest stable release. `--version` overrides either default.

## Browser setup

Help and offline queries work without a browser. Login and live service
commands need Node.js, npm, and the matching Playwright driver and Chromium.
The installer's `--with-browser` option sets up the driver and browser:

```sh
sh install.sh --with-browser
```

Alternatively, download the browser setup script separately:

```sh
gh release download v0.1.0 -R stefanm8/sessh -p setup-browser.sh
sh setup-browser.sh
```

The pinned package is installed in the Playwright Go cache:

- macOS: `~/Library/Caches/ms-playwright-go/1.61.1`
- Linux: `~/.cache/ms-playwright-go/1.61.1`

`PLAYWRIGHT_DRIVER_PATH` selects another directory. Keep that variable set
when running sessh if you use a custom location. `PLAYWRIGHT_BROWSERS_PATH`
can similarly override Chromium's cache. The script reuses a matching driver
and refuses to replace an incompatible one. It does not install Node or
system packages. On Linux, follow the printed `install-deps chromium` command
if shared libraries are missing. Interactive login needs a graphical session.

## Options and manual downloads

| Option | Purpose |
| --- | --- |
| `--version v0.1.0` | Pin the release; also accepts `0.1.0` |
| `--bin-dir /path/to/bin` | Choose where the executable is installed |
| `--from-dir /path/to/downloads` | Install from a local archive and checksum file |
| `--with-browser` | Set up the matching driver and Chromium |

`SESSH_VERSION` and `SESSH_INSTALL_DIR` provide defaults for the first two
options. Explicit options take precedence. Shell configuration is never edited.
For the default location, add this to your shell configuration if needed:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Every release includes these assets:

| Asset | Platform |
| --- | --- |
| `sessh_0.1.0_darwin_arm64.tar.gz` | Apple Silicon Mac |
| `sessh_0.1.0_darwin_amd64.tar.gz` | Intel Mac |
| `sessh_0.1.0_linux_arm64.tar.gz` | ARM64 Linux |
| `sessh_0.1.0_linux_amd64.tar.gz` | Intel/AMD 64-bit Linux |
| `sessh_0.1.0_checksums.txt` | SHA-256 hashes for the archives and scripts |
| `install.sh` | Binary installer, pinned to this release |
| `setup-browser.sh` | Optional driver and Chromium setup |

Download assets using GitHub's release page or `gh release download`. Keep
the archive and checksum file in the same directory, then run:

```sh
sh install.sh --version v0.1.0 --from-dir /path/to/downloads
```

Checksums detect corruption and mismatched assets; their authenticity depends
on downloading them from the trusted repository. Archives include the binary,
documentation, examples, and dependency license texts. Native Windows is not
supported.

## Update, uninstall, and verify

Rerun the installer with `--version latest` to update. Corrupt or incomplete
downloads leave the current binary unchanged. To uninstall the default binary:

```sh
rm "$HOME/.local/bin/sessh"
```

This leaves profiles, configuration, captured messages, and browser caches in
place. Verify installation with `sessh --version`, `sessh --help`, and
`sessh auth list`, then follow the [README](../README.md) to configure a service.
