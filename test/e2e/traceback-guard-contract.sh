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
      # The job-log reader sleeps before draining, so the stderr tee blocks once
      # the job-log pipe (capacity A) is full, and its capture file then holds
      # at most A bytes. A filler of A + min(B/2, 2048) bytes (B: the tee's
      # input pipe) puts the traceback past anything the capture can hold,
      # while the filler, the traceback, and the tee's in-flight chunk still fit
      # in B and A together, so this shell never blocks. A runner's pipes are
      # not always 64 KiB (the GitHub ubuntu-26.04 job-log pipe is 1 MiB), so the
      # case first shrinks both pipes to at most 64 KiB (shrinking is always
      # allowed) and then measures what it really has. The fixtures start it
      # from other sizes through the PROJMUX_TRACEBACK_GUARD_*_BYTES targets.
      measured="$(python3 -c '
import errno
import fcntl
import sys

# F_SETPIPE_SZ is 1031 and F_GETPIPE_SZ is 1032.
limit = 65536
rows = []
cause = ""
for fd, target in ((sys.argv[1], sys.argv[3]), (sys.argv[2], sys.argv[4])):
    start = end = 0
    try:
        start = fcntl.fcntl(int(fd), 1032)
        want = min(start, int(target) if target else limit, limit)
        if want < start:
            fcntl.fcntl(int(fd), 1031, want)
        end = fcntl.fcntl(int(fd), 1032)
    except OSError as exc:
        cause = cause or f"cannot measure or resize the pipe on fd {fd} ({errno.errorcode.get(exc.errno, exc.errno)})"
    rows.append((start, end))
