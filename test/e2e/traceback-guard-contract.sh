#!/usr/bin/env bash
set -euo pipefail

# Pins the smoke harness's Python traceback guard. smoke_contract_pass must
# refuse to record pass when the contract shell's own stdout or stderr, between
# smoke_contract_begin and that call, carries a Python traceback, even when the
# child's non-zero status was consumed. Such a scenario ends through the normal
# fail path: an E2E_TERMINAL record at the pass line, an attempt artifact with
# outcome=fail, a non-zero exit, and one guard line naming the scenario and the
# last traceback's exception. Output outside that interval, or redirected away
# from the job log, never trips the guard.
#
# The flush-boundary case writes a traceback right before the pass call while
# the tee is held back by a job-log reader that has not started draining, so a
# pass decided before the tee caught up would miss it.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
self="$root/test/e2e/traceback-guard-contract.sh"

if [[ -n "${PROJMUX_TRACEBACK_GUARD_CASE:-}" ]]; then
  # shellcheck disable=SC1091 # The shared library is sourced at runtime; scripts/security.sh lints it on its own.
  source "$root/test/lib/smoke.sh"
  smoke_setup_env
  PROJMUX_E2E_SUITE="traceback-guard"
  export PROJMUX_E2E_SUITE
  smoke_contract_install_trap
  trap smoke_cleanup_env EXIT
  raise_key_error() {
    python3 -c 'raise KeyError("routeIncarnation")'
  }
  if [[ "$PROJMUX_TRACEBACK_GUARD_CASE" == "before-begin" ]]; then
    raise_key_error || true
  fi
  if [[ "$PROJMUX_TRACEBACK_GUARD_CASE" == "clean" ]]; then
    # Begin from inside the owned root, so the tees start there too.
    cd "$PROJMUX_SMOKE_WORKDIR"
  fi
  smoke_contract_begin L20 heterogeneous-dialogue provider-neutral-broker
  echo "guard-case-stdout $PROJMUX_TRACEBACK_GUARD_CASE"
  echo "guard-case-stderr $PROJMUX_TRACEBACK_GUARD_CASE" >&2
  case "$PROJMUX_TRACEBACK_GUARD_CASE" in
    consumed)
      raise_key_error || true
      ;;
    cmdsub)
      # shellcheck disable=SC2034
      value="$(raise_key_error)" || true
      ;;
    chained)
      python3 -c '
try:
    raise KeyError("routeIncarnation")
except KeyError:
    raise ValueError("second")
' || true
      ;;
    stdout)
      python3 -c 'raise KeyError("routeIncarnation")' 2>&1 || true
      ;;
    redirected)
      raise_key_error >"$PROJMUX_SMOKE_WORKDIR/redirected.log" 2>&1 || true
      grep -Fq "KeyError: 'routeIncarnation'" "$PROJMUX_SMOKE_WORKDIR/redirected.log"
      ;;
    flush-boundary)
      # The job-log reader sleeps before draining. 96 KiB overfills its 64 KiB
      # pipe, so the stderr tee blocks mid-filler, while the rest of the filler
      # and the traceback still fit in the tee's own input pipe without
      # blocking this shell. The traceback is therefore written, and pass is
      # called, while the capture file does not hold it yet.
      head -c 98304 /dev/zero | tr '\0' 'f' >&2
      printf '\n' >&2
      raise_key_error || true
      # Prove the lag is real at the decision point, or the case means nothing.
      if grep -aqF "KeyError: 'routeIncarnation'" "$SMOKE_CONTRACT_OUTPUT_STDERR"; then
        echo "flush-boundary: the capture already holds the traceback; no lag to test" >&2
        exit 97
      fi
      ;;
    clean)
      # The tees are not owned smoke processes: cleanup must neither wait for
      # nor reap them, and an L08-style inventory must not count them.
      cd /
      inventory="$(smoke_owned_process_inventory)"
      if [[ -n "$inventory" ]]; then
        printf 'the output tees look owned:\n%s\n' "$inventory" >&2
        exit 96
      fi
      ;;
    before-begin | after-pass) ;;
    *)
      echo "unknown case $PROJMUX_TRACEBACK_GUARD_CASE" >&2
      exit 98
      ;;
  esac
  smoke_contract_pass # traceback-guard: pass-line
  if [[ "$PROJMUX_TRACEBACK_GUARD_CASE" == "after-pass" ]]; then
    raise_key_error || true
  fi
  exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# Pin the attempt the children name their artifact after, and hand them a decoy
# GITHUB_RUN_ATTEMPT so a dropped pin fails on every run, not only on a rerun.
contract_attempt=1
decoy_attempt=$((contract_attempt + 1))

pass_line=0
number=0
while IFS= read -r text || [[ -n "$text" ]]; do
  number=$((number + 1))
  if [[ "$text" == *"smoke_contract_pass # traceback-guard: pass-line" ]]; then
    if [[ "$pass_line" != "0" ]]; then
      echo "FAIL traceback-guard-contract: pass-line marker found twice" >&2
      exit 1
    fi
    pass_line="$number"
  fi
done <"$self"
if [[ "$pass_line" == "0" ]]; then
  echo "FAIL traceback-guard-contract: pass-line marker missing" >&2
  exit 1
fi

cases=0
failed=0

