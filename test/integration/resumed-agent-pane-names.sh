#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for the resumed Agent Pane name handoff.
# TestContinueReplayCarriesOldAgentPaneNamesThroughRealTmux drives Continue
# topology replay (`reconcile resources --materialize-project`) and
# TestAgentResumeCarriesOldAgentPaneNamesThroughRealTmux drives `agent resume`
# on an isolated tmux server with a fake `tail -f /dev/null` provider. Old
# Agent Pane names `reviewer`, `default-pane` and `-L` must land on the new Pane
# UID in the Registry and in #{@projmux_pane_label}; an automatic old name must
# not be carried. PMX_TEST_RESUMED_PANE_NAME_REAL_TMUX=1 turns a missing tmux
# into a failure, and both PASS lines are required so a skip or an empty -run
# match cannot pass.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output="$(mktemp "${TMPDIR:-/tmp}/projmux-resumed-agent-pane-names.XXXXXX")"
trap 'rm -f -- "$output"' EXIT
cd "$root"
tests=(
  TestContinueReplayCarriesOldAgentPaneNamesThroughRealTmux
  TestAgentResumeCarriesOldAgentPaneNamesThroughRealTmux
)
status=0
PMX_TEST_RESUMED_PANE_NAME_REAL_TMUX=1 \
  go test ./internal/app -run '^(TestContinueReplayCarriesOldAgentPaneNamesThroughRealTmux|TestAgentResumeCarriesOldAgentPaneNamesThroughRealTmux)$' -count=1 -v >"$output" 2>&1 || status=$?
cat "$output"
if [[ "$status" != 0 ]]; then
  exit "$status"
fi
for test in "${tests[@]}"; do
  if ! grep -qF -- "--- PASS: $test (" "$output"; then
    echo "resumed Agent Pane name real-tmux test $test did not pass" >&2
    exit 1
  fi
done
