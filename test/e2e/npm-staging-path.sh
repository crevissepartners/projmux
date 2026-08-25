#!/usr/bin/env bash
set -euo pipefail

# npm staging/retire path e2e (network-free, deterministic).
#
# Reproduces the class of bug fixed by the executable-path canonicalization:
# during `npm update -g projmux` arborist renames
#   <prefix>/lib/node_modules/projmux
# to
#   <prefix>/lib/node_modules/.projmux-<hash>
# then reifies the replacement and deletes the retired directory. A projmux
# process running from the retired tree resolves its own path there; if that
# doomed path is persisted into the generated tmux config and live hooks, every
# pane focus fails with `... returned 127` once npm removes it.
#
# This suite runs the freshly-built binary from a retired-shaped path and proves:
#   1. the generated config carries NO retire/staging segment,
#   2. it carries the canonical package path instead,
#   3. after the retired dir is deleted (npm cleanup), the path baked into the
#      config still exists and runs — i.e. no `returned 127`,
#   4. live tmux hooks sourced from that config reference the canonical path.
#
# It needs only Go + tmux (no registry), so it is a required, offline e2e gate.

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

socket="projmux-staging-e2e"
cleanup() {
  tmux -L "$socket" kill-server >/dev/null 2>&1 || true
  smoke_cleanup_env
}

smoke_setup_env
PROJMUX_E2E_SUITE="npm-staging"
export PROJMUX_E2E_SUITE
smoke_contract_install_trap
smoke_contract_begin N01 staging-retire executable-canonicalizer
trap cleanup EXIT
cd "$smoke_root"

smoke_build_binary
built="$PROJMUX_SMOKE_BIN"

# Build an npm-global-shaped layout with BOTH the reified canonical tree and the
# retired staging tree, mirroring the brief window npm leaves behind mid-update.
prefix="$PROJMUX_SMOKE_WORKDIR/npm-prefix"
retire_seg=".projmux-Ab3Cd5Ef" # matches arborist `.<name>-<8 base64 chars>`
canonical_bin="$prefix/lib/node_modules/projmux/bin/projmux"
retire_bin="$prefix/lib/node_modules/$retire_seg/bin/projmux"
mkdir -p "$(dirname "$canonical_bin")" "$(dirname "$retire_bin")"
cp "$built" "$canonical_bin"
cp "$built" "$retire_bin"
chmod +x "$canonical_bin" "$retire_bin"

# Step 1+2: run the binary FROM the retired path (no --bin, so it must resolve
# its own os.Executable and canonicalize) and capture the generated config.
cfg="$PROJMUX_SMOKE_WORKDIR/tmux-from-retire.conf"
"$retire_bin" internal tmux print-config >"$cfg" 2>"$PROJMUX_SMOKE_WORKDIR/print-config.err" \
  || { echo "internal tmux print-config from retired path failed:" >&2; cat "$PROJMUX_SMOKE_WORKDIR/print-config.err" >&2; exit 1; }

if grep -Fq "$retire_seg" "$cfg"; then
  echo "FAIL: generated config still references the npm retire/staging segment '$retire_seg'" >&2
  grep -F "$retire_seg" "$cfg" >&2
  exit 1
fi
smoke_assert_file_contains "$cfg" "$canonical_bin"
echo ">> PASS: config canonicalized retire path -> $canonical_bin (no '$retire_seg')"

# Extract the exact projmux path the config will invoke from a hook line, so the
# survival check asserts against what actually got baked in (not an assumption).
baked="$(grep -oE "[^ '\"]*/node_modules/[^ '\"]*/bin/projmux" "$cfg" | head -n1 || true)"
[ -n "$baked" ] || { echo "FAIL: no projmux node_modules path found in generated config" >&2; exit 1; }
echo ">> baked config binary path: $baked"

# Step 3: simulate npm deleting the retired dir after the update completes, then
# prove the baked path survives and runs (this is the anti-127 guarantee).
rm -rf "$prefix/lib/node_modules/$retire_seg"
[ ! -e "$retire_bin" ] || { echo "FAIL: retire dir not removed" >&2; exit 1; }

case "$baked" in
  *"$retire_seg"*)
    echo "FAIL: config baked the doomed retire path; would '... returned 127' after npm cleanup" >&2
    exit 1
    ;;
esac
[ -x "$baked" ] || { echo "FAIL: baked config binary path does not exist after npm cleanup: $baked" >&2; exit 1; }
if ! "$baked" version >"$PROJMUX_SMOKE_WORKDIR/baked-version.out" 2>&1; then
  echo "FAIL: baked config binary failed to run after npm cleanup (the 127 class of bug):" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/baked-version.out" >&2
  exit 1
fi
echo ">> PASS: baked config binary survives npm cleanup and runs ($baked version)"

# Step 4: source the config into a live tmux server and confirm the persisted
# global hooks reference the canonical path, never the retire segment.
tmux -L "$socket" kill-server >/dev/null 2>&1 || true
tmux -L "$socket" new-session -d -s staging -c "$PROJMUX_SMOKE_WORKDIR" sleep 300
tmux -L "$socket" source-file "$cfg"
hooks="$(tmux -L "$socket" show-hooks -g 2>/dev/null || true)"
if printf '%s\n' "$hooks" | grep -Fq "$retire_seg"; then
  echo "FAIL: live tmux hooks reference the retire/staging segment '$retire_seg'" >&2
  printf '%s\n' "$hooks" | grep -F "$retire_seg" >&2
  exit 1
fi
if ! printf '%s\n' "$hooks" | grep -Fq "node_modules/projmux/"; then
  echo "FAIL: live tmux hooks do not reference the canonical package path" >&2
  printf '%s\n' "$hooks" >&2
  exit 1
fi
echo ">> PASS: live tmux hooks reference the canonical path, not the retire segment"

echo ">> npm staging-path e2e complete"
smoke_contract_pass
