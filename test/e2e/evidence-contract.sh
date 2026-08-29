#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if [[ "${PROJMUX_E2E_INTENTIONAL_FAILURE:-}" == "1" ]]; then
  source "$root/test/lib/smoke.sh"
  smoke_setup_env
  PROJMUX_E2E_SUITE="intentional-failure"
  export PROJMUX_E2E_SUITE
  smoke_contract_install_trap
  trap smoke_cleanup_env EXIT
  smoke_contract_begin L17 exit-reconcile exit-reconciler
  false
  exit 99
fi

if [[ "${PROJMUX_E2E_INTENTIONAL_EXIT:-}" == "1" ]]; then
  source "$root/test/lib/smoke.sh"
  smoke_setup_env
  PROJMUX_E2E_SUITE="intentional-failure"
  export PROJMUX_E2E_SUITE
  smoke_contract_install_trap
  trap smoke_cleanup_env EXIT
  smoke_contract_begin L06 create-materialize resource-controller
  exit 23
fi

# Bash reports BASH_LINENO[0] as 0 for a failure raised at the top level of a
# sourced fixture. This branch forces that exact shape through the real harness
# path so the degraded attribution stays observable end to end.
if [[ "${PROJMUX_E2E_INTENTIONAL_ZERO_LINE:-}" == "1" ]]; then
  source "$root/test/lib/smoke.sh"
  smoke_setup_env
  PROJMUX_E2E_SUITE="intentional-failure"
  export PROJMUX_E2E_SUITE
  smoke_contract_install_trap
  trap smoke_cleanup_env EXIT
  smoke_contract_begin L06 create-materialize resource-controller
  smoke_contract_fail 42 0
  exit 42
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

record=(python3 "$root/scripts/e2e-evidence.py" record --directory "$tmp/golden" --scenario-id L01 --suite linux-bootstrap --attempt 1 --phase bootstrap --owner harness)
"${record[@]}" --class unattributed --outcome begin --elapsed-ms 0 >"$tmp/begin.out"
"${record[@]}" --class environment --outcome fail --elapsed-ms 17 >"$tmp/fail.out"
python3 "$root/scripts/e2e-evidence.py" validate --terminal "$tmp/golden/summary.jsonl"

terminal_command=(python3 "$root/scripts/e2e-first-failure.py" terminal \
  --scenario L01 --phase bootstrap --owner harness --shard fixture-1 \
  --status 17 --source test/e2e/linux-smoke.sh --line 123 \
  --command "make test-e2e E2E_SCENARIO=L01" \
  --binary-sha256 "$(printf 'a%.0s' {1..64})" \
  --state-sha256 "$(printf 'b%.0s' {1..64})")
"${terminal_command[@]}" >"$tmp/terminal-record.out" 2>"$tmp/terminal-record.err"
[[ ! -s "$tmp/terminal-record.out" ]]
terminal_golden='E2E_TERMINAL {"attribution":"complete","binary_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","command":"make test-e2e E2E_SCENARIO=L01","line":123,"owner":"harness","phase":"bootstrap","replay":"make test-e2e E2E_SCENARIO=L01","scenario":"L01","schema":"projmux.e2e-terminal/v1","shard":"fixture-1","source":"test/e2e/linux-smoke.sh","state_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","status":17}'
if [[ "$(cat "$tmp/terminal-record.err")" != "$terminal_golden" ]]; then
  echo "stderr terminal JSON golden mismatch" >&2
  diff -u <(printf '%s\n' "$terminal_golden") "$tmp/terminal-record.err" >&2 || true
  exit 1
fi
if python3 "$root/scripts/e2e-first-failure.py" terminal \
  --scenario L01 --phase bootstrap --owner harness --shard fixture-1 \
  --status 17 --source test/e2e/linux-smoke.sh --line 123 \
  --command "github_pat_FAKE_SECRET_SHAPED_ARG_1234567890" \
  >"$tmp/terminal-private.out" 2>"$tmp/terminal-private.err"; then
  echo "terminal schema accepted a raw non-allowlisted command" >&2
  exit 1
