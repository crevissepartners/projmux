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
  [[ "${#sorted_ids[@]}" != "20" ]] || [[ "${sorted_ids[0]}" != "L01" ]] || [[ "${sorted_ids[19]}" != "L20" ]]; then
  echo "Linux shard inventory is not exhaustive and unique L01-L20" >&2
  exit 1
fi

echo ">> four-shard L01-L20 inventory is exhaustive and unique"

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

# The local full-suite capacity guard must not turn the already-isolated CI
# matrix back into one machine-global queue. Hold its capacity slot and prove
# both selector families still enter immediately. The exact matrix shape and
# required aggregate above remain the canonical owners of the CI topology.
(
  set -euo pipefail
  admission="$root/scripts/test-e2e-admission.sh"
  selector_root="$(mktemp -d)"
  holder_pid=""
  selected_pid=""
  # shellcheck disable=SC2317 # Invoked indirectly by the subshell EXIT trap.
  cleanup_selector_contract() {
    touch "$selector_root/release" 2>/dev/null || true
    if [[ -n "$selected_pid" ]]; then
      kill "$selected_pid" 2>/dev/null || true
      wait "$selected_pid" 2>/dev/null || true
    fi
    if [[ -n "$holder_pid" ]]; then
      kill "$holder_pid" 2>/dev/null || true
      wait "$holder_pid" 2>/dev/null || true
    fi
    rm -rf "$selector_root"
  }
  trap cleanup_selector_contract EXIT

  PROJMUX_E2E_ADMISSION_STATE_DIR="$selector_root/state" \
    PROJMUX_E2E_ADMISSION_POLL_SECONDS=0.02 \
    "$admission" bash -c '
      printf "ready\n" >"$1"
      while [[ ! -e "$2" ]]; do sleep 0.02; done
    ' bash "$selector_root/holder-ready" "$selector_root/release" \
    >"$selector_root/holder.log" 2>&1 &
  holder_pid=$!
  for _ in {1..200}; do
    [[ -e "$selector_root/holder-ready" ]] && break
    sleep 0.02
  done
  [[ -e "$selector_root/holder-ready" && -e "$selector_root/state/active" ]]

  PROJMUX_E2E_ADMISSION_STATE_DIR="$selector_root/state" \
    PROJMUX_E2E_LINUX_SHARD=fixture-1 \
    "$admission" bash -c 'printf "selected\n" >"$1"' \
    bash "$selector_root/linux-selected" &
  selected_pid=$!
  for _ in {1..200}; do
    [[ -e "$selector_root/linux-selected" ]] && break
    sleep 0.02
  done
  [[ -e "$selector_root/linux-selected" ]]
  wait "$selected_pid"
  selected_pid=""

  PROJMUX_E2E_ADMISSION_STATE_DIR="$selector_root/state" \
    PROJMUX_E2E_SUITE=codex-lifecycle \
    "$admission" bash -c 'printf "selected\n" >"$1"' \
    bash "$selector_root/suite-selected" &
  selected_pid=$!
  for _ in {1..200}; do
    [[ -e "$selector_root/suite-selected" ]] && break
    sleep 0.02
  done
  [[ -e "$selector_root/suite-selected" ]]
  wait "$selected_pid"
  selected_pid=""

  [[ -e "$selector_root/linux-selected" && -e "$selector_root/suite-selected" ]]
  kill -0 "$holder_pid"
  touch "$selector_root/release"
  wait "$holder_pid"
  holder_pid=""
)
echo ">> CI shard/suite selectors bypass the local full-suite admission boundary"

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
[[ "$(python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" L20)" == \
  "linux-fixture-4:L20" ]]
[[ "$(python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" C01)" == \
  "codex-lifecycle:C01" ]]
[[ "$(python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" N01)" == \
  "npm-staging:N01" ]]
if python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" L21 >/dev/null 2>&1; then
  echo "replay router accepted an unknown scenario" >&2
  exit 1
fi
echo ">> stable-ID replay routing is closed and fail-closed"

dialogue_canary="$root/scripts/agent-dialogue-live-canary.sh"
dialogue_matrix="$root/scripts/agent-dialogue-version-stress.sh"
grep -Fq 'PMX_DIALOGUE_LIVE_CANARY:-' "$dialogue_canary"
grep -Fq 'tools")==[] and matches[0].get("mcp_servers")==[] and matches[0].get("plugins")==[]' "$dialogue_canary"
grep -Fq 'preInboundToolUse":0' "$dialogue_canary"
grep -Fq 'cleanup was not registered before provider launch' "$dialogue_canary"
grep -Fq 'projmux-dialogue-canary-owned-v3' "$dialogue_canary"
grep -Fq 'credentialEnvPresent":False' "$dialogue_canary"
grep -Fq '"credentialSource":{"present":True' "$dialogue_canary"
if grep -Fq '"credentialSource":{"before"' "$dialogue_canary"; then
  echo "live dialogue canary external receipt exposes credential hashes" >&2
  exit 1
fi
grep -Fq 'exactHelperBirthAbsent' "$dialogue_canary"
grep -Fq 'exactTmuxBirthAbsent' "$dialogue_canary"
grep -Fq 'exactClaimBirthAbsent' "$dialogue_canary"
grep -Fq 'activationLeaseDirAbsent' "$dialogue_canary"
grep -Fq 'unknown current-version init field' "$dialogue_canary"
grep -Fq 'collect-claude-public-jsonl' "$dialogue_canary"
gate_line="$(grep -n 'traffic-gate.json").write_text' "$dialogue_canary" | cut -d: -f1)"
qualify_line="$(grep -n 'agent message qualify' "$dialogue_canary" | tail -1 | cut -d: -f1)"
send_line="$(grep -n 'agent message send' "$dialogue_canary" | tail -1 | cut -d: -f1)"
[[ "$gate_line" -lt "$qualify_line" && "$qualify_line" -lt "$send_line" ]] ||
  { echo "live dialogue canary can push before its isolation gate or ordinary send before qualification" >&2; exit 1; }
if grep -Fq -- '--safe-mode' "$dialogue_canary"; then
  echo "live dialogue canary incorrectly treats safe mode as hook evidence" >&2
  exit 1
fi
(
  set -euo pipefail
  guard_root="$(mktemp -d)"
  trap 'rm -rf -- "$guard_root"' EXIT
  install -m 0600 /dev/null "$guard_root/credential.json"
  ln -s "$HOME" "$guard_root/home-link"
  if PMX_DIALOGUE_CANARY_ROOT="$guard_root/home-link/canary" \
    PMX_DIALOGUE_CANARY_RECEIPT="$guard_root/symlink-root.receipt.json" \
    PMX_DIALOGUE_PROJMUX_BIN=/bin/true PMX_DIALOGUE_REAL_CLAUDE_BIN=/bin/true \
    PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE="$guard_root/credential.json" \
    "$dialogue_canary" prepare >/dev/null 2>&1; then
    echo "live dialogue canary accepted a root through a symlinked parent" >&2
    exit 1
  fi
  ln -s "$guard_root/receipt-target" "$guard_root/receipt-link"
  if PMX_DIALOGUE_CANARY_ROOT="$guard_root/receipt-root" \
    PMX_DIALOGUE_CANARY_RECEIPT="$guard_root/receipt-link" \
    PMX_DIALOGUE_PROJMUX_BIN=/bin/true PMX_DIALOGUE_REAL_CLAUDE_BIN=/bin/true \
    PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE="$guard_root/credential.json" \
    "$dialogue_canary" prepare >/dev/null 2>&1; then
    echo "live dialogue canary accepted a symlink receipt" >&2
    exit 1
  fi
  prepared_root="$guard_root/canary path 'quoted"
  prepared_receipt="$guard_root/prepared.receipt.json"
  PMX_DIALOGUE_CANARY_ROOT="$prepared_root" PMX_DIALOGUE_CANARY_RECEIPT="$prepared_receipt" \
    PMX_DIALOGUE_PROJMUX_BIN=/bin/true PMX_DIALOGUE_REAL_CLAUDE_BIN=/bin/true \
    PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE="$guard_root/credential.json" \
    "$dialogue_canary" prepare >/dev/null
  mkdir -p "$prepared_root/xdg-state/projmux/metadata"
  install -m 0600 /dev/null "$prepared_root/xdg-state/projmux/metadata/registry.json"
  python3 - "$prepared_root/tmux/guard.sock" <<'PY'
import socket,sys
value=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM); value.bind(sys.argv[1]); value.close()
PY
  python3 - "$prepared_root" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
(root/"canary-input.json").write_text(json.dumps({"binary":"/bin/true",
 "registryPath":str(root/"xdg-state/projmux/metadata/registry.json"),
 "tmuxSocketPath":str(root/"tmux/guard.sock"),"tmuxSocketName":"guard","projectUID":"project-guard"})+"\n")
PY
  if PMX_DIALOGUE_CANARY_ROOT="$prepared_root" PMX_DIALOGUE_CANARY_RECEIPT="$guard_root/override.receipt.json" \
    PMX_DIALOGUE_LIVE_CANARY=1 "$dialogue_canary" run >/dev/null 2>&1; then
    echo "live dialogue canary accepted a receipt override" >&2
    exit 1
  fi
  [[ ! -e "$prepared_root" && ! -e "$guard_root/override.receipt.json" ]]
)
grep -Fq 'PMX_DIALOGUE_VERSION_STRESS:-' "$dialogue_matrix"
grep -Fq 'label<TAB>/absolute/executable-runner' "$dialogue_matrix"
echo ">> real dialogue canary and version matrix remain opt-in and traffic-gated"

# Replay result acceptance is exact: even valid terminal evidence from a
# neighbouring contract in the routed shard must be rejected as an extra.
replay_root="$(mktemp -d)"
trap 'if declare -F cleanup_scheduler_process >/dev/null; then cleanup_scheduler_process; fi; rm -rf "$replay_root"' EXIT
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
grep -Fqx $'\tscripts/test-e2e-admission.sh scripts/test-e2e-docker.sh' "$root/Makefile"
echo ">> module cache topology is one setup writer; E2E exports the immutable binary path/hash pair to read-only consumers"

# Run the production entrypoint against a deterministic fake Docker runner. A
# shard announces start before blocking on its own release FIFO, so the test
# observes causal overlap directly; elapsed time is used only as a stuck-test
# safety bound and never as schedule evidence.
e2e_runner="$root/scripts/test-e2e-docker.sh"
grep -Fq 'linux_mode="${PROJMUX_E2E_LINUX_MODE:-serial}"' "$e2e_runner"
grep -Fq 'if [[ "$linux_mode" == "serial" || "${#linux_shards[@]}" -lt 2 ]]; then' "$e2e_runner"
grep -Fq 'elif [[ "$linux_mode" == "parallel" ]]; then' "$e2e_runner"
grep -Fq 'invalid PROJMUX_E2E_LINUX_MODE=$linux_mode; expected serial or parallel' "$e2e_runner"

scheduler_fixture_root="$replay_root/scheduler-fixture"
mkdir -p "$scheduler_fixture_root/scripts" "$scheduler_fixture_root/test/e2e"
cp "$e2e_runner" "$scheduler_fixture_root/scripts/test-e2e-docker.sh"
cp "$root/scripts/e2e-evidence.py" "$scheduler_fixture_root/scripts/e2e-evidence.py"
cp "$manifest" "$scheduler_fixture_root/test/e2e/linux-shards.tsv"

cat >"$scheduler_fixture_root/scripts/test-docker-run.sh" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
state="${PROJMUX_E2E_SCHEDULER_FIXTURE_STATE:?}"

if [[ "${1:-}" == "--build-binary" ]]; then
  binary_dir="${2:?}"
  mkdir -p "$binary_dir"
  printf 'scheduler-contract-immutable-binary\n' >"$binary_dir/projmux"
  printf '1\n' >"$binary_dir/build-count"
  printf 'build\n' >>"$state/builds"
  exit 0
fi

test_script="${1:?}"
artifact_dir="${PROJMUX_E2E_ARTIFACTS:?}"
binary="${PROJMUX_TEST_PREBUILT_BIN:?}"
binary_sha="${PROJMUX_TEST_PREBUILT_SHA256:?}"
[[ "$(sha256sum "$binary" | awk '{print $1}')" == "$binary_sha" ]]

suite=""
ids=""
shard=""
case "$test_script" in
  test/e2e/linux-smoke.sh)
    suite="linux"
    shard="${PROJMUX_E2E_LINUX_SHARD:?}"
    ids="$(awk -F'\t' -v want="$shard" '
      $1 == want { print $2; found = 1 }
      END { exit found ? 0 : 1 }
    ' "$root/test/e2e/linux-shards.tsv")"
    ;;
  test/e2e/codex-lifecycle.sh)
    suite="codex"
    ids="C01"
    ;;
  test/e2e/npm-staging-path.sh)
    suite="npm"
    ids="N01"
    ;;
  *)
    echo "unexpected scheduler fixture script: $test_script" >&2
    exit 2
    ;;
