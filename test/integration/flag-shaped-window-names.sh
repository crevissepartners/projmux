#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for Window names spelled like tmux flags (`-L`, `-t`,
# `-Lx`). The Go test drives the materializer's Window create, UID claim, and
# stable-name mirror on an isolated tmux server and requires #{window_name} and
# #{@projmux_window_name} to equal the input. PMX_TEST_FLAG_NAME_REAL_TMUX=1
# turns a missing tmux into a failure, and the PASS line is required so a skip
# or an empty -run match cannot pass.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output="$(mktemp "${TMPDIR:-/tmp}/projmux-flag-shaped-window-names.XXXXXX")"
trap 'rm -f -- "$output"' EXIT
cd "$root"
status=0
PMX_TEST_FLAG_NAME_REAL_TMUX=1 \
  go test ./internal/app -run '^TestMaterializerCreatesFlagShapedWindowNamesThroughRealTmux$' -count=1 -v >"$output" 2>&1 || status=$?
cat "$output"
if [[ "$status" != 0 ]]; then
  exit "$status"
fi
if ! grep -q -- '--- PASS: TestMaterializerCreatesFlagShapedWindowNamesThroughRealTmux' "$output"; then
  echo "flag-shaped Window name real-tmux test did not pass" >&2
  exit 1
fi