fi
if grep -Fq "github_pat_FAKE_SECRET_SHAPED_ARG_1234567890" \
  "$tmp/terminal-record.err" "$tmp/terminal-private.err" "$tmp/terminal-private.out"; then
  echo "terminal schema or its rejection leaked a raw command" >&2
  exit 1
fi

# Degraded attribution must never cost the whole record. Each case drops exactly
# the field that broke its own rule, keeps scenario/phase/owner/shard, and exits 0.
assert_partial_terminal() {
  local label="$1"
  shift
  if ! python3 "$root/scripts/e2e-first-failure.py" terminal \
    --scenario L01 --phase bootstrap --owner harness --shard fixture-1 \
    --command "make test-e2e E2E_SCENARIO=L01" "$@" \
    >"$tmp/partial-$label.out" 2>"$tmp/partial-$label.err"; then
    echo "terminal rejected degraded $label instead of recording partial attribution" >&2
    cat "$tmp/partial-$label.err" >&2
    exit 1
  fi
  [[ ! -s "$tmp/partial-$label.out" ]]
  python3 - "$tmp/partial-$label.err" "$label" <<'PY'
import json
import pathlib
import sys

lines = pathlib.Path(sys.argv[1]).read_text().splitlines()
records = [json.loads(line.split(" ", 1)[1]) for line in lines if line.startswith("E2E_TERMINAL ")]
assert len(records) == 1, f"{sys.argv[2]}: want exactly one terminal record"
record = records[0]
assert record["attribution"] == "partial", f"{sys.argv[2]}: {record['attribution']}"
assert record["scenario"] == "L01" and record["phase"] == "bootstrap"
assert record["owner"] == "harness" and record["shard"] == "fixture-1"
assert record["replay"] == "make test-e2e E2E_SCENARIO=L01"
degraded = {"line": record["line"], "status": record["status"], "source": record["source"]}
assert sum(1 for value in degraded.values() if value in (0, "")) >= 1, degraded
PY
}

assert_partial_terminal line --status 17 --source test/e2e/linux-smoke.sh --line 0
assert_partial_terminal status --status 0 --source test/e2e/linux-smoke.sh --line 123
assert_partial_terminal source --status 17 --source ../etc/passwd.sh --line 123
if grep -Fq "passwd" "$tmp/partial-source.err"; then
  echo "degraded terminal echoed a non-allowlisted source path" >&2
  exit 1
fi

golden='{"artifact":"L01-attempt-1.json","attempt":1,"binary_sha256":"","class":"unattributed","elapsed_ms":0,"outcome":"begin","owner":"harness","phase":"bootstrap","replay":"make test-e2e E2E_SCENARIO=L01","route_socket":"","scenario_id":"L01","schema":"projmux.e2e-attempt/v1","state_sha256":"","suite":"linux-bootstrap"}'
if [[ "$(head -n 1 "$tmp/golden/summary.jsonl")" != "$golden" ]]; then
  echo "evidence JSONL golden mismatch" >&2
  diff -u <(printf '%s\n' "$golden") <(head -n 1 "$tmp/golden/summary.jsonl") >&2 || true
  exit 1
fi
grep -Fq "id=L01 attempt=1 phase=bootstrap outcome=fail class=environment owner=harness artifact=L01-attempt-1.json replay='make test-e2e E2E_SCENARIO=L01'" "$tmp/fail.out"

# The attempt record carries terminal attribution and diagnostics only when the
# attempt actually produced them, so a begin/pass record keeps its exact bytes.
attributed_record=(python3 "$root/scripts/e2e-evidence.py" record --directory "$tmp/attributed" --scenario-id L06 --suite linux-bootstrap --attempt 1 --phase create-materialize --owner resource-controller)
"${attributed_record[@]}" --class environment --outcome begin --elapsed-ms 0 >/dev/null
"${attributed_record[@]}" --class product-authority-race --outcome fail --elapsed-ms 5 \
  --terminal-json "$(sed 's/^E2E_TERMINAL //' "$tmp/terminal-record.err")" \
  --diagnostic '{"attribution":"l06-lock-exhaustion","racers":[{"index":1,"outcome":"exhausted","pid":7,"status":1}]}' \
  >"$tmp/attributed.out"
