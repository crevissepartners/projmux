#!/usr/bin/env bash
# shellcheck disable=SC2016 # bash -c fixtures intentionally expand in the child.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
admission="$root/scripts/test-e2e-admission.sh"
contract_root="$(mktemp -d)"
worker_pids=()

cleanup() {
  touch "$contract_root/default-release" "$contract_root/bypass-owner-release" \
    "$contract_root/bypass-release" 2>/dev/null || true
  for worker_pid in "${worker_pids[@]}"; do
    kill "$worker_pid" 2>/dev/null || true
    wait "$worker_pid" 2>/dev/null || true
  done
  rm -rf "$contract_root"
}
trap cleanup EXIT

wait_for_file() {
  local path="$1"
  local attempt
  for ((attempt = 0; attempt < 200; attempt++)); do
    [[ -e "$path" ]] && return 0
    sleep 0.02
  done
  echo "admission contract timed out waiting for $path" >&2
  return 1
}

wait_for_text() {
  local text="$1"
  local path="$2"
  local attempt
  for ((attempt = 0; attempt < 200; attempt++)); do
    grep -Fq "$text" "$path" 2>/dev/null && return 0
    sleep 0.02
  done
  echo "admission contract timed out waiting for '$text' in $path" >&2
  return 1
}

assert_state_files_cleaned() {
  local state_dir="$1"
  local survivor
  survivor="$(find "$state_dir" -type f -print -quit)"
  if [[ -n "$survivor" ]]; then
    echo "admission state file survived cleanup: $survivor" >&2
    return 1
  fi
}

state_dir="$contract_root/default-state"
PROJMUX_E2E_ADMISSION_STATE_DIR="$state_dir" \
  PROJMUX_E2E_ADMISSION_POLL_SECONDS=0.02 \
  "$admission" bash -c '
    printf "started\n" >"$1"
    while [[ ! -e "$2" ]]; do sleep 0.02; done
    printf "finished\n" >"$3"
  ' bash "$contract_root/default-first-start" "$contract_root/default-release" \
  "$contract_root/default-first-finish" >"$contract_root/default-first.log" 2>&1 &
first_pid=$!
worker_pids+=("$first_pid")
wait_for_file "$contract_root/default-first-start"

PROJMUX_E2E_ADMISSION_STATE_DIR="$state_dir" \
  PROJMUX_E2E_ADMISSION_POLL_SECONDS=0.02 \
  "$admission" bash -c '
    printf "started\n" >"$1"
    printf "finished\n" >"$2"
  ' bash "$contract_root/default-second-start" "$contract_root/default-second-finish" \
  >"$contract_root/default-second.log" 2>&1 &
second_pid=$!
worker_pids+=("$second_pid")

wait_for_text 'state=waiting reason=local-full-suite-capacity capacity=1' \
  "$contract_root/default-second.log"
grep -Fq "owner_pid=$first_pid" "$contract_root/default-second.log"
grep -Fq "owner_cwd=$root" "$contract_root/default-second.log"
grep -Fq "active_state=$state_dir/active" "$contract_root/default-second.log"
[[ -f "$state_dir/active" ]]
grep -Fqx "owner_pid=$first_pid" "$state_dir/active"
grep -Fqx 'reason=local-full-suite-capacity' "$state_dir/active"
grep -Fqx 'capacity=1' "$state_dir/active"
if [[ -e "$contract_root/default-second-start" ]]; then
  echo "default second full suite crossed the capacity-one admission boundary" >&2
  exit 1
fi

E2E_SCENARIO=L06 \
  PROJMUX_E2E_ADMISSION_STATE_DIR="$state_dir" \
  "$admission" bash -c 'printf "selected\n" >"$1"' \
  bash "$contract_root/replay-selected"
[[ -e "$contract_root/replay-selected" && -f "$state_dir/active" ]]
kill -0 "$first_pid"

touch "$contract_root/default-release"
wait "$first_pid"
wait "$second_pid"
worker_pids=()
wait_for_file "$contract_root/default-second-finish"
[[ ! -e "$state_dir/active" ]]
assert_state_files_cleaned "$state_dir"
echo ">> default full-suite admission waits with an exact active owner/reason and both callers finish"
echo ">> a single-scenario replay enters while the local full-suite slot is occupied"

bypass_state_dir="$contract_root/bypass-state"
PROJMUX_E2E_ADMISSION_STATE_DIR="$bypass_state_dir" \
  PROJMUX_E2E_ADMISSION_POLL_SECONDS=0.02 \
  "$admission" bash -c '
    printf "started\n" >"$1"
    while [[ ! -e "$2" ]]; do sleep 0.02; done
  ' bash "$contract_root/bypass-owner-start" "$contract_root/bypass-owner-release" \
  >"$contract_root/bypass-owner.log" 2>&1 &
bypass_owner_pid=$!
worker_pids+=("$bypass_owner_pid")
wait_for_file "$contract_root/bypass-owner-start"

PROJMUX_E2E_ADMISSION_STATE_DIR="$bypass_state_dir" \
  PROJMUX_E2E_ADMISSION_POLL_SECONDS=0.02 \
  PROJMUX_E2E_ADMISSION_BYPASS=1 \
  "$admission" bash -c '
    printf "started\n" >"$1"
    while [[ ! -e "$2" ]]; do sleep 0.02; done
  ' bash "$contract_root/bypass-start" "$contract_root/bypass-release" \
  >"$contract_root/bypass.log" 2>&1 &