esac

if [[ -n "$shard" ]]; then
  : >"$state/active/$shard"
  printf 'start:%s\n' "$shard" >"$state/events"
  IFS= read -r release <"$state/release-$shard"
  [[ "$release" == "$shard" ]]
fi

for scenario_id in $ids; do
  python3 "$root/scripts/e2e-evidence.py" record \
    --directory "$artifact_dir" --scenario-id "$scenario_id" --suite "$suite" \
    --attempt 1 --phase scheduler --owner shard-contract --class environment \
    --outcome begin --elapsed-ms 0 --binary-sha256 "$binary_sha" >/dev/null
  python3 "$root/scripts/e2e-evidence.py" record \
    --directory "$artifact_dir" --scenario-id "$scenario_id" --suite "$suite" \
    --attempt 1 --phase scheduler --owner shard-contract --class environment \
    --outcome pass --elapsed-ms 1 --binary-sha256 "$binary_sha" >/dev/null
done

if [[ -n "$shard" ]]; then
  rm "$state/active/$shard"
  printf 'finish:%s\n' "$shard" >"$state/events"
fi
FIXTURE
chmod +x "$scheduler_fixture_root/scripts/test-e2e-docker.sh" \
  "$scheduler_fixture_root/scripts/test-docker-run.sh" \
  "$scheduler_fixture_root/scripts/e2e-evidence.py"
