#!/bin/sh
# Usage: sh install.sh [VERSION | --source] [--autostart PLUGIN].
# Optional: SESSH_INSTALL_DIR, SESSH_RELEASE_DIR, SESSH_AUTOSTART, SESSH_SHELL_RC, SESSH_REPOSITORY.
set -eu
export LC_ALL=C
DEFAULT_VERSION=latest
REPOSITORY=${SESSH_REPOSITORY:-stefanm8/sessh}

fail() { printf 'sessh installer: %s\n' "$*" >&2; exit 1; }
main() {
  version=$DEFAULT_VERSION
  autostart=${SESSH_AUTOSTART:-}
  source_build=
  version_given=
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --source) source_build=1; shift ;;
      --help|-h)
        printf '%s\n' 'Usage: sh install.sh [VERSION | --source] [--autostart PLUGIN]' 'Installs sessh to ~/.local/bin and configures your shell PATH.' 'Use --source (or make install) to build this checkout with Go.'
        return ;;
      --autostart)
        [ "$#" -ge 2 ] || fail '--autostart needs a plugin (for example teams).'
        autostart=$2
        shift 2
        ;;
      --*) fail "Unknown option: $1" ;;
      *)
        [ -z "$version_given" ] || fail 'Usage: sh install.sh [VERSION | --source] [--autostart PLUGIN]'
        version=$1
        version_given=1
        shift
        ;;
    esac
  done
  case "$autostart" in ''|teams|jira|bitbucket|confluence) ;; *) fail 'Unknown autostart plugin.' ;; esac
  printf '%s\n' "$REPOSITORY" | grep -Eq '^[0-9A-Za-z_.-]+/[0-9A-Za-z_.-]+$' || fail 'SESSH_REPOSITORY must be owner/repository.'
  release_base=https://github.com/$REPOSITORY/releases
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
  work=$(mktemp -d)
  staged_binary=
  trap 'rm -rf "$work"; if [ -n "$staged_binary" ]; then rm -f "$staged_binary"; fi' EXIT
  trap 'exit 1' HUP INT TERM
  if [ -n "$source_build" ]; then
    [ -z "$version_given" ] || fail '--source cannot be combined with a release version.'
    source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
    [ -f "$source_dir/go.mod" ] || fail '--source requires a sessh source checkout.'
    version=${SESSH_BUILD_VERSION:-dev}
    case "$version" in *[!0-9A-Za-z.-]*|'') fail 'Invalid build version.' ;; esac
    (cd "$source_dir" && "${GO:-go}" build -trimpath -ldflags "-X main.version=$version" -o "$work/sessh" ./cmd/sessh)
  else
    if [ "$version" = latest ]; then
      if command -v curl >/dev/null 2>&1 && latest_url=$(curl -fsSLI --connect-timeout 15 --max-time 60 -o /dev/null -w '%{url_effective}' "$release_base/latest"); then
        version=${latest_url##*/}
      elif command -v gh >/dev/null 2>&1; then
        version=$(gh release view --repo "$REPOSITORY" --json tagName --jq .tagName)
      else
        fail 'Could not resolve the latest release. Install curl, or use authenticated gh for a private repository.'
      fi
    fi
    case "$version" in *[!0-9A-Za-z.-]*) fail 'Invalid version.' ;; esac
    printf '%s\n' "$version" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || fail 'Invalid version.'
    version=${version#v}
    prefix=sessh_${version}_${target_os}_${target_arch}
    archive=$prefix.tar.gz
    manifest=sessh_${version}_checksums.txt
    if [ -n "${SESSH_RELEASE_DIR:-}" ]; then
      cp "$SESSH_RELEASE_DIR/$archive" "$SESSH_RELEASE_DIR/$manifest" "$work/"
    elif command -v curl >/dev/null 2>&1 &&
      curl -fsSL --retry 2 --connect-timeout 15 --max-time 300 "$release_base/download/v$version/$manifest" -o "$work/$manifest" &&
      curl -fsSL --retry 2 --connect-timeout 15 --max-time 300 "$release_base/download/v$version/$archive" -o "$work/$archive"; then
      : # Public releases need no GitHub account or token.
    elif command -v gh >/dev/null 2>&1; then
      rm -f "$work/$archive" "$work/$manifest"
      gh release download "v$version" --repo "$REPOSITORY" --pattern "$archive" --pattern "$manifest" --dir "$work"
    else
      fail 'Could not download the release. Check its version/repository, or use authenticated gh for a private repository.'
    fi
    expected=$(awk -v asset="$archive" '$2 == asset { print $1 }' "$work/$manifest")
    [ "${#expected}" -eq 64 ] || fail 'Checksum entry is missing or ambiguous.'
    if command -v sha256sum >/dev/null 2>&1; then
      actual=$(sha256sum "$work/$archive" | awk '{print $1}')
    else
      actual=$(shasum -a 256 "$work/$archive" | awk '{print $1}')
    fi
    [ "$actual" = "$expected" ] || fail 'Checksum mismatch.'
    tar -xzOf "$work/$archive" "$prefix/sessh" > "$work/sessh"
  fi

  # Replace the installation atomically, including when a daemon is running.
  [ ! -d "$bin_dir/sessh" ] || fail 'Destination is a directory.'
  mkdir -p "$bin_dir"
  staged_binary=$(mktemp "$bin_dir/.sessh-install.XXXXXX")
  cat "$work/sessh" > "$staged_binary"
  [ -s "$staged_binary" ] || fail 'Release binary is empty.'
  chmod 755 "$staged_binary"
  mv -f "$staged_binary" "$bin_dir/sessh"
  staged_binary=
  printf 'Installed sessh %s to %s/sessh\n' "$version" "$bin_dir"
  if [ -n "${SESSH_SHELL_RC:-}" ]; then
    "$bin_dir/sessh" setup --rc "$SESSH_SHELL_RC"
  else
    "$bin_dir/sessh" setup
  fi
  if [ -n "$autostart" ]; then
    if [ -n "${SESSH_SHELL_RC:-}" ]; then
      "$bin_dir/sessh" autostart enable "$autostart" --rc "$SESSH_SHELL_RC"
    else
      "$bin_dir/sessh" autostart enable "$autostart"
    fi
  fi
}
main "$@"
