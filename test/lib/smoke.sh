#!/usr/bin/env bash

smoke_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

SMOKE_CONTRACT_ID=""
SMOKE_CONTRACT_PHASE=""
SMOKE_CONTRACT_OWNER=""
SMOKE_CONTRACT_STARTED_MS=0
SMOKE_CONTRACT_TERMINAL=0

smoke_now_ms() {
  date +%s%3N
}

smoke_contract_state_hash() {
  local registry="${XDG_STATE_HOME:-}/projmux/metadata/registry.json"
  if [[ -f "$registry" ]]; then
    sha256sum "$registry" | awk '{print $1}'
  fi
}

smoke_contract_record() {
  local outcome="$1"
  local typed_class="$2"
  local now elapsed route_socket state_hash
  [[ -n "$SMOKE_CONTRACT_ID" ]] || return 0
  now="$(smoke_now_ms)"
  elapsed=$((now - SMOKE_CONTRACT_STARTED_MS))
  route_socket="${PROJMUX_SMOKE_TMUX_SOCKET:-}"
  state_hash="$(smoke_contract_state_hash)"
  python3 "$smoke_root/scripts/e2e-evidence.py" record \
    --directory "$PROJMUX_E2E_ARTIFACTS" \
    --scenario-id "$SMOKE_CONTRACT_ID" \
    --suite "$PROJMUX_E2E_SUITE" \
    --attempt "${PROJMUX_E2E_ATTEMPT:-1}" \
    --phase "$SMOKE_CONTRACT_PHASE" \
    --owner "$SMOKE_CONTRACT_OWNER" \
    --class "$typed_class" \
    --outcome "$outcome" \
    --elapsed-ms "$elapsed" \
    --binary-sha256 "${PROJMUX_SMOKE_BIN_SHA256:-${PROJMUX_SMOKE_EXPECTED_BIN_SHA256:-}}" \
    --route-socket "$route_socket" \
    --state-sha256 "$state_hash"
}

smoke_contract_begin() {
  local scenario_id="$1"
  local phase="$2"
  local owner="$3"
  if [[ -n "$SMOKE_CONTRACT_ID" && "$SMOKE_CONTRACT_TERMINAL" != "1" ]]; then
    echo "contract $SMOKE_CONTRACT_ID has no terminal outcome before $scenario_id" >&2
    return 1
  fi
  SMOKE_CONTRACT_ID="$scenario_id"
  SMOKE_CONTRACT_PHASE="$phase"
  SMOKE_CONTRACT_OWNER="$owner"
  SMOKE_CONTRACT_STARTED_MS="$(smoke_now_ms)"
  SMOKE_CONTRACT_TERMINAL=0
  smoke_contract_record begin environment
}

smoke_contract_pass() {
  smoke_contract_record pass environment
  SMOKE_CONTRACT_TERMINAL=1
}

smoke_contract_err() {
  local status="$1"
  local line="$2"
  local had_errexit=0
  case "$-" in
    *e*) had_errexit=1 ;;
  esac
  set +e
  # ERR traps also run for deliberately observed non-zero commands while the
  # caller has `set +e`.  Those commands are part of the assertion protocol,
  # not terminal failures.  In particular, never re-enable errexit behind the
  # caller's back: doing so turns the expected status capture itself into a
  # false deterministic regression.
  if [[ "$had_errexit" == "1" && -n "$SMOKE_CONTRACT_ID" && "$SMOKE_CONTRACT_TERMINAL" != "1" ]]; then
    smoke_contract_record fail "${PROJMUX_E2E_FAILURE_CLASS:-deterministic-regression}" >&2
    SMOKE_CONTRACT_TERMINAL=1
  elif [[ "$had_errexit" == "1" ]]; then
    echo "E2E_CONTRACT unattributed=1 status=$status line=$line" >&2
  fi
  if [[ "$had_errexit" == "1" ]]; then
    set -e
  fi
  return "$status"
}

smoke_contract_install_trap() {
  trap 'smoke_contract_err "$?" "${BASH_LINENO[0]:-0}"' ERR
}