git -C "$scheduler_fixture_root" init -q

active_scheduler_pid=""
cleanup_scheduler_process() {
  if [[ -z "$active_scheduler_pid" ]] || ! kill -0 "$active_scheduler_pid" 2>/dev/null; then
    return
  fi
  for shard in "${manifest_shards[@]}"; do
    if [[ -e "$scheduler_state/active/$shard" ]]; then
      printf '%s\n' "$shard" >"$scheduler_state/release-$shard" &
    fi
  done
  kill "$active_scheduler_pid" 2>/dev/null || true
  wait "$active_scheduler_pid" 2>/dev/null || true
  active_scheduler_pid=""
}

read_scheduler_event() {
  if ! IFS= read -r -t 20 -u "$scheduler_event_fd" scheduler_event; then
    cat "$scheduler_output" >&2
    echo "scheduler fixture did not reach its next causal barrier" >&2
    cleanup_scheduler_process
    return 1
  fi
}

assert_active_shards() {
  local expected_active="$1"
  mapfile -t actual_active < <(
    find "$scheduler_state/active" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' | sort
  )
  if [[ "${actual_active[*]}" != "$expected_active" ]]; then
    echo "scheduler active shards: got '${actual_active[*]}' want '$expected_active'" >&2
    cleanup_scheduler_process
    return 1
  fi
}

