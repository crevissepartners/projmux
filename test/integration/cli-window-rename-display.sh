#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for the public `projmux rename window` display contract.
# TestCLIWindowRenameConvergesTheTabThroughRealTmux drives the CLI route on an
# isolated tmux server: the Registry name, #{@projmux_window_name} and the tab
# #{window_name} must agree after every rename, flag-shaped names included, and
# re-running the same name must repair a tab left stale beside a matching stable
# name. PMX_TEST_CLI_WINDOW_RENAME_REAL_TMUX=1 turns a missing tmux into a
# failure, and the PASS line is required so a skip or an empty -run match cannot
# pass.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output="$(mktemp "${TMPDIR:-/tmp}/projmux-cli-window-rename-display.XXXXXX")"
trap 'rm -f -- "$output"' EXIT
cd "$root"
test=TestCLIWindowRenameConvergesTheTabThroughRealTmux
status=0
PMX_TEST_CLI_WINDOW_RENAME_REAL_TMUX=1 \
  go test ./internal/app -run "^${test}\$" -count=1 -v >"$output" 2>&1 || status=$?
cat "$output"
if [[ "$status" != 0 ]]; then
  exit "$status"
fi
if ! grep -qF -- "--- PASS: $test (" "$output"; then
  echo "CLI Window rename display real-tmux test $test did not pass" >&2
  exit 1
fi