python3 "$root/scripts/e2e-evidence.py" validate --terminal "$tmp/attributed/summary.jsonl"
python3 - "$tmp/attributed/summary.jsonl" <<'PY'
import json
import pathlib
import sys

begin, fail = (json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text().splitlines())
assert not {"terminal_line", "terminal_status", "terminal_source", "diagnostic"} & set(begin), begin
assert fail["terminal_line"] == 123 and fail["terminal_status"] == 17
assert fail["terminal_source"] == "test/e2e/linux-smoke.sh"
assert fail["diagnostic"]["racers"][0]["outcome"] == "exhausted"
PY

for bad in '{"line":-1}' '{"status":256}' '{"source":"../escape.sh"}' 'not-json'; do
  if python3 "$root/scripts/e2e-evidence.py" record --directory "$tmp/rejected" \
    --scenario-id L06 --suite linux-bootstrap --attempt 1 --phase create-materialize \
    --owner resource-controller --class environment --outcome fail --elapsed-ms 1 \
    --terminal-json "$bad" >"$tmp/rejected.out" 2>"$tmp/rejected.err"; then
    echo "attempt schema accepted out-of-contract terminal attribution: $bad" >&2
    exit 1
  fi
done

cp "$tmp/golden/summary.jsonl" "$tmp/private.jsonl"
sed -i 's/"phase":"bootstrap"/"phase":"token=github_pat_abcdefghijklmnopqrstuvwxyz"/' "$tmp/private.jsonl"
if python3 "$root/scripts/e2e-evidence.py" validate "$tmp/private.jsonl" >"$tmp/privacy.out" 2>"$tmp/privacy.err"; then
  echo "privacy validator accepted secret-shaped evidence" >&2
  exit 1
fi
grep -Eq 'invalid phase|secret-shaped' "$tmp/privacy.err"

cp "$tmp/golden/summary.jsonl" "$tmp/unterminated.jsonl"
sed -i '$d' "$tmp/unterminated.jsonl"
if python3 "$root/scripts/e2e-evidence.py" validate --terminal "$tmp/unterminated.jsonl" >"$tmp/terminal.out" 2>"$tmp/terminal.err"; then
  echo "terminal validator accepted an attempt without a terminal outcome" >&2
  exit 1
fi
grep -Fq 'unterminated attempts' "$tmp/terminal.err"

assert_result_hash_rejects() {
  local label="$1"
  local directory="$2"
  if python3 "$root/scripts/e2e-evidence.py" result-hash --expected L01 "$directory" >"$tmp/$label.out" 2>"$tmp/$label.err"; then
    echo "result hash accepted $label evidence" >&2
    exit 1
  fi
}

pass_record=(python3 "$root/scripts/e2e-evidence.py" record --directory "$tmp/pass" --scenario-id L01 --suite linux-bootstrap --attempt 1 --phase bootstrap --owner harness)
"${pass_record[@]}" --class environment --outcome begin --elapsed-ms 0 >/dev/null
"${pass_record[@]}" --class environment --outcome pass --elapsed-ms 1 >/dev/null
python3 "$root/scripts/e2e-evidence.py" result-hash --expected L01 "$tmp/pass" >"$tmp/pass.sha256"

terminal_record=(python3 "$root/scripts/e2e-evidence.py" record --directory "$tmp/terminal-only" --scenario-id L01 --suite linux-bootstrap --attempt 1 --phase bootstrap --owner harness)
"${terminal_record[@]}" --class environment --outcome pass --elapsed-ms 1 >/dev/null
assert_result_hash_rejects terminal-only "$tmp/terminal-only"
assert_result_hash_rejects terminal-fail "$tmp/golden"

