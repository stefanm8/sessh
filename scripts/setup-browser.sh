#!/bin/sh
# Set up the exact browser driver expected by this sessh release, without sudo.
set -eu
export LC_ALL=C
PLAYWRIGHT_VERSION=1.61.1

fail() { printf 'sessh browser setup: %s\n' "$*" >&2; exit 1; }
case "${1:-}" in
  --help|-h)
    printf 'Usage: sh setup-browser.sh\nInstalls Playwright %s and Chromium in your user cache. Requires Node.js and npm.\n' "$PLAYWRIGHT_VERSION"
    exit 0 ;;
  '') ;;
  *) fail 'No options are supported except --help.' ;;
esac
[ "$#" -le 1 ] || fail 'Too many arguments.'
command -v node >/dev/null 2>&1 || fail 'Install Node.js and npm first.'
command -v npm >/dev/null 2>&1 || fail 'Install npm first.'
case "$(uname -s)" in
  Darwin) cache_dir=${HOME:?HOME must be set}/Library/Caches ;;
  Linux) cache_dir=${HOME:?HOME must be set}/.cache ;;
  *) fail 'Supported systems are macOS and Linux.' ;;
esac
driver_dir=${PLAYWRIGHT_DRIVER_PATH:-$cache_dir/ms-playwright-go/$PLAYWRIGHT_VERSION}
mkdir -p "$driver_dir"
driver_dir=$(cd "$driver_dir" && pwd)
node_path=$(command -v node)

if [ -e "$driver_dir/package" ] || [ -L "$driver_dir/package" ]; then
  installed_version=$(node -p 'require(process.argv[1]).version' "$driver_dir/package/package.json") ||
    fail 'The existing driver is invalid. Set PLAYWRIGHT_DRIVER_PATH to a new directory.'
  [ "$installed_version" = "$PLAYWRIGHT_VERSION" ] ||
    fail 'The existing driver has a different version. Set PLAYWRIGHT_DRIVER_PATH to a new directory.'
else
  npm install --prefix "$driver_dir/npm" --no-save --no-package-lock --ignore-scripts --no-audit --no-fund "playwright-core@$PLAYWRIGHT_VERSION"
  ln -s "$driver_dir/npm/node_modules/playwright-core" "$driver_dir/package"
fi
if [ ! -e "$driver_dir/node" ] && [ ! -L "$driver_dir/node" ]; then
  ln -s "$node_path" "$driver_dir/node"
fi
[ -x "$driver_dir/node" ] || fail 'The cached Node executable is not usable. Set PLAYWRIGHT_DRIVER_PATH to a new directory.'
"$driver_dir/node" "$driver_dir/package/cli.js" install chromium
printf 'Playwright %s and Chromium are ready. Run sessh auth login <plugin>.\n' "$PLAYWRIGHT_VERSION"
printf 'On Linux, missing system libraries can be installed separately with:\n  "%s" "%s/package/cli.js" install-deps chromium\n' "$driver_dir/node" "$driver_dir"
