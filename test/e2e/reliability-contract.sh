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
secret_argv="github_pat_FAKE_SECRET_SHAPED_ARG_1234567890"
(cd "$owned_root" && exec -a "$secret_argv" sleep 30) &
secret_pid=$!
sleep 30 &
sibling_pid=$!
smoke_owned_process_inventory >"$artifacts/f01-residual-redacted"
if grep -Fq "$secret_argv" "$artifacts/f01-residual-redacted"; then
  echo "F01 residual process evidence leaked raw argv" >&2
  exit 1
fi
grep -Eq $'^[0-9]+\towned_by=cwd([+]argv)?\texecutable=sleep\tcmdline_sha256=[a-f0-9]{64}$' \
  "$artifacts/f01-residual-redacted"
smoke_cleanup_env
if kill -0 "$owned_pid" 2>/dev/null; then
  echo "F01 owned process survived quiescent cleanup" >&2
  exit 1
fi
if kill -0 "$secret_pid" 2>/dev/null; then
  echo "F01 secret-argv owned process survived quiescent cleanup" >&2
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

# F08: a wait that outlives its budget fails as a named timeout carrying the
# budget actually applied and the state the wait ended in. It never returns
# success, so a slow fixture cannot be reported by the next assertion as a
# product regression. The fixture is slow, not broken: the same predicate
# succeeds once E2E_WAIT_SCALE buys it enough time.
f08_marker="$artifacts/f08-slow-marker"
f08_log="$artifacts/f08-diagnostic.log"
printf 'f08 fixture diagnostic tail\n' >"$f08_log"
(
  sleep 1
  : >"$f08_marker"
) &
f08_fixture_pid=$!
set +e
SMOKE_WAIT_DIAGNOSTIC_LOG="$f08_log" \
  smoke_wait_until 0.2 "the F08 slow fixture marker" test -e "$f08_marker" \
  2>"$artifacts/f08.err"
f08_status=$?
set -e
if [[ "$f08_status" == "0" ]]; then
  echo "F08 wait helper reported success before its slow fixture existed" >&2
  exit 1
fi
grep -Fq 'timed out waiting for the F08 slow fixture marker' "$artifacts/f08.err"
grep -Eq '^  budget=0\.2s scale=1 elapsed_ms=[0-9]+ attempts=[0-9]+ last_status=[1-9][0-9]*$' \
  "$artifacts/f08.err"
grep -Fq "command: test -e $f08_marker" "$artifacts/f08.err"
grep -Fq 'f08 fixture diagnostic tail' "$artifacts/f08.err"
if ! (
  SMOKE_WAIT_SCALE=""
  E2E_WAIT_SCALE=20
  smoke_wait_until 0.2 "the F08 slow fixture marker under scale" test -e "$f08_marker"
); then
  echo "F08 scaled budget did not absorb the slow fixture" >&2
  exit 1
fi
wait "$f08_fixture_pid"

# F09: E2E_WAIT_SCALE moves the timeout instant itself, which is what lets a
# loaded runner be given time instead of a regression report.
f09_never_true() { return 7; }
f09_timeout_ms() {
  local scale="$1" started
  started="$(smoke_now_ms)"
  (
    SMOKE_WAIT_SCALE=""
    E2E_WAIT_SCALE="$scale"
    smoke_wait_until 0.4 "the F09 condition" f09_never_true
  ) >/dev/null 2>&1 || true
  printf '%s' "$(($(smoke_now_ms) - started))"
}
f09_scale_one="$(f09_timeout_ms 1)"
f09_scale_five="$(f09_timeout_ms 5)"
if ((f09_scale_five < f09_scale_one * 3)) || ((f09_scale_five < 1800)); then
  echo "F09 E2E_WAIT_SCALE did not expand the budget: scale1=${f09_scale_one}ms scale5=${f09_scale_five}ms" >&2
  exit 1