cancel_record=(python3 "$root/scripts/e2e-evidence.py" record --directory "$tmp/cancel" --scenario-id L01 --suite linux-bootstrap --attempt 1 --phase bootstrap --owner harness)
"${cancel_record[@]}" --class environment --outcome begin --elapsed-ms 0 >/dev/null
"${cancel_record[@]}" --class environment --outcome cancel --elapsed-ms 1 >/dev/null
assert_result_hash_rejects terminal-cancel "$tmp/cancel"

unattributed_record=(python3 "$root/scripts/e2e-evidence.py" record --directory "$tmp/unattributed" --scenario-id L01 --suite linux-bootstrap --attempt 1 --phase bootstrap --owner harness)
"${unattributed_record[@]}" --class environment --outcome begin --elapsed-ms 0 >/dev/null
"${unattributed_record[@]}" --class unattributed --outcome pass --elapsed-ms 1 >/dev/null
assert_result_hash_rejects terminal-unattributed "$tmp/unattributed"

set +e
PROJMUX_E2E_INTENTIONAL_FAILURE=1 \
  PROJMUX_E2E_ARTIFACTS="$tmp/intentional" \
  "$root/test/e2e/evidence-contract.sh" >"$tmp/intentional.out" 2>"$tmp/intentional.err"
intentional_status=$?
set -e
if [[ "$intentional_status" != "1" ]]; then
  echo "intentional failure exited $intentional_status, want 1" >&2
  cat "$tmp/intentional.err" >&2
  exit 1
fi
python3 "$root/scripts/e2e-evidence.py" validate --terminal "$tmp/intentional/summary.jsonl"
# A single observed failure no longer borrows the reproduction claim: nothing has
# been retried, so the class says exactly that.
grep -Fq 'id=L17 attempt=1 phase=exit-reconcile outcome=fail class=unrepeated-failure owner=exit-reconciler' "$tmp/intentional.err"

set +e
PROJMUX_E2E_INTENTIONAL_EXIT=1 \
  PROJMUX_E2E_ARTIFACTS="$tmp/intentional-exit" \
  "$root/test/e2e/evidence-contract.sh" >"$tmp/intentional-exit.out" 2>"$tmp/intentional-exit.err"
intentional_exit_status=$?
set -e
if [[ "$intentional_exit_status" != "23" ]]; then
  echo "intentional explicit exit returned $intentional_exit_status, want 23" >&2
  exit 1
fi
python3 "$root/scripts/e2e-evidence.py" validate --terminal "$tmp/intentional-exit/summary.jsonl"
python3 - "$tmp/intentional-exit.err" "$tmp/intentional-exit/L06-attempt-1.json" <<'PY'
import json
import pathlib
import sys

lines = pathlib.Path(sys.argv[1]).read_text().splitlines()
records = [json.loads(line.split(" ", 1)[1]) for line in lines if line.startswith("E2E_TERMINAL ")]
assert len(records) == 1
record = records[0]
assert record["scenario"] == "L06" and record["status"] == 23
assert record["source"] == "test/e2e/evidence-contract.sh" and record["line"] > 1
assert record["command"] == record["replay"] == "make test-e2e E2E_SCENARIO=L06"
assert record["attribution"] == "complete"

diagnostics = [json.loads(line.split(" ", 1)[1]) for line in lines if line.startswith("E2E_DIAGNOSTIC ")]
# No racer ran in this fixture, so the diagnostic must not name lock exhaustion.
assert len(diagnostics) == 1 and diagnostics[0]["attribution"] == "l06-no-racers-observed"
assert diagnostics[0]["holder"]["acquire"] == "not-started"
assert diagnostics[0]["holder"]["lock_observed"] is False

# The artifact must carry the same attribution and diagnostics the log reported.
# That equality is the whole point: the log dies with the runner, the file does not.
artifact = json.loads(pathlib.Path(sys.argv[2]).read_text())
assert artifact["outcome"] == "fail"
assert artifact["terminal_status"] == record["status"] == 23
assert artifact["terminal_line"] == record["line"]
assert artifact["terminal_source"] == record["source"]
assert artifact["diagnostic"] == diagnostics[0]
assert [racer["index"] for racer in artifact["diagnostic"]["racers"]] == list(range(1, 9))
PY