run_scheduler_fixture() {
  local profile="$1"
  scheduler_state="$scheduler_fixture_root/state-$profile"
  scheduler_output="$scheduler_fixture_root/$profile.log"
  local artifacts="$scheduler_fixture_root/artifacts-$profile"
  local cache="$scheduler_fixture_root/cache-$profile"
  mkdir -p "$scheduler_state/active"
  mkfifo "$scheduler_state/events"
  for shard in "${manifest_shards[@]}"; do
    mkfifo "$scheduler_state/release-$shard"
  done
  exec {scheduler_event_fd}<>"$scheduler_state/events"

  if [[ "$profile" == "parallel" ]]; then
    env -u E2E_SCENARIO -u PROJMUX_E2E_LINUX_SHARD -u PROJMUX_E2E_SUITE \
      -u PROJMUX_E2E_SHARD_ORDER -u PROJMUX_E2E_PREPARE_ONLY \
      PROJMUX_E2E_SCHEDULER_FIXTURE_STATE="$scheduler_state" \
      PROJMUX_E2E_ARTIFACTS="$artifacts" \
      PROJMUX_E2E_BUILD_CACHE="$cache" \
      PROJMUX_E2E_LINUX_MODE=parallel \
      "$scheduler_fixture_root/scripts/test-e2e-docker.sh" \
      >"$scheduler_output" 2>&1 &
  else
    env -u E2E_SCENARIO -u PROJMUX_E2E_LINUX_SHARD -u PROJMUX_E2E_SUITE \
      -u PROJMUX_E2E_SHARD_ORDER -u PROJMUX_E2E_PREPARE_ONLY \
      -u PROJMUX_E2E_LINUX_MODE \
      PROJMUX_E2E_SCHEDULER_FIXTURE_STATE="$scheduler_state" \
      PROJMUX_E2E_ARTIFACTS="$artifacts" \
      PROJMUX_E2E_BUILD_CACHE="$cache" \
      "$scheduler_fixture_root/scripts/test-e2e-docker.sh" \
      >"$scheduler_output" 2>&1 &
  fi
  active_scheduler_pid=$!

  if [[ "$profile" == "serial" ]]; then
    for shard in "${manifest_shards[@]}"; do
      read_scheduler_event
      [[ "$scheduler_event" == "start:$shard" ]] || {
        echo "default schedule started out of canonical order: $scheduler_event" >&2
        cleanup_scheduler_process
        return 1
      }
      assert_active_shards "$shard"
      printf '%s\n' "$shard" >"$scheduler_state/release-$shard"
      read_scheduler_event
      [[ "$scheduler_event" == "finish:$shard" ]] || {
        echo "default schedule overlapped before $shard finished: $scheduler_event" >&2
        cleanup_scheduler_process
        return 1
      }
      assert_active_shards ""
    done
    printf '1\n' >"$scheduler_state/max-overlap"
  else
    parallel_starts=()
    for _ in "${manifest_shards[@]}"; do
      read_scheduler_event
      [[ "$scheduler_event" == start:* ]] || {
        echo "parallel schedule finished before its four-way barrier: $scheduler_event" >&2
        cleanup_scheduler_process
        return 1
      }
      parallel_starts+=("${scheduler_event#start:}")
    done
    mapfile -t parallel_starts < <(printf '%s\n' "${parallel_starts[@]}" | sort)
    mapfile -t sorted_manifest_shards < <(printf '%s\n' "${manifest_shards[@]}" | sort)
    [[ "${parallel_starts[*]}" == "${sorted_manifest_shards[*]}" ]] || {
      echo "parallel start barrier omitted or duplicated a shard" >&2
      cleanup_scheduler_process
      return 1
    }
    assert_active_shards "${sorted_manifest_shards[*]}"
    printf '4\n' >"$scheduler_state/max-overlap"
    for shard in "${manifest_shards[@]}"; do
      printf '%s\n' "$shard" >"$scheduler_state/release-$shard"
    done
    parallel_finishes=()
    for _ in "${manifest_shards[@]}"; do
      read_scheduler_event
      [[ "$scheduler_event" == finish:* ]] || {
        echo "parallel schedule emitted an invalid finish event: $scheduler_event" >&2
        cleanup_scheduler_process
        return 1
      }
      parallel_finishes+=("${scheduler_event#finish:}")
    done
    mapfile -t parallel_finishes < <(printf '%s\n' "${parallel_finishes[@]}" | sort)
    [[ "${parallel_finishes[*]}" == "${sorted_manifest_shards[*]}" ]] || {
      echo "parallel finish barrier omitted or duplicated a shard" >&2
      cleanup_scheduler_process
      return 1
    }
    assert_active_shards ""
  fi

  if ! wait "$active_scheduler_pid"; then
    cat "$scheduler_output" >&2
    active_scheduler_pid=""
    return 1
  fi
  active_scheduler_pid=""
  exec {scheduler_event_fd}>&-
}

