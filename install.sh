#!/bin/sh
# Usage: sh install.sh [VERSION]. Optional: SESSH_INSTALL_DIR, SESSH_RELEASE_DIR.
set -eu
export LC_ALL=C
DEFAULT_VERSION=latest
REPOSITORY=stefanm8/sessh

fail() { printf 'sessh installer: %s\n' "$*" >&2; exit 1; }
main() {
  [ "$#" -le 1 ] || fail 'Usage: sh install.sh [VERSION]'
  version=${1:-$DEFAULT_VERSION}
  bin_dir=${SESSH_INSTALL_DIR:-${HOME:?}/.local/bin}
  case "$(uname -s)" in
    Darwin) target_os=darwin ;;
    Linux) target_os=linux ;;
    *) fail 'Only macOS and Linux are supported.' ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) target_arch=amd64 ;;
    arm64|aarch64) target_arch=arm64 ;;
    *) fail 'Only amd64 and arm64 are supported.' ;;
  esac
  if [ "$version" = latest ]; then
    version=$(gh release view --repo "$REPOSITORY" --json tagName --jq .tagName)
  fi
  case "$version" in *[!0-9A-Za-z.-]*) fail 'Invalid version.' ;; esac
  printf '%s\n' "$version" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || fail 'Invalid version.'
  version=${version#v}
  prefix=sessh_${version}_${target_os}_${target_arch}
  archive=$prefix.tar.gz
  manifest=sessh_${version}_checksums.txt
  work=$(mktemp -d)
  staged_binary=
  trap 'rm -rf "$work"; if [ -n "$staged_binary" ]; then rm -f "$staged_binary"; fi' EXIT
  trap 'exit 1' HUP INT TERM
  if [ -n "${SESSH_RELEASE_DIR:-}" ]; then
    cp "$SESSH_RELEASE_DIR/$archive" "$SESSH_RELEASE_DIR/$manifest" "$work/"
  else
    gh release download "v$version" --repo "$REPOSITORY" --pattern "$archive" --pattern "$manifest" --dir "$work"
  fi
  expected=$(awk -v asset="$archive" '$2 == asset { print $1 }' "$work/$manifest")
  [ "${#expected}" -eq 64 ] || fail 'Checksum entry is missing or ambiguous.'
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$work/$archive" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$work/$archive" | awk '{print $1}')
  fi
  [ "$actual" = "$expected" ] || fail 'Checksum mismatch.'

  # Extract only the binary, then replace the installation atomically.
  [ ! -d "$bin_dir/sessh" ] || fail 'Destination is a directory.'
  mkdir -p "$bin_dir"
  staged_binary=$(mktemp "$bin_dir/.sessh-install.XXXXXX")
  tar -xzOf "$work/$archive" "$prefix/sessh" > "$staged_binary"
  [ -s "$staged_binary" ] || fail 'Release binary is empty.'
  chmod 755 "$staged_binary"
  mv -f "$staged_binary" "$bin_dir/sessh"
  staged_binary=
  printf 'Installed sessh %s to %s/sessh\n' "$version" "$bin_dir"
}
main "$@"