print(rows[0][0], rows[0][1], rows[1][0], rows[1][1], cause)
' "$SMOKE_CONTRACT_SAVED_STDERR_FD" 2 "${PROJMUX_TRACEBACK_GUARD_JOBLOG_BYTES:-}" "${PROJMUX_TRACEBACK_GUARD_TEE_BYTES:-}")" || true
      read -r job_log_start job_log_capacity tee_start tee_capacity cause <<<"$measured"
      job_log_capacity="${job_log_capacity:-0}"
      tee_capacity="${tee_capacity:-0}"
      filler=$((job_log_capacity + (tee_capacity / 2 < 2048 ? tee_capacity / 2 : 2048)))
      unavailable="$cause"
      if [[ -z "$unavailable" ]] && ((job_log_capacity <= 0 || tee_capacity <= 0)); then
        unavailable="pipe capacity not measurable"
      elif [[ -z "$unavailable" ]] && ((tee_capacity < 8192)); then
        # The kernel gives a new pipe at least two pages. A one-page pipe holds
        # a single slot, which the tee fills with a few small writes.
        unavailable="tee input pipe below two pages $tee_capacity"
      fi
      measure="job-log=$job_log_capacity tee-input=$tee_capacity filler=$filler job-log-start=${job_log_start:-0} tee-input-start=${tee_start:-0}"
      if [[ -n "$unavailable" ]]; then
        echo "flush-boundary: precondition unavailable: $unavailable $measure" >&2
        exit 97
      fi
      echo "flush-boundary-tee $(tee --version 2>&1 | sed -n 1p)" >&2
      echo "flush-boundary-capacity $measure" >&2
      head -c "$filler" /dev/zero | tr '\0' 'f' >&2
      printf '\n' >&2
      raise_key_error || true
      # Prove the lag is real at the decision point, or the case means nothing.
      if grep -aqF "KeyError: 'routeIncarnation'" "$SMOKE_CONTRACT_OUTPUT_STDERR"; then
        echo "flush-boundary: precondition unavailable: the capture already holds the traceback; capture $(stat -c %s "$SMOKE_CONTRACT_OUTPUT_STDERR") bytes $measure" >&2
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
  local name="$1" lag="$2" joblog_bytes="${3:-}" tee_bytes="${4:-}" status=0
  # The child's EXIT-trap cleanup needs three quiet /proc samples inside a 1s
  # budget; a loaded machine misses that, so widen the default scale.
  local -a child_env=(
    PROJMUX_TRACEBACK_GUARD_CASE="$name"
    PROJMUX_E2E_ARTIFACTS="$tmp/$name"
    PROJMUX_E2E_ATTEMPT="$contract_attempt"
    GITHUB_RUN_ATTEMPT="$decoy_attempt"
    E2E_WAIT_SCALE="${E2E_WAIT_SCALE:-10}"
  )
  rm -rf "${tmp:?}/$name"
  if [[ -n "$joblog_bytes" ]]; then
    child_env+=(PROJMUX_TRACEBACK_GUARD_JOBLOG_BYTES="$joblog_bytes")
  fi
  if [[ -n "$tee_bytes" ]]; then
    child_env+=(PROJMUX_TRACEBACK_GUARD_TEE_BYTES="$tee_bytes")
  fi
  if [[ "$lag" == "lag" ]]; then
    # The job-log reader starts draining stderr only after three seconds.
    {
      # Fixture: a job-log pipe larger than 64 KiB, as on the GitHub
      # ubuntu-26.04 runner. A user over the pipe limit is refused; the
      # outer check then skips that fixture by name.
      if [[ "${joblog_bytes:-0}" -gt 65536 ]]; then
        python3 -c '
import errno
import fcntl
import sys

try:
    fcntl.fcntl(1, 1031, int(sys.argv[2]))
except OSError as exc:
    open(sys.argv[1], "w").write(errno.errorcode.get(exc.errno, str(exc.errno)))
' "$tmp/$name.enlarge" "$joblog_bytes"
      fi
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

# check_case NAME WANT [EXCEPTION] [lag] [JOBLOG_BYTES] [TEE_BYTES]: WANT is "fail" (the guard fired with
# EXCEPTION) or "pass" (the scenario recorded pass and no guard line exists).
check_case() {
  local name="$1" want="$2" exception="${3:-}" lag="${4:-}" joblog_bytes="${5:-}" tee_bytes="${6:-}" status verdict capacity=""
  local label="$name${joblog_bytes:+ job-log=$joblog_bytes}${tee_bytes:+ tee-input=$tee_bytes}"
  run_child "$name" "$lag" "$joblog_bytes" "$tee_bytes"
  if [[ -s "$tmp/$name.enlarge" ]]; then
    echo "skip $label: cannot enlarge the job-log pipe ($(<"$tmp/$name.enlarge"))"
    return 0
  fi
  read -r status <"$tmp/$name.rc"
  verdict="$(python3 - "$tmp/$name.out" "$tmp/$name.err" "$tmp/$name/L20-attempt-$contract_attempt.json" \
    "$name" "$want" "$exception" "$status" "$pass_line" "$joblog_bytes" "$tee_bytes" <<'PY'
import json
import pathlib
import sys

out_path, err_path, artifact_path, name, want, exception, status, pass_line, joblog_bytes, tee_bytes = sys.argv[1:11]
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
measured = []
if name == "flush-boundary":
    measured = [line for line in err_lines if line.startswith("flush-boundary-capacity ")]
    fields = dict(part.split("=", 1) for part in measured[0].split()[1:]) if len(measured) == 1 else {}
    if not fields:
        problems.append(f"want one flush-boundary-capacity line, got {measured}")
    else:
        # Each pipe ends at the smaller of its start size, its fixture target, and 64 KiB.
        want_job_log = min(int(fields["job-log-start"]), int(joblog_bytes or 65536), 65536)
        want_tee = min(int(fields["tee-input-start"]), int(tee_bytes or 65536), 65536)
        if int(fields["job-log"]) != want_job_log or int(fields["tee-input"]) != want_tee:
            problems.append(f"the pipes were not normalized to {want_job_log}/{want_tee}: {measured[0]}")
        elif joblog_bytes and int(joblog_bytes) > 65536 and int(fields["job-log-start"]) != int(joblog_bytes):
            problems.append(f"the job-log pipe did not start at {joblog_bytes}: {measured[0]}")
        elif "f" * int(fields["filler"]) not in err:
            problems.append("the filler block did not reach the job log intact")
        elif int(fields["filler"]) <= int(fields["job-log"]):
            problems.append(f"filler does not exceed the job-log pipe: {measured[0]}")
print(" ".join(measured) + "|" + "; ".join(problems))
PY
)"
  capacity="${verdict%%|*}"
  verdict="${verdict#*|}"
  if [[ -n "$verdict" ]]; then
    echo "FAIL traceback-guard-contract case $label: $verdict" >&2
    echo "--- child stderr ($name, first 4000 bytes) ---" >&2
    # The named cause can sit behind the filler block; print it first.
    grep -a '^flush-boundary' "$tmp/$name.err" | cut -c1-400 >&2 || true
    head -c 4000 "$tmp/$name.err" >&2
    failed=$((failed + 1))
  else
    tee_line="$(grep -a -m1 '^flush-boundary-tee ' "$tmp/$name.err" || true)"
    echo "ok $label rc=$status $(grep -m1 '^E2E_CONTRACT id=L20 guard=' "$tmp/$name.err" || echo 'no guard line')${capacity:+ $capacity}${tee_line:+ $tee_line}"
  fi
  cases=$((cases + 1))
}

