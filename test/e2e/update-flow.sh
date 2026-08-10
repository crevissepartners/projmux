#!/usr/bin/env bash
set -euo pipefail

# npm update-flow e2e.
#
# Proves the npm update fix end to end against the real registry:
#   1. install an older published projmux globally via npm
#   2. run the freshly-built (fixed) binary's `update apply`, which must run
#      `npm install -g projmux@latest` (not the old `npm update -g`, which
#      frequently refuses to cross a newer minor/major and leaves the install
#      stuck on the old version)
#   3. assert the global `projmux` is now the latest published version
#   4. (bonus) assert installer autodetection reports `npm` from an npm-shaped
#      binary path even when PROJMUX_INSTALLER is unset
#
# This depends on the public npm registry and the published `projmux` package,
# so it is opt-in / local rather than a required CI gate. It skips cleanly when
# the registry is unreachable or too few versions are published.

fail() { echo "update-flow e2e: $*" >&2; exit 1; }
skip() { echo "update-flow e2e SKIP: $*" >&2; exit 0; }

# Non-root container user cannot write the default global prefix; point npm at
# writable dirs under HOME.
export HOME="${HOME:-/tmp/projmux-home}"
mkdir -p "$HOME"
export NPM_CONFIG_PREFIX="$HOME/.npm-global"
export NPM_CONFIG_CACHE="$HOME/.npm-cache"
export NPM_CONFIG_UPDATE_NOTIFIER=false
export NPM_CONFIG_FUND=false
export NPM_CONFIG_AUDIT=false
mkdir -p "$NPM_CONFIG_PREFIX/bin" "$NPM_CONFIG_CACHE"
export PATH="$NPM_CONFIG_PREFIX/bin:$PATH"

command -v node >/dev/null 2>&1 || fail "node is required"
command -v npm >/dev/null 2>&1 || fail "npm is required"
command -v go >/dev/null 2>&1 || fail "go is required to build the binary under test"

echo ">> npm $(npm --version), node $(node --version)"

if ! npm ping >/dev/null 2>&1; then
  skip "npm registry unreachable (this suite needs network; run with a bridged network)"
fi

versions_json="$(npm view projmux versions --json 2>/dev/null)" || skip "cannot query published projmux versions"
latest="$(npm view projmux version 2>/dev/null || true)"
[ -n "$latest" ] || skip "no published projmux latest dist-tag"

# Pick the newest published version that is not the current latest.
older="$(node -e '
  const versions = JSON.parse(process.argv[1]);
  const arr = Array.isArray(versions) ? versions : [versions];
  const latest = process.argv[2];
  const older = arr.filter((v) => v !== latest);
  console.log(older.length ? older[older.length - 1] : "");
' "$versions_json" "$latest")"
[ -n "$older" ] || skip "need at least two published versions to test an upgrade (latest=$latest)"

echo ">> published older=$older latest=$latest"

# Step 1: install the older version globally and confirm it is active.
npm install -g "projmux@$older" >/dev/null 2>&1 || fail "npm install -g projmux@$older failed"
got_old="$(projmux version 2>/dev/null || true)"
echo ">> before update: $got_old"
echo "$got_old" | grep -Fq "$older" || fail "expected installed version $older, got: $got_old"

# Step 2: build the fixed binary under test from the mounted source tree.
build="$(mktemp -d)"
( cd /workspace && go build -o "$build/projmux" ./cmd/projmux ) || fail "go build ./cmd/projmux failed"

# Guard: the fixed apply must plan `npm install -g projmux@latest`.
fixed_plan="$(PROJMUX_INSTALLER=npm "$build/projmux" update apply --dry-run --no-apply 2>&1)" || fail "fixed apply --dry-run failed: $fixed_plan"
echo "$fixed_plan" | grep -Fq "npm install -g projmux@latest" \
  || fail "fixed apply plan missing 'npm install -g projmux@latest': $fixed_plan"

# Step 3: run the fixed apply for real (skip tmux reload; not the focus here).
PROJMUX_INSTALLER=npm "$build/projmux" update apply --no-apply || fail "update apply failed"

# Step 4: the global projmux must now be the latest published version.
got_new="$(projmux version 2>/dev/null || true)"
echo ">> after update: $got_new"
echo "$got_new" | grep -Fq "$latest" || fail "after update expected $latest, got: $got_new"
[ "$older" != "$latest" ] && echo "$got_new" | grep -Fq "$older" \
  && fail "still reporting old version $older after update: $got_new"
echo ">> PASS: upgraded $older -> $latest via fixed 'update apply'"

# Step 5 (bonus): installer autodetection from an npm-shaped path with the env
# hint removed. Copy the fixed binary into a node_modules/@projmux layout and
# confirm `update status` self-reports source=npm without PROJMUX_INSTALLER.
npmshape="$build/node_modules/@projmux/linux-x64/bin"
mkdir -p "$npmshape"
cp "$build/projmux" "$npmshape/projmux"
detected="$(env -u PROJMUX_INSTALLER "$npmshape/projmux" update status --json 2>/dev/null || true)"
echo "$detected" | grep -Fq '"source": "npm"' \
  || fail "autodetection did not report npm from npm-shaped path: $detected"
echo ">> PASS: installer autodetection reports npm without PROJMUX_INSTALLER"

echo ">> update-flow e2e complete"