smoke_setup_env() {
  local smoke_parent="${TMPDIR:-/tmp}"
  if [[ ! -d "$smoke_parent" ]]; then
    smoke_parent=/tmp
  fi
  # Keep the owned root short enough that the diagnostics transport remains
  # fully observable in the fixed 80-column real-tmux fixture. Ownership is
  # established by the receipt below, never by trusting this name prefix.
  PROJMUX_SMOKE_WORKDIR="$(mktemp -d "$smoke_parent/pmx.XXXXXX")"
  export PROJMUX_SMOKE_WORKDIR
  PROJMUX_SMOKE_OWNER="$$-$RANDOM-$(smoke_now_ms)"
  export PROJMUX_SMOKE_OWNER
  printf '%s\n' "$PROJMUX_SMOKE_OWNER" >"$PROJMUX_SMOKE_WORKDIR/.projmux-smoke-owner"
  chmod 0600 "$PROJMUX_SMOKE_WORKDIR/.projmux-smoke-owner"
  PROJMUX_E2E_SUITE="${PROJMUX_E2E_SUITE:-e2e}"
  export PROJMUX_E2E_SUITE
  PROJMUX_E2E_ARTIFACTS="${PROJMUX_E2E_ARTIFACTS:-$PROJMUX_SMOKE_WORKDIR/evidence}"
  export PROJMUX_E2E_ARTIFACTS
  mkdir -p "$PROJMUX_E2E_ARTIFACTS"

  # A smoke run must never inherit the caller's tmux client identity.  Keep
  # every bare tmux command, as well as every named -L socket, below a
  # run-unique directory that cleanup can validate before touching a server.
  unset TMUX TMUX_PANE
  PROJMUX_SMOKE_TMUX_ROOT="$PROJMUX_SMOKE_WORKDIR/tmux"
  export PROJMUX_SMOKE_TMUX_ROOT
  export TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT"

  export HOME="$PROJMUX_SMOKE_WORKDIR/home"
  export XDG_CACHE_HOME="$PROJMUX_SMOKE_WORKDIR/cache"
  export XDG_CONFIG_HOME="$PROJMUX_SMOKE_WORKDIR/config"
  export XDG_RUNTIME_DIR="$PROJMUX_SMOKE_WORKDIR/runtime"
  export XDG_STATE_HOME="$PROJMUX_SMOKE_WORKDIR/state"
  export TMPDIR="$PROJMUX_SMOKE_WORKDIR/tmp"
  export GOCACHE="$PROJMUX_SMOKE_WORKDIR/go-cache"
  export PROJMUX_USAGE_STATE_DIR="$PROJMUX_SMOKE_WORKDIR/usage"
  # Suites build without network access, so the module cache must already hold
  # the checked-in module graph. An inherited GOMODCACHE (the harness mounts a
  # prefetched one) wins; otherwise fall back to a run-local cache, which is
  # only sufficient when the build needs no external modules.
  export GOMODCACHE="${GOMODCACHE:-$PROJMUX_SMOKE_WORKDIR/go-mod-cache}"
  export GOTOOLCHAIN=local

  mkdir -p \
    "$HOME" \
    "$XDG_CACHE_HOME" \
    "$XDG_CONFIG_HOME" \
    "$XDG_RUNTIME_DIR" \
    "$XDG_STATE_HOME" \
    "$TMPDIR" \
    "$TMUX_TMPDIR" \
    "$GOCACHE" \
    "$GOMODCACHE" \
    "$PROJMUX_USAGE_STATE_DIR"
  chmod 0700 "$XDG_RUNTIME_DIR" "$TMUX_TMPDIR"
}