fi
if ! (
  # shellcheck disable=SC2034 # Both are consumed by the sourced wait helper.
  SMOKE_WAIT_SCALE=""
  # shellcheck disable=SC2034
  E2E_WAIT_SCALE=not-a-number
  smoke_wait_until 0.1 "the F09 fallback" true
) 2>"$artifacts/f09-invalid.err"; then
  echo "F09 invalid E2E_WAIT_SCALE broke the wait instead of falling back" >&2
  exit 1
fi
grep -Fq 'ignoring invalid E2E_WAIT_SCALE=not-a-number; using 1' "$artifacts/f09-invalid.err"
printf 'F09 wait budget timeout scale1=%sms scale5=%sms\n' "$f09_scale_one" "$f09_scale_five"

# F10: the budget scale is only useful where the e2e actually runs. `docker run`
# passes exactly the env it is told to, so a helper that reads E2E_WAIT_SCALE
# and a runner that never forwards it would leave every containerized wait
# pinned at scale 1 with nothing to show for it.
if ! grep -Eq '^[[:space:]]*-e E2E_WAIT_SCALE=' "$root/scripts/test-docker-run.sh"; then
  echo "F10 test-docker-run.sh does not forward E2E_WAIT_SCALE into the container" >&2
  exit 1
fi
if ! grep -Eq '^[[:space:]]*-e PROJMUX_E2E_REGISTRY_STRESS=' "$root/scripts/test-docker-run.sh"; then
  echo "F10 test-docker-run.sh does not forward the explicit Registry stress opt-in" >&2
  exit 1
fi

# F11: required L06 is cardinality-neutral holder/waiter acceptance. The real
# eight-way route must stay behind its explicit opt-in, and current diagnostics
# must keep their closed causes instead of falling back to other/unknown.
required_l06_source="$(sed -n '/# Required L06 owns one semantic guarantee:/,/^if \[\[ "${PROJMUX_E2E_REGISTRY_STRESS:-0}" == "1" \]\]; then/p' "$root/test/e2e/linux-smoke.sh")"
l06_source="$(sed -n '/# Required L06 owns one semantic guarantee:/,/^# 5\./p' "$root/test/e2e/linux-smoke.sh")"
if grep -Eq '\{1\.\.8\}|phase0-stress' <<<"$required_l06_source"; then
  echo "F11 required L06 still contains an unconditional eight-way route" >&2
  exit 1
fi
grep -Fq 'if [[ "${PROJMUX_E2E_REGISTRY_STRESS:-0}" == "1" ]]; then' "$root/test/e2e/linux-smoke.sh"
grep -Fq 'phase0-stress-results.tsv' "$root/test/e2e/linux-smoke.sh"
grep -Fq '>> opt-in Registry stress racer: index=%s pid=%s status=%s outcome=%s started_ms=%s finished_ms=%s' \
  "$root/test/e2e/linux-smoke.sh"
if grep -Fq '$create_registry.lock' <<<"$l06_source" ||
  grep -Fq 'stat -c %Y' <<<"$l06_source" ||
  grep -Eq 'SMOKE_L06_RACER_(PIDS|STATUSES|OUTCOMES)' <<<"$l06_source" ||
  grep -Eq 'l06-(other|unknown)|:(other|unknown):' <<<"$l06_source"; then
  echo "F11 L06 source retained marker-time inference, fixed racer arrays, or other/unknown attribution" >&2
  exit 1
fi
if grep -Eq 'l06-(other|unknown)|"(other|unknown)"' "$root/scripts/e2e-first-failure.py"; then
  echo "F11 current L06 diagnostics expose an other/unknown fallback" >&2
  exit 1
fi

# Phase-0 first-failure diagnostics are synthetic and scenario-owned. They
# exercise only the log formatter; no product timeout, retry, lock, or Registry
# semantics are changed to manufacture the failures.
diagnostic_root="$artifacts/first-failure"
mkdir -p "$diagnostic_root/controller" "$diagnostic_root/tmux"

