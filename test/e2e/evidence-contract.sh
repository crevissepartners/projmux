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

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

record=(python3 "$root/scripts/e2e-evidence.py" record --directory "$tmp/golden" --scenario-id L01 --suite linux-bootstrap --attempt 1 --phase bootstrap --owner harness)
"${record[@]}" --class unattributed --outcome begin --elapsed-ms 0 >"$tmp/begin.out"
"${record[@]}" --class environment --outcome fail --elapsed-ms 17 >"$tmp/fail.out"
python3 "$root/scripts/e2e-evidence.py" validate --terminal "$tmp/golden/summary.jsonl"

golden='{"artifact":"L01-attempt-1.json","attempt":1,"binary_sha256":"","class":"unattributed","elapsed_ms":0,"outcome":"begin","owner":"harness","phase":"bootstrap","replay":"make test-e2e E2E_SCENARIO=L01","route_socket":"","scenario_id":"L01","schema":"projmux.e2e-attempt/v1","state_sha256":"","suite":"linux-bootstrap"}'
if [[ "$(head -n 1 "$tmp/golden/summary.jsonl")" != "$golden" ]]; then
  echo "evidence JSONL golden mismatch" >&2
  diff -u <(printf '%s\n' "$golden") <(head -n 1 "$tmp/golden/summary.jsonl") >&2 || true
  exit 1
fi
grep -Fq "id=L01 attempt=1 phase=bootstrap outcome=fail class=environment owner=harness artifact=L01-attempt-1.json replay='make test-e2e E2E_SCENARIO=L01'" "$tmp/fail.out"

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

echo ">> evidence parser/golden/privacy/pass-only aggregation/intentional-failure tests passed"