set +e
PROJMUX_E2E_INTENTIONAL_ZERO_LINE=1 \
  PROJMUX_E2E_ARTIFACTS="$tmp/zero-line" \
  "$root/test/e2e/evidence-contract.sh" >"$tmp/zero-line.out" 2>"$tmp/zero-line.err"
zero_line_status=$?
set -e
if [[ "$zero_line_status" != "42" ]]; then
  echo "zero-line fixture returned $zero_line_status, want 42" >&2
  cat "$tmp/zero-line.err" >&2
  exit 1
fi
python3 "$root/scripts/e2e-evidence.py" validate --terminal "$tmp/zero-line/summary.jsonl"
python3 - "$tmp/zero-line.err" "$tmp/zero-line/L06-attempt-1.json" <<'PY'
import json
import pathlib
import sys

lines = pathlib.Path(sys.argv[1]).read_text().splitlines()
assert not any("invalid terminal status or source line" in line for line in lines), lines
records = [json.loads(line.split(" ", 1)[1]) for line in lines if line.startswith("E2E_TERMINAL ")]
assert len(records) == 1 and records[0]["attribution"] == "partial"
assert records[0]["line"] == 0 and records[0]["scenario"] == "L06"

artifact = json.loads(pathlib.Path(sys.argv[2]).read_text())
assert artifact["outcome"] == "fail" and artifact["terminal_line"] == 0
assert artifact["terminal_status"] == 42
assert artifact["diagnostic"]["attribution"] == "l06-no-racers-observed"
PY

# --- observed failure classification -----------------------------------------
#
# The classification axis is "do we own the source of the nondeterminism", not
# "does a retry pass". Every row below is a statement about attempts that were
# actually observed; none of them may be produced by a constant.

assert_classify() {
  local want="$1"
  shift
  local got
  got="$(python3 "$root/scripts/e2e-evidence.py" classify "$@")"
  if [[ "$got" != "$want" ]]; then
    echo "classify $* -> [$got], want [$want]" >&2
    exit 1
  fi
}

# A failure nobody has retried claims neither reproduction nor flakiness.
assert_classify "class=unrepeated-failure basis=single-observation" --outcome fail
# The same build seen both failing and passing is nondeterministic, whichever
# side of the split the record being written happens to be on.
assert_classify "class=flake basis=observed-nondeterminism" --outcome fail --prior pass
assert_classify "class=flake basis=observed-nondeterminism" --outcome pass --prior fail
assert_classify "class=flake basis=observed-nondeterminism" --outcome fail --prior fail --prior pass
# Reproduction is earned by being seen again, never assumed.
assert_classify "class=deterministic-regression basis=reproduced-failure" --outcome fail --prior fail
# A scenario whose nondeterminism originates outside this repository is kept out
# of `flake` so a quarantine policy never sets a deadline nobody here can meet.
assert_classify "class=unowned-nondeterminism basis=observed-nondeterminism" \
  --outcome fail --owner codex-appserver-adapter --prior pass
assert_classify "class=unowned-nondeterminism basis=observed-nondeterminism" \
  --outcome pass --owner codex-appserver-adapter --prior fail
# A declaration fills the gap only while the attempts are silent. An observed
# split outranks it.
assert_classify "class=product-authority-race basis=declared" \
  --outcome fail --class product-authority-race
assert_classify "class=flake basis=observed-nondeterminism" \
  --outcome fail --class product-authority-race --prior pass
assert_classify "class=environment basis=single-observation" --outcome pass
assert_classify "class=environment basis=declared" --outcome begin --class environment

# Cross-attempt derivation through the real recorder. Each attempt writes into
# its own `attempt-*` directory exactly as scripts/test-e2e-docker.sh lays them
# out, so the recorder learns the earlier result the same way CI evidence does.
binary="$(printf 'c%.0s' {1..64})"
attempt_record() {
  local root_dir="$1"
  local attempt="$2"
  local scenario="$3"
  local owner="$4"
  local outcome="$5"
  shift 5
  python3 "$root/scripts/e2e-evidence.py" record \
    --directory "$root_dir/attempt-local-$attempt-1/linux-fixture-2" \
    --evidence-root "$root_dir" \
    --scenario-id "$scenario" --suite linux-bootstrap --attempt "$attempt" \
    --phase create-materialize --owner "$owner" --outcome "$outcome" \
    --elapsed-ms 1 --binary-sha256 "$binary" "$@"
}

