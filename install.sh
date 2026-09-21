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
  --help               Show this help

Downloads require GitHub CLI (gh auth login). For browser commands,
run the separate setup-browser.sh script after installing Node.js and npm.
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
  if [ -z "$from_dir" ]; then
    command -v gh >/dev/null 2>&1 || fail 'Install GitHub CLI and run gh auth login first.'
    if [ "$version" = latest ]; then
      version=$(gh release view --repo "$REPOSITORY" --json tagName --jq .tagName)
    fi
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

  if [ -n "$from_dir" ]; then
    cp "$from_dir/$archive" "$from_dir/$manifest" "$work/"
  else
    gh release download "$tag" --repo "$REPOSITORY" --pattern "$archive" --pattern "$manifest" --dir "$work"
  fi
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
}

main "$@"