check_case consumed fail "KeyError: 'routeIncarnation'"
check_case cmdsub fail "KeyError: 'routeIncarnation'"
check_case chained fail "ValueError: second"
check_case stdout fail "KeyError: 'routeIncarnation'"
check_case flush-boundary fail "KeyError: 'routeIncarnation'" lag
# The same case on pipes smaller than 64 KiB, and on the GitHub runner's mix of
# a 1 MiB job-log pipe with a small tee input pipe.
check_case flush-boundary fail "KeyError: 'routeIncarnation'" lag 8192 8192
check_case flush-boundary fail "KeyError: 'routeIncarnation'" lag 1048576 8192
check_case clean pass
check_case redirected pass
check_case before-begin pass
check_case after-pass pass

# check_unavailable LABEL LAG JOBLOG_BYTES TEE_BYTES WANT: the case cannot make
# its lag here, so it must fail with 97 and a precondition line naming WANT,
# never pass or skip.
check_unavailable() {
  local label="$1" lag="$2" joblog_bytes="$3" tee_bytes="$4" want="$5" status
  run_child flush-boundary "$lag" "$joblog_bytes" "$tee_bytes"
  if [[ -s "$tmp/flush-boundary.enlarge" ]]; then
    echo "skip $label: cannot enlarge the job-log pipe ($(<"$tmp/flush-boundary.enlarge"))"
    return 0
  fi
  read -r status <"$tmp/flush-boundary.rc"
  if [[ "$status" != "97" ]] || ! grep -qF "flush-boundary: precondition unavailable: $want" "$tmp/flush-boundary.err"; then
    echo "FAIL traceback-guard-contract case $label: want rc 97 and a precondition naming '$want', got rc $status" >&2
    grep -a '^flush-boundary' "$tmp/flush-boundary.err" | cut -c1-400 >&2 || true
    head -c 4000 "$tmp/flush-boundary.err" >&2
    failed=$((failed + 1))
  else
    echo "ok $label rc=$status $(grep -m1 'precondition unavailable' "$tmp/flush-boundary.err")"
  fi
  cases=$((cases + 1))
}

# Without a job-log pipe nothing can be measured or held back, and a tee input
# pipe of one page cannot be held back reliably.
check_unavailable "flush-boundary without a job-log pipe" "" "" "" "cannot measure or resize the pipe on fd "
check_unavailable "flush-boundary job-log=4096 tee-input=4096" lag 4096 4096 "tee input pipe below two pages 4096"
check_unavailable "flush-boundary job-log=1048576 tee-input=4096" lag 1048576 4096 "tee input pipe below two pages 4096"

if [[ "$failed" != "0" ]]; then
  echo "FAIL traceback-guard-contract ($failed of $cases cases)" >&2
  exit 1
fi
echo "PASS traceback-guard-contract ($cases cases)"
