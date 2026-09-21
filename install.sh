#!/bin/sh
# Install a verified release binary without Go or administrator privileges.
set -eu
export LC_ALL=C

# The distribution build replaces this with the release's own tag.
DEFAULT_VERSION=latest
REPOSITORY=stefanm8/sessh

fail() { printf 'sessh installer: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'EOF'
Usage: sh install.sh [options]
  --version VERSION    Release tag, e.g. v0.1.0 (default: this release or latest)
  --bin-dir DIRECTORY  Install directory (default: $HOME/.local/bin)
  --from-dir DIRECTORY Use already downloaded archives and checksums
  --with-browser       Also install the pinned Playwright driver and Chromium
  --help               Show this help

Private releases require an authenticated GitHub CLI (gh auth login).
Public releases can also be downloaded using curl. Browser setup requires
Node.js and npm; the CLI and offline queries work without them.
EOF
}

cleanup() {
  if [ -n "${staged_binary:-}" ]; then rm -f "$staged_binary"; fi
  if [ -n "${work:-}" ]; then rm -rf "$work"; fi
}

main() {
  version=${SESSH_VERSION:-$DEFAULT_VERSION}
  bin_dir=${SESSH_INSTALL_DIR:-${HOME:?HOME must be set}/.local/bin}
  from_dir=
  with_browser=false
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --version|--bin-dir|--from-dir)
        [ "$#" -ge 2 ] && [ -n "$2" ] || fail "Missing value for $1"
        case "$1" in
          --version) version=$2 ;;
          --bin-dir) bin_dir=$2 ;;
          --from-dir) from_dir=$2 ;;
        esac
        shift 2 ;;
      --with-browser) with_browser=true; shift ;;
      --help|-h) usage; return ;;
      *) fail "Unknown option: $1 (use --help)" ;;
    esac
  done

  case "$(uname -s)" in
    Darwin) target_os=darwin ;;
    Linux) target_os=linux ;;
    *) fail 'Supported systems are macOS and Linux.' ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) target_arch=amd64 ;;
    arm64|aarch64) target_arch=arm64 ;;
    *) fail 'Supported architectures are amd64 and arm64.' ;;
  esac
  for tool in tar awk mktemp; do
    command -v "$tool" >/dev/null 2>&1 || fail "$tool is required."
  done
  if command -v sha256sum >/dev/null 2>&1; then
    hash_command=sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    hash_command=shasum
  else
    fail 'Install sha256sum or shasum to verify downloads.'
  fi
  if [ "$with_browser" = true ]; then
    command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1 ||
      fail 'Browser setup requires Node.js and npm. Omit --with-browser to install only the CLI.'
  fi

  transport=local
  if [ -z "$from_dir" ]; then
    if command -v gh >/dev/null 2>&1 && gh auth status --hostname github.com >/dev/null 2>&1; then
      transport=gh
    else
      command -v curl >/dev/null 2>&1 || fail 'Install GitHub CLI (gh) or curl to download a release.'
      transport=curl
    fi
  fi
  if [ "$version" = latest ]; then
    case "$transport" in
      gh) version=$(gh release view --repo "$REPOSITORY" --json tagName --jq .tagName) || fail 'Cannot find the latest release.' ;;
      curl)
        latest_url=$(curl --fail --silent --show-error --location --proto '=https' --output /dev/null --write-out '%{url_effective}' "https://github.com/$REPOSITORY/releases/latest") ||
          fail 'Cannot find a public release. For a private repository, run gh auth login first.'
        version=${latest_url##*/} ;;
      local) fail '--from-dir requires --version (or a release-specific install.sh).' ;;
    esac
  fi
  case "$version" in *[!0-9A-Za-z.-]*) fail 'Invalid release version.' ;; esac
  printf '%s\n' "$version" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || fail 'Invalid release version.'
  version=${version#v}
  tag=v$version
  prefix=sessh_${version}_${target_os}_${target_arch}
  archive=$prefix.tar.gz
  manifest=sessh_${version}_checksums.txt
  work=$(mktemp -d)
  staged_binary=
  trap cleanup EXIT
  trap 'exit 1' HUP INT TERM

  case "$transport" in
    local)
      cp "$from_dir/$archive" "$from_dir/$manifest" "$work/" || fail 'Archive or checksum file is missing.' ;;
    gh)
      gh release download "$tag" --repo "$REPOSITORY" --pattern "$archive" --pattern "$manifest" --dir "$work" ||
        fail 'Download failed. Check the release tag and your repository access.' ;;
    curl)
      for asset in "$archive" "$manifest"; do
        curl --fail --silent --show-error --location --proto '=https' "https://github.com/$REPOSITORY/releases/download/$tag/$asset" --output "$work/$asset" ||
          fail 'Download failed. Private releases require gh auth login and repository access.'
      done ;;
  esac
  expected=$(awk -v asset="$archive" '$2 == asset { print $1 }' "$work/$manifest")
  [ "${#expected}" -eq 64 ] || fail 'Checksum entry is missing or ambiguous.'
  case "$expected" in *[!0-9a-fA-F]*) fail 'Invalid checksum entry.' ;; esac
  if [ "$hash_command" = sha256sum ]; then
    actual=$(sha256sum "$work/$archive" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$work/$archive" | awk '{print $1}')
  fi
  [ "$actual" = "$expected" ] || fail 'Checksum mismatch; the existing installation was not changed.'

  # Extract only named files to stdout, never archive paths onto the filesystem.
  tar -xzOf "$work/$archive" "$prefix/sessh" > "$work/sessh" || fail 'Release archive has no sessh binary.'
  [ -s "$work/sessh" ] || fail 'Release binary is empty.'
  if [ "$with_browser" = true ]; then
    tar -xzOf "$work/$archive" "$prefix/setup-browser.sh" > "$work/setup-browser.sh" || fail 'Release archive has no browser setup script.'
    sh "$work/setup-browser.sh"
  fi
  [ ! -d "$bin_dir/sessh" ] || fail 'The destination sessh is a directory.'
  mkdir -p "$bin_dir"
  staged_binary=$(mktemp "$bin_dir/.sessh-install.XXXXXX")
  cp "$work/sessh" "$staged_binary"
  chmod 755 "$staged_binary"
  mv -f "$staged_binary" "$bin_dir/sessh"
  staged_binary=
  printf 'Installed sessh %s to %s/sessh\n' "$version" "$bin_dir"
  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *) printf 'Add %s to your PATH, then run sessh --help.\n' "$bin_dir" ;;
  esac
  if [ "$with_browser" = false ]; then
    printf 'For browser commands, rerun with --with-browser after installing Node.js and npm.\n'
  fi
}

main "$@"