record_class() {
  python3 - "$1" <<'RECORD_CLASS_PY'
import json
import pathlib
import sys

record = json.loads(pathlib.Path(sys.argv[1]).read_text())
print(f"{record['class']} {record.get('class_basis', '-')} {','.join(record.get('prior_outcomes', ['-']))}")
RECORD_CLASS_PY
}

# 1. attempt-1 fails, attempt-2 passes on the same binary -> owned flake.
flake_root="$tmp/retry-flake"
attempt_record "$flake_root" 1 L06 resource-controller begin >/dev/null
attempt_record "$flake_root" 1 L06 resource-controller fail >/dev/null
attempt_record "$flake_root" 2 L06 resource-controller begin >/dev/null
attempt_record "$flake_root" 2 L06 resource-controller pass >"$tmp/retry-flake.out"
if [[ "$(record_class "$flake_root/attempt-local-2-1/linux-fixture-2/L06-attempt-2.json")" != "flake observed-nondeterminism fail" ]]; then
  echo "retried-and-passed attempt was not recorded as an owned flake" >&2
  cat "$flake_root/attempt-local-2-1/linux-fixture-2/L06-attempt-2.json" >&2
  exit 1
fi
grep -Fq "class=flake" "$tmp/retry-flake.out"
# The first attempt keeps the bytes it was written with. Reclassification is a
# report, never a rewrite of evidence somebody already downloaded.
if [[ "$(record_class "$flake_root/attempt-local-1-1/linux-fixture-2/L06-attempt-1.json")" != "unrepeated-failure - -" ]]; then
  echo "recording a later attempt rewrote the earlier attempt evidence" >&2
  exit 1
fi

# 2. the same failure seen twice on the same binary stays a regression.
regression_root="$tmp/retry-regression"
attempt_record "$regression_root" 1 L06 resource-controller begin >/dev/null
attempt_record "$regression_root" 1 L06 resource-controller fail >/dev/null
attempt_record "$regression_root" 2 L06 resource-controller begin >/dev/null
attempt_record "$regression_root" 2 L06 resource-controller fail >"$tmp/retry-regression.out"
if [[ "$(record_class "$regression_root/attempt-local-2-1/linux-fixture-2/L06-attempt-2.json")" != "deterministic-regression reproduced-failure fail" ]]; then
  echo "a failure reproduced on the same binary was not a deterministic regression" >&2
  exit 1
fi
grep -Fq "class=deterministic-regression" "$tmp/retry-regression.out"

# 3. a scenario whose nondeterminism we do not own is kept separate from flake.
unowned_root="$tmp/retry-unowned"
attempt_record "$unowned_root" 1 C01 codex-appserver-adapter begin >/dev/null
attempt_record "$unowned_root" 1 C01 codex-appserver-adapter fail >/dev/null
attempt_record "$unowned_root" 2 C01 codex-appserver-adapter begin >/dev/null
attempt_record "$unowned_root" 2 C01 codex-appserver-adapter pass >"$tmp/retry-unowned.out"
if [[ "$(record_class "$unowned_root/attempt-local-2-1/linux-fixture-2/C01-attempt-2.json")" != "unowned-nondeterminism observed-nondeterminism fail" ]]; then
  echo "unowned nondeterminism was folded into the owned flake value" >&2
  exit 1
fi
grep -Fq "class=unowned-nondeterminism" "$tmp/retry-unowned.out"

# A different build is not evidence about this one.
other_root="$tmp/retry-other-binary"
python3 "$root/scripts/e2e-evidence.py" record \
  --directory "$other_root/attempt-local-1-1/linux-fixture-2" --evidence-root "$other_root" \
  --scenario-id L06 --suite linux-bootstrap --attempt 1 --phase create-materialize \
  --owner resource-controller --outcome fail --elapsed-ms 1 \
  --binary-sha256 "$(printf 'd%.0s' {1..64})" >/dev/null