run_scheduler_fixture serial
run_scheduler_fixture parallel

serial_state="$scheduler_fixture_root/state-serial"
parallel_state="$scheduler_fixture_root/state-parallel"
serial_artifacts="$scheduler_fixture_root/artifacts-serial"
parallel_artifacts="$scheduler_fixture_root/artifacts-parallel"

[[ "$(cat "$serial_state/max-overlap")" == "1" ]]
[[ "$(cat "$parallel_state/max-overlap")" == "4" ]]
for profile in serial parallel; do
  state="$scheduler_fixture_root/state-$profile"
  artifacts="$scheduler_fixture_root/artifacts-$profile"
  [[ "$(wc -l <"$state/builds")" == "1" ]] || {
    echo "$profile scheduler fixture did not build exactly once" >&2
    exit 1
  }
  recorded_sha="$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["binary_sha256"])
' "$artifacts/build.json")"
  [[ "$recorded_sha" == "$(sha256sum "$artifacts/binary/projmux" | awk '{print $1}')" ]] || {
    echo "$profile scheduler fixture mutated its product binary" >&2
    exit 1
  }
done

cmp "$serial_artifacts/binary/projmux" "$parallel_artifacts/binary/projmux"
cmp "$serial_artifacts/result.sha256" "$parallel_artifacts/result.sha256"
python3 - "$serial_artifacts" "$parallel_artifacts" <<'PY'
import collections
import json
import pathlib
import sys

expected = {f"L{index:02d}" for index in range(1, 21)} | {"C01", "N01"}
for root_text in sys.argv[1:]:
    terminal = collections.Counter()
    for summary in pathlib.Path(root_text).rglob("summary.jsonl"):
        for line in summary.read_text(encoding="utf-8").splitlines():
            record = json.loads(line)
            if record["outcome"] == "pass":
                terminal[record["scenario_id"]] += 1
    if set(terminal) != expected or set(terminal.values()) != {1}:
        raise SystemExit(f"scheduler inventory mismatch: {terminal}")
PY

echo ">> selectorless local default is canonical serial with maximum overlap 1"
echo ">> explicit parallel mode crosses the four-shard barrier with maximum overlap 4"
echo ">> local profiles share build-count 1, immutable binary, exact L01-L20/C01/N01 inventory, and result hash"
