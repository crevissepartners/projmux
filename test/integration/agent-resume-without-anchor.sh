#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for Backlog 173: an absent anchor Pane is named as absent,
# and `agent resume` needs no anchor Pane at all.
# TestAbsentAnchorPaneAnswersBlankReceiptThroughRealTmux pins the tmux answer
# the classification reads -- exit 0 with blank $/@/% for a Pane that is gone --
# and TestAgentResumeNeedsNoAnchorPaneThroughRealTmux drives `agent resume` on
# an isolated server with a fake `tail -f /dev/null` provider while TMUX_PANE
# and __PROJMUX_RUNTIME_ANCHOR_PANE both name a Pane the server never issued.
# TestStartProjectRefusesADeadAnchorThroughRealTmux pins that `start project`
# over that dead anchor is refused as an absent Pane, with zero writes.
# TestAgentResumeIntoStoppedProjectNeedsNoAnchorPaneThroughRealTmux resumes into
# a stopped Project over that dead anchor and requires resume to start its one
# session.
# PMX_TEST_RESUME_NO_ANCHOR_REAL_TMUX=1 turns a missing tmux into a failure, and
# every PASS line is required so a skip or an empty -run match cannot pass.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output="$(mktemp "${TMPDIR:-/tmp}/projmux-agent-resume-without-anchor.XXXXXX")"
trap 'rm -f -- "$output"' EXIT
cd "$root"
tests=(
  TestAbsentAnchorPaneAnswersBlankReceiptThroughRealTmux
  TestAgentResumeNeedsNoAnchorPaneThroughRealTmux
  TestStartProjectRefusesADeadAnchorThroughRealTmux
  TestAgentResumeIntoStoppedProjectNeedsNoAnchorPaneThroughRealTmux
)
status=0
PMX_TEST_RESUME_NO_ANCHOR_REAL_TMUX=1 \
  go test ./internal/app -run '^(TestAbsentAnchorPaneAnswersBlankReceiptThroughRealTmux|TestAgentResumeNeedsNoAnchorPaneThroughRealTmux|TestStartProjectRefusesADeadAnchorThroughRealTmux|TestAgentResumeIntoStoppedProjectNeedsNoAnchorPaneThroughRealTmux)$' -count=1 -v >"$output" 2>&1 || status=$?
cat "$output"
if [[ "$status" != 0 ]]; then
  exit "$status"
fi
for test in "${tests[@]}"; do
  if ! grep -qF -- "--- PASS: $test (" "$output"; then
    echo "agent resume anchor real-tmux test $test did not pass" >&2
    exit 1
  fi
done