attempt_record "$other_root" 2 L06 resource-controller begin >/dev/null
attempt_record "$other_root" 2 L06 resource-controller fail >/dev/null
if [[ "$(record_class "$other_root/attempt-local-2-1/linux-fixture-2/L06-attempt-2.json")" != "unrepeated-failure - -" ]]; then
  echo "a different product binary was treated as evidence about this one" >&2
  exit 1
fi

# The report is the operator surface for a class that later attempts contradict.
python3 "$root/scripts/e2e-evidence.py" flake-rate "$flake_root" >"$tmp/flake-report.out"
grep -Fq "E2E_FLAKE scenario=L06 binary=cccccccc attempts=2 pass=1 fail=1 flake_rate=0.500 verdict=flake" "$tmp/flake-report.out"
grep -Fq "E2E_FLAKE_FLIP scenario=L06 binary=cccccccc attempt=attempt-local-1-1 recorded=unrepeated-failure verdict=flake" "$tmp/flake-report.out"
grep -Fq ">> E2E flake report scenarios=1 nondeterministic=1 flips=1" "$tmp/flake-report.out"
python3 "$root/scripts/e2e-evidence.py" flake-rate "$unowned_root" >"$tmp/flake-unowned.out"
grep -Fq "verdict=unowned-nondeterminism" "$tmp/flake-unowned.out"
python3 "$root/scripts/e2e-evidence.py" flake-rate "$regression_root" >"$tmp/flake-regression.out"
grep -Fq "verdict=deterministic-regression" "$tmp/flake-regression.out"
if grep -Fq "E2E_FLAKE_FLIP" "$tmp/flake-regression.out"; then
  echo "the report invented a classification flip for a reproduced failure" >&2
  exit 1
fi

# Derivation is additive only where it happened: an attempt with nothing to
# compare against keeps the exact byte sequence it had before this schema grew.
lone_root="$tmp/retry-lone"
attempt_record "$lone_root" 1 L06 resource-controller begin --class environment >/dev/null
python3 - "$lone_root/attempt-local-1-1/linux-fixture-2/summary.jsonl" <<'LONE_PY'
import json
import pathlib
import sys

record = json.loads(pathlib.Path(sys.argv[1]).read_text().splitlines()[0])
assert not {"class_basis", "prior_outcomes"} & set(record), record
assert record["class"] == "environment", record
LONE_PY

# Out-of-contract derivation trails are rejected the same way every other field is.
assert_rejects_evidence() {
  local label="$1"
  local mutation="$2"
  cp "$flake_root/attempt-local-2-1/linux-fixture-2/summary.jsonl" "$tmp/derived-$label.jsonl"
  sed -i "$mutation" "$tmp/derived-$label.jsonl"
  if python3 "$root/scripts/e2e-evidence.py" validate "$tmp/derived-$label.jsonl" \
    >"$tmp/derived-$label.out" 2>"$tmp/derived-$label.err"; then
    echo "evidence validator accepted $label" >&2
    exit 1
  fi
}
assert_rejects_evidence unknown-basis 's/"class_basis":"observed-nondeterminism"/"class_basis":"guessed"/'
assert_rejects_evidence unsorted-prior 's/"prior_outcomes":\["fail"\]/"prior_outcomes":["pass","fail"]/'
assert_rejects_evidence empty-prior 's/"prior_outcomes":\["fail"\]/"prior_outcomes":[]/'
assert_rejects_evidence orphan-prior 's/"class_basis":"observed-nondeterminism",//'