bypass_pid=$!
worker_pids+=("$bypass_pid")
wait_for_file "$contract_root/bypass-start"

[[ -e "$contract_root/bypass-owner-start" && -f "$bypass_state_dir/active" ]]
kill -0 "$bypass_owner_pid"
kill -0 "$bypass_pid"
grep -Fq 'state=bypass reason=explicit-stress' "$contract_root/bypass.log"
touch "$contract_root/bypass-release" "$contract_root/bypass-owner-release"
wait "$bypass_pid"
wait "$bypass_owner_pid"
worker_pids=()
[[ ! -e "$bypass_state_dir/active" ]]
assert_state_files_cleaned "$bypass_state_dir"
echo ">> explicit stress bypass overlaps an admitted full-suite owner"

set +e
PROJMUX_E2E_ADMISSION_STATE_DIR="$contract_root/failure-state" \
  "$admission" bash -c 'exit 17' >"$contract_root/failure.log" 2>&1
failure_status=$?
set -e
[[ "$failure_status" == "17" ]]
[[ ! -e "$contract_root/failure-state/active" ]]
assert_state_files_cleaned "$contract_root/failure-state"
echo ">> terminal child status is preserved and admission state is released"

stale_state_dir="$contract_root/stale-state"
mkdir -p "$stale_state_dir"
stale_record="$stale_state_dir/stale-owner"
{
  printf 'owner_pid=99999999\n'
  printf 'owner_started=2000-01-01T00:00:00Z\n'
  printf 'owner_cwd=/stale-owner\n'
  printf 'owner_record=%s\n' "$stale_record"
  printf 'reason=local-full-suite-capacity\n'
  printf 'capacity=1\n'
} >"$stale_record"
ln "$stale_record" "$stale_state_dir/active"
PROJMUX_E2E_ADMISSION_STATE_DIR="$stale_state_dir" \
  PROJMUX_E2E_ADMISSION_POLL_SECONDS=0.02 \
  "$admission" bash -c 'printf "recovered\n" >"$1"' \
  bash "$contract_root/stale-recovered" >"$contract_root/stale.log" 2>&1
grep -Fq 'state=reclaiming reason=stale-owner owner_pid=99999999' \
  "$contract_root/stale.log"
[[ -e "$contract_root/stale-recovered" && ! -e "$stale_state_dir/active" ]]
assert_state_files_cleaned "$stale_state_dir"
echo ">> a dead active owner is diagnosed and reclaimed before the next suite starts"

# Resolution only: the live EUID root must never be acquired (or even created)
# by this contract, so every call below runs in resolve-only mode with a child
# command that must not execute.
expected_root="/tmp/projmux-e2e-admission-$(id -u)"
resolve_root() {
  local env_root="$1"
  mkdir -p "$env_root/runtime" "$env_root/state" "$env_root/home" "$env_root/tmp"
  env -u PROJMUX_E2E_ADMISSION_STATE_DIR \
    XDG_RUNTIME_DIR="$env_root/runtime" \
    XDG_STATE_HOME="$env_root/state" \
    HOME="$env_root/home" \
    TMPDIR="$env_root/tmp" \
    PROJMUX_E2E_ADMISSION_RESOLVE_ONLY=1 \
    "$admission" bash -c 'exit 99'
}
set +e
resolved_a="$(resolve_root "$contract_root/env-a")"
resolve_a_status=$?
resolved_b="$(resolve_root "$contract_root/env-b")"
resolve_b_status=$?
set -e
if [[ "$resolve_a_status" != "0" || "$resolve_b_status" != "0" ]]; then
  echo "admission resolve-only ran or failed instead of exiting 0: first=$resolve_a_status second=$resolve_b_status" >&2
  exit 1
fi
if [[ "$resolved_a" != "$expected_root" || "$resolved_b" != "$expected_root" ]]; then
  echo "admission root resolution split by caller environment: first=$resolved_a second=$resolved_b expected=$expected_root" >&2
  exit 1
fi
for env_root in "$contract_root/env-a" "$contract_root/env-b"; do
  if [[ -n "$(find "$env_root" -mindepth 2 -print -quit)" ]]; then
    echo "admission resolve-only wrote under a caller environment root: $env_root" >&2
    exit 1
  fi
done

resolve_only_state="$contract_root/resolve-only-state"
resolved_override="$(
  PROJMUX_E2E_ADMISSION_STATE_DIR="$resolve_only_state" \
    PROJMUX_E2E_ADMISSION_RESOLVE_ONLY=1 \
    "$admission" bash -c 'exit 99'
)"
if [[ "$resolved_override" != "$resolve_only_state" ]]; then
  echo "admission resolve-only ignored the isolation override: got=$resolved_override expected=$resolve_only_state" >&2
  exit 1
fi
if [[ -e "$resolve_only_state" ]]; then
  echo "admission resolve-only created the override state dir: $resolve_only_state" >&2
  exit 1
fi
echo ">> two callers with different XDG_RUNTIME_DIR, XDG_STATE_HOME, HOME, and TMPDIR resolve one EUID-pinned admission root without acquiring it"