SMOKE_L06_HOLDER_PID=4242
SMOKE_L06_HOLDER_HELD_MS="$(( $(date +%s) * 1000 - 1250 ))"
SMOKE_L06_HOLDER_RELEASED_MS="$((SMOKE_L06_HOLDER_HELD_MS + 1000))"
SMOKE_L06_OPERATION=concurrent-create-pane
SMOKE_L06_RELEASE_STATE=released
SMOKE_L06_RACER_EVENTS=("1:5001:1:deadline:$((SMOKE_L06_HOLDER_HELD_MS + 250)):$((SMOKE_L06_HOLDER_RELEASED_MS + 250))")
# The sourced formatter consumes this state indirectly. This explicit no-op
# read keeps that cross-file contract visible to standalone ShellCheck.
: "$SMOKE_L06_HOLDER_PID" "$SMOKE_L06_HOLDER_HELD_MS" "$SMOKE_L06_HOLDER_RELEASED_MS" \
  "$SMOKE_L06_OPERATION" "$SMOKE_L06_RELEASE_STATE" "${SMOKE_L06_RACER_EVENTS[*]}"
smoke_l06_failure_diagnostic >"$diagnostic_root/l06.out" 2>"$diagnostic_root/l06.err"
[[ ! -s "$diagnostic_root/l06.out" ]]

printf '%s\n' '{"projects":[{"name":"before","root":"/raw/home/private"}],"version":2}' >"$diagnostic_root/registry.before"
printf '%s\n' '{"projects":[{"name":"after","root":"/raw/home/private"}],"version":2}' >"$diagnostic_root/registry.after"
touch "$diagnostic_root/controller/pending"
python3 "$root/scripts/e2e-first-failure.py" l08 \
  --before "$diagnostic_root/registry.before" --after "$diagnostic_root/registry.after" \
  --controller-entries 1 --hook-processes 2 --owned-processes 3 --socket-entries 4 \
  >"$diagnostic_root/l08.out" 2>"$diagnostic_root/l08.err"
[[ ! -s "$diagnostic_root/l08.out" ]]

printf '0\n' >"$diagnostic_root/child.rc"
printf 'bounded output\n' >"$diagnostic_root/child.out"
printf 'bounded error\n' >"$diagnostic_root/child.err"
# shellcheck disable=SC2016 # Literal tmux IDs exercise the closed state parser.
printf '%s\n' '6001|$1|@2|%3|root' >"$diagnostic_root/clients"
# shellcheck disable=SC2016 # Literal tmux IDs exercise the closed state parser.
printf '%s\n' '6002|$1|@2|%3|1|0|0' >"$diagnostic_root/panes"
printf '\033[31mtimed out waiting for tmux client\033[0m\nargv: projmux --token github_pat_FAKE_SECRET_SHAPED_ARG_1234567890 --root /raw/home/private\n' \
  >"$diagnostic_root/client.log"
python3 "$root/scripts/e2e-first-failure.py" l16 \
  --child-pid 0 --rc "$diagnostic_root/child.rc" --out "$diagnostic_root/child.out" \
  --err "$diagnostic_root/child.err" --clients "$diagnostic_root/clients" \
  --panes "$diagnostic_root/panes" --tail "$diagnostic_root/client.log" \
  >"$diagnostic_root/l16.out" 2>"$diagnostic_root/l16.err"
[[ ! -s "$diagnostic_root/l16.out" ]]