# Failing e2e artifacts are retained 14 days, so evidence written before the L06
# diagnostic payload was renamed stays downloadable and mixes with new evidence
# in the same report root. Aggregation reads attempt records, never the
# scenario-specific diagnostic payload, so a retained v1 diagnostic must neither
# fail validation nor drop its attempt out of the flake report.
legacy_root="$tmp/retained-v1"
legacy_diagnostic='{"attribution":"l06-lock-exhaustion","holder":{"acquire":"contended","age_ms":37500,"operation":"concurrent-create-pane","pid":0,"release":"held"},"racers":[{"index":1,"outcome":"completed","pid":7,"status":0}],"scenario":"L06","schema":"projmux.e2e-diagnostic/v1"}'
attempt_record "$legacy_root" 1 L06 resource-controller begin >/dev/null
attempt_record "$legacy_root" 1 L06 resource-controller fail --diagnostic "$legacy_diagnostic" >/dev/null
attempt_record "$legacy_root" 2 L06 resource-controller begin >/dev/null
attempt_record "$legacy_root" 2 L06 resource-controller pass >/dev/null
python3 "$root/scripts/e2e-evidence.py" validate --terminal "$legacy_root/attempt-local-1-1/linux-fixture-2/summary.jsonl"
python3 "$root/scripts/e2e-evidence.py" flake-rate "$legacy_root" >"$tmp/flake-legacy.out"
grep -Fq "E2E_FLAKE scenario=L06 binary=cccccccc attempts=2 pass=1 fail=1 flake_rate=0.500 verdict=flake" "$tmp/flake-legacy.out"
grep -Fq "E2E_FLAKE_FLIP scenario=L06 binary=cccccccc attempt=attempt-local-1-1 recorded=unrepeated-failure verdict=flake" "$tmp/flake-legacy.out"

# --- derived L06 diagnostic labels -------------------------------------------
#
# Every field below used to assert something no observation supported.

l06_diagnostic() {
  local -a racers=()
  local index
  for index in {1..8}; do
    racers+=(--racer "$index:$2:$3:$4")
  done
  python3 "$root/scripts/e2e-first-failure.py" l06 \
    --holder-pid 0 --holder-started-ms "$1" --operation concurrent-create-pane \
    --release held "${racers[@]}" 2>&1 >/dev/null | sed 's/^E2E_DIAGNOSTIC //'
}

l06_diagnostic 0 900 0 completed >"$tmp/l06-completed.json"
l06_diagnostic 0 900 1 exhausted >"$tmp/l06-exhausted.json"
l06_diagnostic 0 900 1 other >"$tmp/l06-other.json"
l06_diagnostic "$(( $(date +%s) * 1000 - 1250 ))" 900 0 completed >"$tmp/l06-observed.json"
python3 - "$tmp" <<'L06_PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
def diagnostic(name):
    return json.loads((root / f"l06-{name}.json").read_text())

# The exact shape of the first real capture: eight racers completed with status
# 0 and no lock file was ever read. The old constants called this lock
# exhaustion, called it contended, and gave it a lock age measured from the
# scenario start.
completed = diagnostic("completed")
assert completed["schema"] == "projmux.e2e-diagnostic/v2", completed["schema"]
assert completed["attribution"] == "l06-no-racer-failure", completed["attribution"]
assert completed["holder"]["acquire"] == "acquired", completed["holder"]
assert completed["holder"]["lock_observed"] is False
assert completed["holder"]["lock_age_ms"] == 0

exhausted = diagnostic("exhausted")
assert exhausted["attribution"] == "l06-lock-exhaustion", exhausted["attribution"]
assert exhausted["holder"]["acquire"] == "contended", exhausted["holder"]

other = diagnostic("other")
assert other["attribution"] == "l06-racer-failure", other["attribution"]
assert other["holder"]["acquire"] == "unknown", other["holder"]

# A lock file that was actually stat-ed is the only thing that makes the age
# field mean lock age.
observed = diagnostic("observed")
assert observed["holder"]["lock_observed"] is True
assert observed["holder"]["lock_age_ms"] >= 1250
assert "age_ms" not in observed["holder"], observed["holder"]
L06_PY

echo ">> evidence parser/terminal/golden/privacy/pass-only aggregation/intentional-failure/partial-attribution/preserved-diagnostic/observed-classification/derived-diagnostic tests passed"