smoke_cleanup_tmux_server() {
  local socket_name="${1:-}"
  local actual=""
  local -a tmux_command=(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT" tmux)

  if [[ -n "$socket_name" ]]; then
    actual="$("${tmux_command[@]}" -L "$socket_name" display-message -p '#{socket_path}' 2>/dev/null || true)"
  else
    actual="$("${tmux_command[@]}" display-message -p '#{socket_path}' 2>/dev/null || true)"
  fi
  if [[ -z "$actual" ]]; then
    return
  fi

  case "$actual" in
    "$PROJMUX_SMOKE_TMUX_ROOT"/*) ;;
    *)
      echo "refusing smoke cleanup outside run-local tmux root: $actual" >&2
      return 1
      ;;
  esac

  # Keep an exact audit record before killing through -S.  A socket name is
  # never itself accepted as cleanup authority.
  printf '%s\n' "$actual" >>"$PROJMUX_SMOKE_WORKDIR/tmux-cleanup-targets"
  "${tmux_command[@]}" -S "$actual" kill-server >/dev/null 2>&1 || true
}

smoke_owned_process_inventory() {
  local proc pid cwd command executable reason cmdline_hash
  local -a argv=()
  local -A candidates=()

  # Resolve candidates in two native passes. Forking readlink+tr for every
  # process on a developer workstation made exact cleanup scale with unrelated
  # system process count (and could take minutes). `find -lname` covers owned
  # cwd roots; one fixed-string cmdline scan covers processes that carry the
  # owned root as an argument. Detailed reads remain restricted to that union.
  while IFS= read -r proc; do
    pid="${proc##*/}"
    candidates["$pid"]=1
  done < <(
    find /proc -mindepth 2 -maxdepth 2 -type l -name cwd \
      \( -lname "$PROJMUX_SMOKE_WORKDIR" -o -lname "$PROJMUX_SMOKE_WORKDIR/*" \) \
      -printf '%h\n' 2>/dev/null
  )
  while IFS= read -r proc; do
    pid="${proc%/cmdline}"
    candidates["${pid##*/}"]=1
  done < <(grep -aFl -- "$PROJMUX_SMOKE_WORKDIR" /proc/[0-9]*/cmdline 2>/dev/null || true)

  for pid in "${!candidates[@]}"; do
    if [[ "$pid" == "$$" || "$pid" == "${BASHPID:-$$}" || "$pid" == "$PPID" ]]; then
      continue
    fi
    proc="/proc/$pid"
    cwd="$(readlink "$proc/cwd" 2>/dev/null || true)"
    argv=()
    mapfile -d '' -t argv <"$proc/cmdline" 2>/dev/null || true
    command="${argv[*]}"
    reason=""
    case "$cwd" in
      "$PROJMUX_SMOKE_WORKDIR" | "$PROJMUX_SMOKE_WORKDIR"/*)
        reason="cwd"
        ;;
    esac
    if [[ -n "$command" && "$command" == *"$PROJMUX_SMOKE_WORKDIR"* ]]; then
      reason="${reason:+$reason+}argv"
    fi
    [[ -n "$reason" ]] || continue
    executable="$(readlink "$proc/exe" 2>/dev/null || true)"
    executable="${executable##*/}"
    executable="${executable:-unknown}"
    cmdline_hash="$(printf '%s' "$command" | sha256sum | awk '{print $1}')"
    # Cleanup failure inventories are persisted artifacts. Raw argv/cwd may
    # contain provider tokens, prompts, and private paths; expose only typed
    # ownership, the kernel executable basename, and an equality-safe digest.
    printf '%s\towned_by=%s\texecutable=%s\tcmdline_sha256=%s\n' \
      "$pid" "$reason" "$executable" "$cmdline_hash"
  done
}

smoke_wait_owned_quiet() {
  local stable=0 inventory
  for _ in {1..10}; do
    inventory="$(smoke_owned_process_inventory)"
    if [[ -z "$inventory" ]]; then
      stable=$((stable + 1))
      if [[ "$stable" == "3" ]]; then
        return 0
      fi
    else
      stable=0
    fi
    sleep 0.05
  done
  return 1
}

