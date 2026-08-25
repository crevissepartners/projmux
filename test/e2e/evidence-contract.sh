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

echo ">> evidence parser/golden/privacy/intentional-failure tests passed"
