#!/bin/sh
# Build release archives locally; this script never publishes them.
set -eu
export LC_ALL=C
# Keep macOS resource forks and extended attributes out of portable archives.
export COPYFILE_DISABLE=1

version=${1:-}
version=${version#v}
case "$version" in
  ''|*[!a-zA-Z0-9._-]*)
    echo 'Specify a release version such as 0.1.0 or 0.1.0-rc.1.' >&2
    exit 1
    ;;
esac
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo 'Specify a release version such as 0.1.0 or 0.1.0-rc.1.' >&2
  exit 1
fi

cd "$(dirname "$0")/.."
mkdir -p dist
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM

sed "s/^DEFAULT_VERSION=latest$/DEFAULT_VERSION=v$version/" install.sh > dist/install.sh
cp scripts/setup-browser.sh dist/setup-browser.sh

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  archive="sessh_${version}_${target_os}_${target_arch}"
  mkdir "$stage/$archive"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "$stage/$archive/sessh" ./cmd/sessh
  cp LICENSE README.md THIRD_PARTY_NOTICES.md config.example.yaml "$stage/$archive/"
  cp -R third_party docs "$stage/$archive/"
  cp SECURITY.md CONTRIBUTING.md "$stage/$archive/"
  cp dist/install.sh dist/setup-browser.sh "$stage/$archive/"
  tar -czf "dist/$archive.tar.gz" -C "$stage" "$archive"
done

cd dist
shasum -a 256 "sessh_${version}_"*.tar.gz install.sh setup-browser.sh > "sessh_${version}_checksums.txt"