smoke_reap_owned_processes() {
  local inventory
  local -a pids=()
  inventory="$(smoke_owned_process_inventory)"
  while IFS=$'\t' read -r pid _; do
    [[ -n "$pid" ]] && pids+=("$pid")
  done <<<"$inventory"
  if ((${#pids[@]} == 0)); then
    return 0
  fi
  printf '%s\n' "$inventory" >"$PROJMUX_SMOKE_WORKDIR/residual-processes.before-term"
  kill -TERM "${pids[@]}" 2>/dev/null || true
  if smoke_wait_owned_quiet; then
    return 0
  fi
  pids=()
  while IFS=$'\t' read -r pid _; do
    [[ -n "$pid" ]] && pids+=("$pid")
  done < <(smoke_owned_process_inventory)
  if ((${#pids[@]} > 0)); then
    kill -KILL "${pids[@]}" 2>/dev/null || true
  fi
  smoke_wait_owned_quiet
}

smoke_validate_owned_root() {
  [[ -n "${PROJMUX_SMOKE_WORKDIR:-}" ]] || return 1
  [[ -d "$PROJMUX_SMOKE_WORKDIR" && ! -L "$PROJMUX_SMOKE_WORKDIR" ]] || return 1
  [[ -f "$PROJMUX_SMOKE_WORKDIR/.projmux-smoke-owner" && ! -L "$PROJMUX_SMOKE_WORKDIR/.projmux-smoke-owner" ]] || return 1
  [[ "$(cat "$PROJMUX_SMOKE_WORKDIR/.projmux-smoke-owner")" == "${PROJMUX_SMOKE_OWNER:-}" ]]
}

smoke_cleanup_env() {
  local cleanup_status=0
  if [[ -n "$SMOKE_CONTRACT_ID" && "$SMOKE_CONTRACT_TERMINAL" != "1" ]]; then
    set +e
    smoke_contract_record fail "${PROJMUX_E2E_FAILURE_CLASS:-deterministic-regression}" >&2
    SMOKE_CONTRACT_TERMINAL=1
    set -e
  fi
  if [[ -n "${PROJMUX_SMOKE_TMUX_SOCKET:-}" ]]; then
    smoke_cleanup_tmux_server "$PROJMUX_SMOKE_TMUX_SOCKET" || cleanup_status=$?
  fi
  smoke_cleanup_tmux_server || cleanup_status=$?
  if [[ "$cleanup_status" != "0" ]]; then
    echo "preserving smoke workdir after refused tmux cleanup: $PROJMUX_SMOKE_WORKDIR" >&2
    return "$cleanup_status"
  fi
  if [[ -n "${PROJMUX_SMOKE_WORKDIR:-}" ]]; then
    if ! smoke_validate_owned_root; then
      echo "refusing smoke cleanup without exact owner receipt: $PROJMUX_SMOKE_WORKDIR" >&2
      return 1
    fi
    if ! smoke_wait_owned_quiet; then
      smoke_reap_owned_processes || cleanup_status=$?
    fi
    if [[ "$cleanup_status" != "0" ]] || ! smoke_wait_owned_quiet; then
      smoke_owned_process_inventory >"$PROJMUX_SMOKE_WORKDIR/residual-processes.final" || true
      echo "preserving non-quiescent owned smoke root: $PROJMUX_SMOKE_WORKDIR" >&2
      return 1
    fi
    # Go module downloads are intentionally read-only. Make only this validated
    # run-local tree writable again so direct/local smoke cleanup can remove it.
    chmod -R u+w "$PROJMUX_SMOKE_WORKDIR" 2>/dev/null || true
    rm -rf "$PROJMUX_SMOKE_WORKDIR"
  fi
}

smoke_wait_for_current_frame() {
  local description="$1"
  local path="$2"
  local offset="$3"
  local needle="$4"
  for _ in {1..100}; do
    if tail -c "+$((offset + 1))" "$path" 2>/dev/null | grep -aFq "$needle"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for current-frame $description offset=$offset" >&2
  tail -c "+$((offset + 1))" "$path" >&2 || true
  return 1
}

smoke_wait_for() {
  local description="$1"
  shift
  for _ in {1..100}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  if [[ -n "${SMOKE_WAIT_DIAGNOSTIC_LOG:-}" ]]; then
    tail -c 12000 "$SMOKE_WAIT_DIAGNOSTIC_LOG" >&2 || true
  fi
  return 1
}

smoke_require_uid() {
  local label="$1"
  local kind="$2"
  local value="$3"
  local prefix
  case "$kind" in
    project) prefix=proj ;;
    window) prefix=win ;;
    pane) prefix=pane ;;
    agent) prefix=agent ;;
    control-session) prefix=ctl ;;
    *) echo "unknown identity receipt kind: $kind" >&2; return 2 ;;
  esac
  if [[ ! "$value" =~ ^${prefix}-[a-z2-7]{26}$ ]]; then
    echo "$label returned an invalid identity receipt: [$value]" >&2
    return 1
  fi
}