run_child() {
  local name="$1" lag="$2" status=0
  # The child's EXIT-trap cleanup needs three quiet /proc samples inside a 1s
  # budget; a loaded machine misses that, so widen the default scale.
  local -a child_env=(
    PROJMUX_TRACEBACK_GUARD_CASE="$name"
    PROJMUX_E2E_ARTIFACTS="$tmp/$name"
    PROJMUX_E2E_ATTEMPT="$contract_attempt"
    GITHUB_RUN_ATTEMPT="$decoy_attempt"
    E2E_WAIT_SCALE="${E2E_WAIT_SCALE:-10}"
  )
  if [[ "$lag" == "lag" ]]; then
    # The job-log reader starts draining stderr only after three seconds.
    {
      env "${child_env[@]}" "$self" 2>&1 >"$tmp/$name.out" || status=$?
      printf '%s\n' "$status" >"$tmp/$name.rc"
    } | {
      sleep 3
      cat >"$tmp/$name.err"
    }
  else
    env "${child_env[@]}" "$self" >"$tmp/$name.out" 2>"$tmp/$name.err" || status=$?
    printf '%s\n' "$status" >"$tmp/$name.rc"
  fi
}

# check_case NAME WANT [EXCEPTION] [lag]: WANT is "fail" (the guard fired with
# EXCEPTION) or "pass" (the scenario recorded pass and no guard line exists).
check_case() {
  local name="$1" want="$2" exception="${3:-}" lag="${4:-}" status verdict
  run_child "$name" "$lag"
  read -r status <"$tmp/$name.rc"
  verdict="$(python3 - "$tmp/$name.out" "$tmp/$name.err" "$tmp/$name/L20-attempt-$contract_attempt.json" \
    "$name" "$want" "$exception" "$status" "$pass_line" <<'PY'
import json
import pathlib
import sys

out_path, err_path, artifact_path, name, want, exception, status, pass_line = sys.argv[1:9]
status, pass_line = int(status), int(pass_line)
out = pathlib.Path(out_path).read_text(errors="replace")
err = pathlib.Path(err_path).read_text(errors="replace")
err_lines = err.splitlines()
problems = []
terminals = [json.loads(line.split(" ", 1)[1]) for line in err_lines if line.startswith("E2E_TERMINAL ")]
guards = [line for line in err_lines if line.startswith("E2E_CONTRACT id=L20 guard=")]
# The scenario's own lines must still reach the job log on their original fds.
if f"guard-case-stdout {name}" not in out.splitlines():
    problems.append("the scenario's stdout line is missing from the child's stdout")
if f"guard-case-stderr {name}" not in err_lines:
    problems.append("the scenario's stderr line is missing from the child's stderr")
artifact = pathlib.Path(artifact_path)
outcome = json.loads(artifact.read_text()).get("outcome") if artifact.is_file() else None
if want == "pass":
    if status != 0:
        problems.append(f"child exit status: expected 0, got {status}")
    if outcome != "pass":
        problems.append(f"artifact outcome: expected pass, got {outcome}")
    if terminals:
        problems.append(f"want no E2E_TERMINAL record, got {len(terminals)}")
    if guards:
        problems.append(f"want no guard line, got {guards}")
else:
    if status == 0:
        problems.append("child exit status: expected non-zero, got 0")
    if outcome != "fail":
        problems.append(f"artifact outcome: expected fail, got {outcome}")
    if len(terminals) != 1:
        problems.append(f"E2E_TERMINAL records: expected 1, got {len(terminals)}")
    else:
        record = terminals[0]
        if record["line"] != pass_line:
            problems.append(f"terminal line: expected the pass call {pass_line}, got {record['line']}")
        if record["source"] != "test/e2e/traceback-guard-contract.sh":
            problems.append(f"terminal source: got {record['source']!r}")
    want_guard = [line for line in guards if " guard=python-traceback " in line and line.endswith(f" exception={exception}")]
    if len(guards) != 1 or len(want_guard) != 1:
        problems.append(f"want one python-traceback guard line with exception={exception}, got {guards}")
    elif err_lines.index(guards[0]) > min(i for i, line in enumerate(err_lines) if line.startswith("E2E_TERMINAL ")):
        problems.append("the guard line must precede the terminal record")
if name == "flush-boundary" and "f" * 98304 not in err:
    problems.append("the filler block did not reach the job log intact")
print("; ".join(problems))
PY
)"
  if [[ -n "$verdict" ]]; then
    echo "FAIL traceback-guard-contract case $name: $verdict" >&2
    echo "--- child stderr ($name, first 4000 bytes) ---" >&2
    head -c 4000 "$tmp/$name.err" >&2
    failed=$((failed + 1))
  else
    echo "ok $name rc=$status $(grep -m1 '^E2E_CONTRACT id=L20 guard=' "$tmp/$name.err" || echo 'no guard line')"
  fi
  cases=$((cases + 1))
}

check_case consumed fail "KeyError: 'routeIncarnation'"
check_case cmdsub fail "KeyError: 'routeIncarnation'"
check_case chained fail "ValueError: second"
check_case stdout fail "KeyError: 'routeIncarnation'"
check_case flush-boundary fail "KeyError: 'routeIncarnation'" lag
check_case clean pass
check_case redirected pass
check_case before-begin pass
check_case after-pass pass

if [[ "$failed" != "0" ]]; then
  echo "FAIL traceback-guard-contract ($failed of $cases cases)" >&2
  exit 1
fi
echo "PASS traceback-guard-contract ($cases cases)"
