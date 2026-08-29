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

# The manifest is only a schedule contract if the CI job list is derived from
# it. One runner per shard is the guarantee: an aggregate job that hides a
# shard behind its siblings, and a shard with no job at all, both break it.
workflow="$root/.github/workflows/ci.yml"

job_block() {
  awk -v job="$1" '
    $0 == "  " job ":" { inside = 1; next }
    inside && /^  [^ ]/ { inside = 0 }
    inside { print }
  ' "$workflow"
}

matrix_axis() {
  awk -v axis="$1" '
    $0 == "        " axis ":" { inside = 1; next }
    inside && /^          - / { sub(/^          - /, ""); print; next }
    inside { inside = 0 }
  '
}

linux_block="$(job_block e2e-linux)"
suite_block="$(job_block e2e-suite)"
compat_block="$(job_block e2e-tests)"
artifact_names=()
gate_block="$(job_block test)"

# The branch ruleset requires a context named "E2E Tests", and an unreported
# required context never fails - it waits. So this job is load-bearing in an
# unusual way: its absence would not turn a check red, it would deadlock merges.
[[ -n "$compat_block" ]] ||
  { echo "missing the E2E Tests compatibility job the branch ruleset requires" >&2; exit 1; }
grep -Fqx '    name: E2E Tests' <<<"$compat_block" ||
  { echo "the compatibility job must report the exact required context name E2E Tests" >&2; exit 1; }
grep -Fqx '    if: always()' <<<"$compat_block" ||
  { echo "E2E Tests must be if: always(); a skipped job leaves the required context pending" >&2; exit 1; }
for child in e2e-linux e2e-suite; do
  grep -Fqx "      - $child" <<<"$compat_block" ||
    { echo "the E2E Tests job does not need $child" >&2; exit 1; }
  grep -Fq -- "--required $child" <<<"$compat_block" ||
    { echo "the E2E Tests job does not require $child" >&2; exit 1; }
done
for pair in "e2e-linux:$linux_block" "e2e-suite:$suite_block"; do
  job="${pair%%:*}"
  body="${pair#*:}"
  [[ -n "$body" ]] || { echo "missing e2e job $job" >&2; exit 1; }
  grep -Fqx '      fail-fast: false' <<<"$body" ||
    { echo "$job must set fail-fast: false so one failure cannot mask its siblings" >&2; exit 1; }
  grep -Fqx '    runs-on: ubuntu-latest' <<<"$body" ||
    { echo "$job must declare its own runner" >&2; exit 1; }
done

mapfile -t manifest_shards < <(cut -f1 "$manifest")
mapfile -t job_shards < <(matrix_axis shard <<<"$linux_block")
if [[ "${manifest_shards[*]}" != "${job_shards[*]}" ]]; then
  echo "Linux shard manifest/job list mismatch" >&2
  diff -u <(printf '%s\n' "${manifest_shards[@]}") <(printf '%s\n' "${job_shards[@]}") >&2 || true
  exit 1
fi
grep -Fq 'PROJMUX_E2E_LINUX_SHARD: ${{ matrix.shard }}' <<<"$linux_block" ||
  { echo "e2e-linux must select its shard through PROJMUX_E2E_LINUX_SHARD" >&2; exit 1; }

mapfile -t job_suites < <(matrix_axis suite <<<"$suite_block")
if [[ "${job_suites[*]}" != "codex-lifecycle npm-staging" ]]; then
  echo "non-Linux suite job list is not the codex/npm pair: ${job_suites[*]}" >&2
  exit 1
fi
grep -Fq 'PROJMUX_E2E_SUITE: ${{ matrix.suite }}' <<<"$suite_block" ||
  { echo "e2e-suite must select its suite through PROJMUX_E2E_SUITE" >&2; exit 1; }

for child in e2e-linux e2e-suite; do
  grep -Fqx "      - $child" <<<"$gate_block" ||
    { echo "required Test does not need $child" >&2; exit 1; }
  grep -Fq -- "--required $child" <<<"$gate_block" ||
    { echo "required Test gate does not require $child" >&2; exit 1; }
done
if grep -Eq -- '--required e2e( |$|\\)' <<<"$gate_block"; then
  echo "required Test still names the retired aggregate e2e child" >&2
  exit 1
fi

# Evidence preservation has to survive the split: every leg needs the timeout,
# both uploads, and an artifact name carrying its own matrix axis. A name that
# omits the axis makes the legs of one matrix overwrite each other's evidence.
for pair in "e2e-linux:shard:$linux_block" "e2e-suite:suite:$suite_block"; do
  job="${pair%%:*}"
  rest="${pair#*:}"
  axis="${rest%%:*}"
  body="${rest#*:}"
  grep -Fqx '    timeout-minutes: 30' <<<"$body" ||
    { echo "$job must bound a wedged container with timeout-minutes" >&2; exit 1; }
  [[ "$(grep -Fc 'uses: actions/upload-artifact@v4' <<<"$body")" == "2" ]] ||
    { echo "$job must preserve both failing and passing attempt evidence" >&2; exit 1; }
  mapfile -t job_artifacts < <(grep -E '^          name: e2e-evidence-' <<<"$body")
  [[ "${#job_artifacts[@]}" == "2" ]] ||
    { echo "$job must name both evidence artifacts" >&2; exit 1; }
  for artifact in "${job_artifacts[@]}"; do
    grep -Fq "\${{ matrix.$axis }}" <<<"$artifact" ||
      { echo "$job artifact name omits matrix.$axis, so its legs overwrite each other: $artifact" >&2; exit 1; }
  done
  artifact_names+=("${job_artifacts[@]}")
