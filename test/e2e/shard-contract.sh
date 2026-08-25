#!/usr/bin/env bash
# shellcheck disable=SC2016 # This contract matches literal production shell fragments.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
manifest="$root/test/e2e/linux-shards.tsv"

expected_ids=()
shard_count=0
while IFS=$'\t' read -r shard ids; do
  [[ -n "$shard" && -n "$ids" ]] || { echo "empty shard manifest row" >&2; exit 1; }
  shard_count=$((shard_count + 1))
  read -r -a row_ids <<<"$ids"
  ((${#row_ids[@]} > 0)) || { echo "empty shard: $shard" >&2; exit 1; }
  expected_ids+=("${row_ids[@]}")
done <"$manifest"

[[ "$shard_count" == "4" ]] || { echo "expected exactly four Linux shards" >&2; exit 1; }
mapfile -t sorted_ids < <(printf '%s\n' "${expected_ids[@]}" | sort)
mapfile -t source_ids < <(sed -n 's/.*smoke_contract_begin \(L[0-9][0-9]\).*/\1/p' "$root/test/e2e/linux-smoke.sh" | sort)
if [[ "${sorted_ids[*]}" != "${source_ids[*]}" ]]; then
  echo "Linux shard manifest/source inventory mismatch" >&2
  diff -u <(printf '%s\n' "${sorted_ids[@]}") <(printf '%s\n' "${source_ids[@]}") >&2 || true
  exit 1
fi
if [[ "$(printf '%s\n' "${sorted_ids[@]}" | uniq -d | wc -l)" != "0" ]] || \
  [[ "${#sorted_ids[@]}" != "19" ]] || [[ "${sorted_ids[0]}" != "L01" ]] || [[ "${sorted_ids[18]}" != "L19" ]]; then
  echo "Linux shard inventory is not exhaustive and unique L01-L19" >&2
  exit 1
fi

echo ">> four-shard L01-L19 inventory is exhaustive and unique"

[[ "$(python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" L17)" == \
  "linux-fixture-4:L17" ]]
[[ "$(python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" C01)" == \
  "codex-lifecycle:C01" ]]
[[ "$(python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" N01)" == \
  "npm-staging:N01" ]]
if python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" L20 >/dev/null 2>&1; then
  echo "replay router accepted an unknown scenario" >&2
  exit 1
fi
echo ">> stable-ID replay routing is closed and fail-closed"

# Replay result acceptance is exact: even valid terminal evidence from a
# neighbouring contract in the routed shard must be rejected as an extra.
replay_root="$(mktemp -d)"
trap 'rm -rf "$replay_root"' EXIT
mkdir -p "$replay_root/one"
for scenario_id in L17 L18; do
  python3 "$root/scripts/e2e-evidence.py" record \
    --directory "$replay_root/one" --scenario-id "$scenario_id" --suite linux \
    --attempt 1 --phase replay --owner shard-contract --class environment \
    --outcome begin --elapsed-ms 0 --binary-sha256 "$(printf 'a%.0s' {1..64})" >/dev/null
  python3 "$root/scripts/e2e-evidence.py" record \
    --directory "$replay_root/one" --scenario-id "$scenario_id" --suite linux \
    --attempt 1 --phase replay --owner shard-contract --class environment \
    --outcome pass --elapsed-ms 1 --binary-sha256 "$(printf 'a%.0s' {1..64})" >/dev/null
done
if python3 "$root/scripts/e2e-evidence.py" result-hash --expected L17 "$replay_root" >/dev/null 2>&1; then
  echo "exact replay result hash accepted neighbouring terminal evidence" >&2
  exit 1
fi
echo ">> exact replay result inventory rejects shard neighbours"

docker_runner="$root/scripts/test-docker-run.sh"
[[ "$(grep -Fc -- '-v "$modcache:/gomodcache:rw"' "$docker_runner")" == "1" ]]
[[ "$(grep -Fc -- '-v "$modcache:/gomodcache:ro"' "$docker_runner")" == "2" ]]
grep -Fq 'export PROJMUX_TEST_SKIP_PREFETCH=1' "$root/scripts/test-e2e-docker.sh"
grep -Fq 'export PROJMUX_TEST_PREBUILT_BIN="$binary_dir/projmux"' "$root/scripts/test-e2e-docker.sh"
grep -Fq 'export PROJMUX_TEST_PREBUILT_SHA256="$binary_sha"' "$root/scripts/test-e2e-docker.sh"
grep -Fq -- '-e E2E_SCENARIO="${E2E_SCENARIO:-}"' "$docker_runner"
echo ">> module cache topology is one setup writer; E2E exports the immutable binary path/hash pair to read-only consumers"
