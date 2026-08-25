#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$root/test/lib/smoke.sh"

artifacts="${PROJMUX_E2E_ARTIFACTS:-$(mktemp -d)}"
mkdir -p "$artifacts"

# F01: an exact owned child is reaped only after the exact run-local socket
# boundary is checked; a sibling process without the owner root stays alive.
smoke_setup_env
owned_root="$PROJMUX_SMOKE_WORKDIR"
(cd "$owned_root" && exec sleep 30) &
owned_pid=$!
sleep 30 &
sibling_pid=$!
smoke_cleanup_env
if kill -0 "$owned_pid" 2>/dev/null; then
  echo "F01 owned process survived quiescent cleanup" >&2
  exit 1
fi
if ! kill -0 "$sibling_pid" 2>/dev/null; then
  echo "F01 cleanup mutated a sibling process" >&2
  exit 1
fi
kill "$sibling_pid"
wait "$sibling_pid" 2>/dev/null || true

# F02: the oracle accepts initial no-op and requires an empty fixed point; it
# rejects a repeated changed report as a cycle rather than retrying blindly.
fixed_counter=0
fixed_report() {
  fixed_counter=$((fixed_counter + 1))
  if [[ "$fixed_counter" == "1" ]]; then
    printf '{"outcome":"changed","items":["x"],"pass":1}\n'
  else
    printf '{"outcome":"no-op","items":[]}\n'
  fi
}
smoke_bounded_fixed_point "$artifacts/f02" fixed_report

# F04: an old row before the invocation offset is not evidence for the current
# frame. Only the post-offset semantic row can complete the wait.
frame="$artifacts/frame.log"
printf 'offline Project old\n' >"$frame"
frame_offset="$(stat -c %s "$frame")"
(sleep 0.05; printf 'offline Project current\n' >>"$frame") &
frame_pid=$!
smoke_wait_for_current_frame "offline Project" "$frame" "$frame_offset" "offline Project current"
wait "$frame_pid"

# F06: reject the empty selector at its producer receipt boundary.
if smoke_require_uid "F06 producer" project "uid:" 2>"$artifacts/f06.err"; then
  echo "F06 accepted an empty UID selector" >&2
  exit 1
fi
if smoke_require_uid "F06 producer" project "proj-short" 2>>"$artifacts/f06.err"; then
  echo "F06 accepted a malformed raw UID" >&2
  exit 1
fi
smoke_require_uid "F06 producer" project "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"

# F07: every intentional failure retains typed first-attempt terminal evidence.
f07=(python3 "$root/scripts/e2e-evidence.py" record --directory "$artifacts/f07" --scenario-id L17 --suite reliability --attempt 1 --phase exit-reconcile --owner exit-reconciler)
"${f07[@]}" --class environment --outcome begin --elapsed-ms 0 >/dev/null
"${f07[@]}" --class harness-lifecycle --outcome fail --elapsed-ms 1 >/dev/null
python3 "$root/scripts/e2e-evidence.py" validate --terminal "$artifacts/f07/summary.jsonl"

echo ">> F01/F02/F04/F06/F07 reliability contracts passed"
