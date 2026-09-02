#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$root/.github/workflows/ci.yml"
manifest="$root/test/e2e/linux-shards.tsv"
quarantine="$root/test/e2e/quarantine.tsv"
observations="$root/test/e2e/residual-observations.tsv"

job_block() {
	awk -v job="$1" '
    $0 == "  " job ":" { inside = 1; next }
    inside && /^  [^ ]/ { inside = 0 }
    inside { print }
  ' "$workflow"
}

linux_block="$(job_block e2e-linux)"
suite_block="$(job_block e2e-suite)"
test_gate_block="$(job_block test)"
unit_block="$(job_block unit)"

for pair in "e2e-linux:$linux_block" "e2e-suite:$suite_block"; do
	job="${pair%%:*}"
	body="${pair#*:}"
	[[ "$(grep -Fxc '          E2E_WAIT_SCALE: "2"' <<<"$body")" == "1" ]] || {
		echo "$job must apply the measured CI E2E_WAIT_SCALE=2 exactly once" >&2
		exit 1
	}
done

python3 - "$manifest" "$quarantine" "$observations" <<'PY'
import datetime
import pathlib
import re
import sys

manifest = pathlib.Path(sys.argv[1])
quarantine = pathlib.Path(sys.argv[2])
observations = pathlib.Path(sys.argv[3])

linux_ids = []
for line in manifest.read_text(encoding="utf-8").splitlines():
    shard, ids = line.split("\t", 1)
    if not shard or not ids:
        raise SystemExit("empty Linux shard manifest row")
    linux_ids.extend(ids.split())

required = linux_ids + ["C01", "N01"]
expected = [f"L{number:02d}" for number in range(1, 20)] + ["C01", "N01"]
if sorted(required) != sorted(expected) or len(required) != len(set(required)):
    raise SystemExit("required E2E scenario mapping is not exact L01-L19/C01/N01")

rows = []
for number, line in enumerate(quarantine.read_text(encoding="utf-8").splitlines(), 1):
    if not line or line.startswith("#"):
        continue
    fields = line.split("\t")
    if len(fields) != 4:
        raise SystemExit(f"quarantine row {number} must have four tab-separated fields")
    scenario, owner, deadline, observed = fields
    if scenario not in expected:
        raise SystemExit(f"quarantine row {number} names an unknown scenario: {scenario}")
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,47}", owner):
        raise SystemExit(f"quarantine row {number} has an invalid owner")
    try:
        datetime.date.fromisoformat(deadline)
    except ValueError as error:
        raise SystemExit(f"quarantine row {number} has an invalid deadline") from error
    try:
        observed_count = int(observed)
    except ValueError as error:
        raise SystemExit(f"quarantine row {number} has a non-integer observation count") from error
    if observed_count < 3:
        raise SystemExit(f"quarantine row {number} has fewer than three observed flakes")
    rows.append(scenario)

if len(rows) != len(set(rows)):
    raise SystemExit("quarantine contains a duplicate scenario")

observed = {}
for number, line in enumerate(observations.read_text(encoding="utf-8").splitlines(), 1):
    if not line or line.startswith("#"):
        continue
    fields = line.split("\t")
    if len(fields) != 3:
        raise SystemExit(f"observation row {number} must have three tab-separated fields")
    scenario, attempts_text, flakes_text = fields
    if scenario in observed or scenario not in expected:
        raise SystemExit(f"observation row {number} has a duplicate or unknown scenario")
    try:
        attempts = int(attempts_text)
        flakes = int(flakes_text)
    except ValueError as error:
        raise SystemExit(f"observation row {number} has a non-integer count") from error
    if attempts < 1 or flakes < 0 or flakes > attempts:
        raise SystemExit(f"observation row {number} has an invalid denominator/numerator")
    observed[scenario] = (attempts, flakes)

if set(observed) != set(expected):
    raise SystemExit("residual observations do not cover exact L01-L19/C01/N01")
eligible = sorted(scenario for scenario, (_, flakes) in observed.items() if flakes >= 3)
if sorted(rows) != eligible:
    raise SystemExit(
        f"quarantine/evidence mismatch: quarantine={sorted(rows)} eligible={eligible}"
    )
if rows:
    raise SystemExit(
        "Phase 5 has no evidence-backed quarantine entries; add execution/evidence "
        "and non-gating wiring before changing this closed mapping"
    )

print(
    f">> residual policy required={len(required)} quarantine={len(rows)} "
    f"threshold=3 scenario_attempts={sum(attempts for attempts, _ in observed.values())}"
)
print(
    ">> Negative N/A: quarantine is empty, so no quarantined execution route exists; "
    "required mapping remains exact L01-L19/C01/N01"
)
PY

# Go unit tests have no attempt evidence/classification surface. Phase 5 keeps
# that blind spot outside automatic quarantine and leaves the exact required
# child in place instead of inventing observations. The exact E2E matrix,
# compatibility context, and project-wide aggregate are owned once by
# shard-contract.sh rather than restated by this residual evidence policy.
grep -Fqx '    name: Unit Tests' <<<"$unit_block"
grep -Fqx '      - unit' <<<"$test_gate_block"
grep -Fq -- '--required unit' <<<"$test_gate_block"

echo ">> Unit Tests remains required; classification/quarantine is N/A without per-test attempt evidence"