for terminal_spec in \
  "L06:create-materialize:resource-controller:fixture-2:1569" \
  "L08:canonical-delete:delete-controller:fixture-1:4048" \
  "L16:discovery-pin:discovery-adapter:fixture-3:7496"; do
  IFS=: read -r terminal_scenario terminal_phase terminal_owner terminal_shard terminal_line <<<"$terminal_spec"
  python3 "$root/scripts/e2e-first-failure.py" terminal \
    --scenario "$terminal_scenario" --phase "$terminal_phase" --owner "$terminal_owner" \
    --shard "$terminal_shard" --status 1 --source test/e2e/linux-smoke.sh \
    --line "$terminal_line" --command "make test-e2e E2E_SCENARIO=$terminal_scenario" \
    --binary-sha256 "$(printf 'a%.0s' {1..64})" --state-sha256 "$(printf 'b%.0s' {1..64})" \
    2>"$diagnostic_root/$terminal_scenario.terminal"
done

python3 - "$diagnostic_root" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])

def record(name):
    prefix, payload = (root / f"{name}.err").read_text().strip().split(" ", 1)
    assert prefix == "E2E_DIAGNOSTIC"
    return json.loads(payload)

l06 = record("l06")
assert l06["scenario"] == "L06" and l06["attribution"] == "l06-registry-lock-deadline"
# The renamed holder field set travels under its own version; L08/L16 are
# unchanged and stay v1.
assert l06["schema"] == "projmux.e2e-diagnostic/v3", l06["schema"]
assert l06["holder"]["pid"] == 4242 and l06["holder"]["observed"] is True
assert l06["holder"]["held_ms"] < l06["holder"]["released_ms"]
assert l06["holder"]["operation"] == "concurrent-create-pane"
assert l06["holder"]["release"] == "released"
assert len(l06["racers"]) == 1 and l06["racers"][0]["outcome"] == "deadline"
assert l06["racers"][0]["started_ms"] < l06["racers"][0]["finished_ms"]

l08 = record("l08")
assert l08["schema"] == "projmux.e2e-diagnostic/v1", l08["schema"]
assert l08["scenario"] == "L08" and l08["attribution"] == "l08-state-drift"
assert l08["registry_before_sha256"] != l08["registry_after_sha256"]
assert l08["changed_json_paths"] == ["$.projects[0].name"]
assert l08["pending"] == {"controller_entries": 1, "hook_processes": 2, "owned_processes": 3, "socket_entries": 4}

l16 = record("l16")
assert l16["schema"] == "projmux.e2e-diagnostic/v1", l16["schema"]
assert l16["scenario"] == "L16" and l16["attribution"] == "l16-semantic-timeout"
assert l16["child"] == {"alive": False, "pid": 0, "state": ""}
assert all(l16["files"][name]["exists"] for name in ("rc", "out", "err"))
assert l16["tmux"]["clients"][0]["pane_id"] == "%3"
assert l16["tmux"]["panes"][0]["dead"] == "0"
assert l16["sanitized_tail"]["line_count"] == 2

joined = "\n".join((root / f"{name}.err").read_text() for name in ("l06", "l08", "l16"))
for forbidden in ("github_pat_", "/raw/home/private", "--token", "--root", "argv: projmux"):
    assert forbidden not in joined
assert "registry_before_sha256" in joined and '"projects"' not in joined

# A failed-job-log-only view is sufficient to join the common terminal row to
# exactly one scenario diagnostic, without asserting product-vs-harness blame.
for scenario, name, attribution in (
    ("L06", "l06", "l06-registry-lock-deadline"),
    ("L08", "l08", "l08-state-drift"),
    ("L16", "l16", "l16-semantic-timeout"),
):
    _, terminal_payload = (root / f"{scenario}.terminal").read_text().strip().split(" ", 1)
    terminal = json.loads(terminal_payload)
    diagnostic = record(name)
    assert terminal["scenario"] == diagnostic["scenario"] == scenario
    assert diagnostic["attribution"] == attribution
    assert terminal["command"] == terminal["replay"]
    assert "class" not in terminal and "class" not in diagnostic
PY

echo ">> F01/F02/F04/F06/F07/F08/F09/F10/F11 and L06/L08/L16 first-failure contracts passed"
