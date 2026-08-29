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
grep -Fq 'id=L17 attempt=1 phase=exit-reconcile outcome=fail class=deterministic-regression owner=exit-reconciler' "$tmp/intentional.err"

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
assert len(diagnostics) == 1 and diagnostics[0]["attribution"] == "l06-lock-exhaustion"

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
assert artifact["diagnostic"]["attribution"] == "l06-lock-exhaustion"
PY

echo ">> evidence parser/terminal/golden/privacy/pass-only aggregation/intentional-failure/partial-attribution/preserved-diagnostic tests passed"