done
mapfile -t unique_names < <(printf '%s\n' "${artifact_names[@]}" | sort -u)
if [[ "${#artifact_names[@]}" != "4" || "${#unique_names[@]}" != "4" ]]; then
  echo "the two e2e jobs share an artifact name" >&2
  printf '%s\n' "${artifact_names[@]}" >&2
  exit 1
fi

echo ">> CI schedules one runner per suite: ${job_shards[*]} ${job_suites[*]}"
echo ">> the required E2E Tests context still reports over the split shards"

# The tag workflow uses the same selectors and manifest, but its aggregate is
# a release gate rather than a branch-ruleset compatibility context. Build
# Release must depend on only that aggregate so every matrix result is reduced
# once, fail-closed, before archive construction starts.
workflow="$root/.github/workflows/release.yml"
release_linux_block="$(job_block e2e-linux)"
release_suite_block="$(job_block e2e-suite)"
release_e2e_block="$(job_block e2e)"
release_build_block="$(job_block release)"

for pair in "e2e-linux:$release_linux_block" "e2e-suite:$release_suite_block"; do
  job="${pair%%:*}"
  body="${pair#*:}"
  [[ -n "$body" ]] || { echo "release workflow is missing e2e job $job" >&2; exit 1; }
  grep -Fqx '      fail-fast: false' <<<"$body" ||
    { echo "release $job must set fail-fast: false" >&2; exit 1; }
  grep -Fqx '    runs-on: ubuntu-latest' <<<"$body" ||
    { echo "release $job must declare its own runner" >&2; exit 1; }
  grep -Fqx '    timeout-minutes: 30' <<<"$body" ||
    { echo "release $job must bound a wedged container" >&2; exit 1; }
done

mapfile -t release_job_shards < <(matrix_axis shard <<<"$release_linux_block")
if [[ "${manifest_shards[*]}" != "${release_job_shards[*]}" ]]; then
  echo "Linux shard manifest/release job list mismatch" >&2
  diff -u <(printf '%s\n' "${manifest_shards[@]}") <(printf '%s\n' "${release_job_shards[@]}") >&2 || true
  exit 1
fi
grep -Fq 'PROJMUX_E2E_LINUX_SHARD: ${{ matrix.shard }}' <<<"$release_linux_block" ||
  { echo "release e2e-linux must select its shard through PROJMUX_E2E_LINUX_SHARD" >&2; exit 1; }

mapfile -t release_job_suites < <(matrix_axis suite <<<"$release_suite_block")
if [[ "${release_job_suites[*]}" != "codex-lifecycle npm-staging" ]]; then
  echo "release non-Linux suite job list is not the codex/npm pair: ${release_job_suites[*]}" >&2
  exit 1
fi
grep -Fq 'PROJMUX_E2E_SUITE: ${{ matrix.suite }}' <<<"$release_suite_block" ||
  { echo "release e2e-suite must select its suite through PROJMUX_E2E_SUITE" >&2; exit 1; }

grep -Fqx '    name: Release E2E Tests' <<<"$release_e2e_block" ||
  { echo "release e2e aggregate must report the exact name Release E2E Tests" >&2; exit 1; }
grep -Fqx '    if: always()' <<<"$release_e2e_block" ||
  { echo "Release E2E Tests must be if: always()" >&2; exit 1; }
for child in e2e-linux e2e-suite; do
  grep -Fqx "      - $child" <<<"$release_e2e_block" ||
    { echo "Release E2E Tests does not need $child" >&2; exit 1; }
  grep -Fq -- "--required $child" <<<"$release_e2e_block" ||
    { echo "Release E2E Tests does not fail closed over $child" >&2; exit 1; }
done

grep -Fqx '      - e2e' <<<"$release_build_block" ||
  { echo "Build Release must depend on the Release E2E Tests aggregate" >&2; exit 1; }
for child in e2e-linux e2e-suite; do
  if grep -Fqx "      - $child" <<<"$release_build_block"; then
    echo "Build Release must not bypass the e2e aggregate with direct $child dependency" >&2
    exit 1
  fi
done

release_artifact_names=()
for pair in "e2e-linux:shard:$release_linux_block" "e2e-suite:suite:$release_suite_block"; do
  job="${pair%%:*}"
  rest="${pair#*:}"
  axis="${rest%%:*}"
  body="${rest#*:}"
  [[ "$(grep -Fc 'uses: actions/upload-artifact@v4' <<<"$body")" == "2" ]] ||
    { echo "release $job must preserve both failing and passing evidence" >&2; exit 1; }
  mapfile -t job_artifacts < <(grep -E '^          name: release-e2e-evidence-' <<<"$body")
  [[ "${#job_artifacts[@]}" == "2" ]] ||
    { echo "release $job must name both evidence artifacts" >&2; exit 1; }
  for artifact in "${job_artifacts[@]}"; do
    grep -Fq "\${{ matrix.$axis }}" <<<"$artifact" ||
      { echo "release $job artifact name omits matrix.$axis: $artifact" >&2; exit 1; }
  done
  release_artifact_names+=("${job_artifacts[@]}")
done
mapfile -t release_unique_names < <(printf '%s\n' "${release_artifact_names[@]}" | sort -u)
if [[ "${#release_artifact_names[@]}" != "4" || "${#release_unique_names[@]}" != "4" ]]; then
  echo "the release e2e jobs share an artifact name" >&2
  exit 1
fi

echo ">> Release schedules one runner per suite behind one fail-closed aggregate"

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
grep -Fq 'suite="${PROJMUX_E2E_SUITE:-}"' "$root/scripts/test-e2e-docker.sh"
grep -Fq 'suite="linux-${PROJMUX_E2E_LINUX_SHARD}"' "$root/scripts/test-e2e-docker.sh"
echo ">> module cache topology is one setup writer; E2E exports the immutable binary path/hash pair to read-only consumers"