smoke_bounded_fixed_point() {
  local report_prefix="$1"
  shift
  local pass report outcome hash
  local -A seen=()
  for pass in 1 2 3 4; do
    report="$report_prefix-$pass.json"
    "$@" >"$report"
    hash="$(sha256sum "$report" | awk '{print $1}')"
    if grep -Fq '"outcome":"no-op"' "$report" || grep -Fq '"outcome": "no-op"' "$report"; then
      grep -Eq '"items":[[:space:]]*\[\]|"items": \[\]' "$report" || return 1
      return 0
    fi
    if ! grep -Fq '"outcome":"changed"' "$report" && ! grep -Fq '"outcome": "changed"' "$report"; then
      echo "fixed-point report has no typed outcome: $report" >&2
      return 1
    fi
    if [[ -n "${seen[$hash]:-}" ]]; then
      echo "fixed-point oscillation/cycle detected: pass=$pass prior=${seen[$hash]} hash=$hash" >&2
      return 1
    fi
    seen[$hash]="$pass"
  done
  echo "fixed-point did not converge within 4 passes" >&2
  return 1
}

smoke_build_binary() {
  if [[ -n "${PROJMUX_SMOKE_PREBUILT_BIN:-}" ]]; then
    if [[ ! -f "$PROJMUX_SMOKE_PREBUILT_BIN" || -L "$PROJMUX_SMOKE_PREBUILT_BIN" || ! -x "$PROJMUX_SMOKE_PREBUILT_BIN" ]]; then
      echo "prebuilt smoke binary must be a regular executable: $PROJMUX_SMOKE_PREBUILT_BIN" >&2
      return 1
    fi
    PROJMUX_SMOKE_BIN_SHA256="$(sha256sum "$PROJMUX_SMOKE_PREBUILT_BIN" | awk '{print $1}')"
    if [[ -z "${PROJMUX_SMOKE_EXPECTED_BIN_SHA256:-}" || "$PROJMUX_SMOKE_BIN_SHA256" != "$PROJMUX_SMOKE_EXPECTED_BIN_SHA256" ]]; then
      echo "prebuilt smoke binary hash mismatch: got=$PROJMUX_SMOKE_BIN_SHA256 expected=${PROJMUX_SMOKE_EXPECTED_BIN_SHA256:-missing}" >&2
      return 1
    fi
    PROJMUX_SMOKE_BIN="$PROJMUX_SMOKE_PREBUILT_BIN"
    export PROJMUX_SMOKE_BIN PROJMUX_SMOKE_BIN_SHA256
    echo ">> using immutable prebuilt $PROJMUX_SMOKE_BIN sha256=$PROJMUX_SMOKE_BIN_SHA256"
    return
  fi
  make build BUILD_DIR="$PROJMUX_SMOKE_WORKDIR/build" PROJMUX_BIN="$PROJMUX_SMOKE_WORKDIR/build/projmux"
  PROJMUX_SMOKE_BIN="$PROJMUX_SMOKE_WORKDIR/build/projmux"
  PROJMUX_SMOKE_BIN_SHA256="$(sha256sum "$PROJMUX_SMOKE_BIN" | awk '{print $1}')"
  export PROJMUX_SMOKE_BIN PROJMUX_SMOKE_BIN_SHA256
}

smoke_assert_file_contains() {
  local path="$1"
  local needle="$2"
  if ! grep -Fq "$needle" "$path"; then
    echo "expected $path to contain: $needle" >&2
    exit 1
  fi
}

smoke_assert_output_contains() {
  local output="$1"
  local needle="$2"
  if [[ "$output" != *"$needle"* ]]; then
    echo "expected output to contain: $needle" >&2
    echo "$output" >&2
    exit 1
  fi
}

# smoke_assert_file_lacks fails when the file contains needle. It is the
# negative half used by the CLI internal-namespace audit: proving a generated
# config no longer emits a relocated route needs an absence assertion, not
# another presence assertion.
smoke_assert_file_lacks() {
  local path="$1"
  local needle="$2"
  if grep -Fq "$needle" "$path"; then
    echo "expected $path to NOT contain: $needle" >&2
    grep -Fn "$needle" "$path" >&2 || true
    exit 1
  fi
}
