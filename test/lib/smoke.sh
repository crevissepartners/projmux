#!/usr/bin/env bash

smoke_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

SMOKE_CONTRACT_ID=""
SMOKE_CONTRACT_PHASE=""
SMOKE_CONTRACT_OWNER=""
SMOKE_CONTRACT_SOURCE=""
SMOKE_CONTRACT_TERMINAL_STATE_PATH=""
SMOKE_CONTRACT_STARTED_MS=0
SMOKE_CONTRACT_TERMINAL=0
SMOKE_CONTRACT_TERMINAL_JSON=""
SMOKE_CONTRACT_DIAGNOSTIC_JSON=""

SMOKE_L06_HOLDER_PID=0
SMOKE_L06_HOLDER_STARTED_MS=0
SMOKE_L06_OPERATION="controller-trigger-burst"
SMOKE_L06_RELEASE_STATE="not-started"
declare -a SMOKE_L06_RACER_PIDS=(0 0 0 0 0 0 0 0)
declare -a SMOKE_L06_RACER_STATUSES=(0 0 0 0 0 0 0 0)
declare -a SMOKE_L06_RACER_OUTCOMES=(not-started not-started not-started not-started not-started not-started not-started not-started)

SMOKE_L08_REGISTRY_BEFORE=""
SMOKE_L08_REGISTRY_AFTER=""
SMOKE_L08_CONTROLLER_ROOT=""
SMOKE_L08_SOCKET_ROOT=""

SMOKE_L16_CHILD_PID=0
SMOKE_L16_CHILD_PID_PATH=""
SMOKE_L16_RC_PATH=""
SMOKE_L16_OUT_PATH=""
SMOKE_L16_ERR_PATH=""
SMOKE_L16_TAIL_PATH=""
SMOKE_L16_TMUX_FUNCTION=""

# `date +%s%3N` is not a millisecond clock everywhere. uutils coreutils ignores
# the %3N field width and prints nanoseconds with leading zeros stripped, so the
# concatenation loses digits roughly one call in ten and the resulting stamp can
# be *smaller* than the one before it. Every elapsed_ms in the evidence was that
# subtraction, which meant a negative elapsed_ms rejected the whole record and
# left the attempt with no terminal outcome. Read seconds and nanoseconds as two
# fields of one clock read instead, and pad defensively before truncating.
smoke_now_ms() {
  local stamp seconds nanoseconds
  stamp="$(date +%s.%N)"
  seconds="${stamp%%.*}"
  nanoseconds="${stamp#*.}"
  while ((${#nanoseconds} < 9)); do
    nanoseconds="0$nanoseconds"
  done
  printf '%s\n' "$((10#$seconds * 1000 + 10#${nanoseconds:0:3}))"
}

# Wait budgets are stated in seconds of wall clock, not in loop iterations, so a
# call site declares the time it is willing to spend rather than a count that
# silently means something different on a slower machine. E2E_WAIT_SCALE
# multiplies every budget, which is how a loaded runner buys time instead of
# reporting a product regression.
SMOKE_WAIT_SCALE=""
SMOKE_WAIT_ATTEMPTS=0
SMOKE_WAIT_ELAPSED_MS=0
SMOKE_WAIT_LAST_STATUS=0

smoke_wait_scale() {
  if [[ -n "$SMOKE_WAIT_SCALE" ]]; then
    printf '%s' "$SMOKE_WAIT_SCALE"
    return 0
  fi
  local scale="${E2E_WAIT_SCALE:-1}"
  if [[ ! "$scale" =~ ^[0-9]+([.][0-9]+)?$ ]] ||
    [[ "$(awk -v k="$scale" 'BEGIN { print (k > 0) ? "y" : "n" }')" != "y" ]]; then
    echo "ignoring invalid E2E_WAIT_SCALE=$scale; using 1" >&2
    scale=1
  fi
  SMOKE_WAIT_SCALE="$scale"
  printf '%s' "$scale"
}

smoke_wait_budget_ms() {
  local seconds="$1"
  if [[ ! "$seconds" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
    echo "wait budget must be a number of seconds, got: $seconds" >&2
    return 2
  fi
  awk -v s="$seconds" -v k="$(smoke_wait_scale)" \
    'BEGIN { ms = s * k * 1000; if (ms < 1) { ms = 1 } printf "%d", ms }'
}

# smoke_wait_until_quiet <budget-seconds> <command> [args...]
#
# The shared polling primitive. It reports nothing, so it is only for waits
# whose caller handles the timeout as ordinary control flow. Every assertion
# wait must use smoke_wait_until instead. The command runs in the current shell,
# so predicate functions can publish what they observed.
smoke_wait_until_quiet() {
  local budget_ms started_ms now_ms
  budget_ms="$(smoke_wait_budget_ms "$1")" || return 2
  shift
  SMOKE_WAIT_ATTEMPTS=0
  SMOKE_WAIT_LAST_STATUS=0
  started_ms="$(smoke_now_ms)"
  now_ms="$started_ms"
  while :; do
    SMOKE_WAIT_ATTEMPTS=$((SMOKE_WAIT_ATTEMPTS + 1))
    SMOKE_WAIT_LAST_STATUS=0
    "$@" || SMOKE_WAIT_LAST_STATUS=$?
    now_ms="$(smoke_now_ms)"
    SMOKE_WAIT_ELAPSED_MS=$((now_ms - started_ms))
    if [[ "$SMOKE_WAIT_LAST_STATUS" == "0" ]]; then
      return 0
    fi
    if ((now_ms >= started_ms + budget_ms)); then
      SMOKE_WAIT_ELAPSED_MS=$((now_ms - started_ms))
      return 1
    fi
    sleep 0.05
  done
}

# smoke_wait_until <budget-seconds> <description> <command> [args...]
#
# Waits for <command> to succeed within the scaled budget. A timeout is always
# an explicit described failure carrying the budget that was actually applied
# and the state the wait ended in; it never returns success, so the caller
# cannot walk into the next assertion and report slowness as a regression.
# Set SMOKE_WAIT_DIAGNOSTIC_LOG to have the timeout tail a scenario log too.
smoke_wait_until() {
  local budget="$1"
  local description="$2"
  shift 2
  local quiet_status=0
  smoke_wait_until_quiet "$budget" "$@" || quiet_status=$?
  if [[ "$quiet_status" == "0" ]]; then
    return 0
  fi
  # A malformed budget is a harness bug, not a slow runner. Keep it distinct so
  # it cannot be read as a timeout.
  if [[ "$quiet_status" == "2" ]]; then
    return 2
  fi
  {
    printf 'timed out waiting for %s\n' "$description"
    printf '  budget=%ss scale=%s elapsed_ms=%s attempts=%s last_status=%s\n' \
      "$budget" "$(smoke_wait_scale)" "$SMOKE_WAIT_ELAPSED_MS" \
      "$SMOKE_WAIT_ATTEMPTS" "$SMOKE_WAIT_LAST_STATUS"
    printf '  command:'
    printf ' %q' "$@"
    printf '\n'
  } >&2
  if [[ -n "${SMOKE_WAIT_DIAGNOSTIC_LOG:-}" && -r "${SMOKE_WAIT_DIAGNOSTIC_LOG:-}" ]]; then
    printf '  diagnostic tail of %s:\n' "$SMOKE_WAIT_DIAGNOSTIC_LOG" >&2
    tail -c 12000 "$SMOKE_WAIT_DIAGNOSTIC_LOG" >&2 || true
  fi
  return 1
}

smoke_contract_state_hash() {
  local registry="${XDG_STATE_HOME:-}/projmux/metadata/registry.json"
  if [[ -f "$registry" ]]; then
    sha256sum "$registry" | awk '{print $1}'
  fi
}

# The second argument is what this call site *declares*, never a verdict. The
# recorder consults the other attempts recorded for the same product binary and
# only falls back to the declaration when they say nothing.
smoke_contract_record() {
  local outcome="$1"
  local declared_class="$2"
  local now elapsed route_socket state_hash
  [[ -n "$SMOKE_CONTRACT_ID" ]] || return 0
  now="$(smoke_now_ms)"
  elapsed=$((now - SMOKE_CONTRACT_STARTED_MS))
  route_socket="${PROJMUX_SMOKE_TMUX_SOCKET:-}"
  state_hash="$(smoke_contract_state_hash)"
  python3 "$smoke_root/scripts/e2e-evidence.py" record \
    --directory "$PROJMUX_E2E_ARTIFACTS" \
    --evidence-root "${PROJMUX_E2E_EVIDENCE_ROOT:-$PROJMUX_E2E_ARTIFACTS}" \
    --scenario-id "$SMOKE_CONTRACT_ID" \
    --suite "$PROJMUX_E2E_SUITE" \
    --attempt "${PROJMUX_E2E_ATTEMPT:-${GITHUB_RUN_ATTEMPT:-1}}" \
    --phase "$SMOKE_CONTRACT_PHASE" \
    --owner "$SMOKE_CONTRACT_OWNER" \
    --class "$declared_class" \
    --outcome "$outcome" \
    --elapsed-ms "$elapsed" \
    --binary-sha256 "${PROJMUX_SMOKE_BIN_SHA256:-${PROJMUX_SMOKE_EXPECTED_BIN_SHA256:-}}" \
    --route-socket "$route_socket" \
    --state-sha256 "$state_hash" \
    --terminal-json "$SMOKE_CONTRACT_TERMINAL_JSON" \
    --diagnostic "$SMOKE_CONTRACT_DIAGNOSTIC_JSON"
}

smoke_contract_shard() {
  if [[ -n "${PROJMUX_E2E_LINUX_SHARD:-}" ]]; then
    printf '%s\n' "$PROJMUX_E2E_LINUX_SHARD"
    return
  fi
  case "${PROJMUX_E2E_SUITE:-}" in
    codex*) printf '%s\n' codex-lifecycle ;;
    npm*) printf '%s\n' npm-staging ;;
    linux) printf '%s\n' all ;;
    *) printf '%s\n' contract ;;
  esac
}

smoke_contract_terminal() {
  local status="$1"
  local line="$2"
  local state_hash
  if [[ -n "$SMOKE_CONTRACT_TERMINAL_STATE_PATH" && -f "$SMOKE_CONTRACT_TERMINAL_STATE_PATH" ]]; then
    state_hash="$(sha256sum "$SMOKE_CONTRACT_TERMINAL_STATE_PATH" | awk '{print $1}')"
  else
    state_hash="$(smoke_contract_state_hash)"
  fi
  python3 "$smoke_root/scripts/e2e-first-failure.py" terminal \
    --scenario "$SMOKE_CONTRACT_ID" \
    --phase "$SMOKE_CONTRACT_PHASE" \
    --owner "$SMOKE_CONTRACT_OWNER" \
    --shard "$(smoke_contract_shard)" \
    --status "$status" \
    --source "$SMOKE_CONTRACT_SOURCE" \
    --line "$line" \
    --command "make test-e2e E2E_SCENARIO=$SMOKE_CONTRACT_ID" \
    --binary-sha256 "${PROJMUX_SMOKE_BIN_SHA256:-${PROJMUX_SMOKE_EXPECTED_BIN_SHA256:-}}" \
    --state-sha256 "$state_hash"
}

smoke_l06_failure_diagnostic() {
  local index
  local -a command=(
    python3 "$smoke_root/scripts/e2e-first-failure.py" l06
    --holder-pid "$SMOKE_L06_HOLDER_PID"
    --holder-started-ms "$SMOKE_L06_HOLDER_STARTED_MS"
    --operation "$SMOKE_L06_OPERATION"
    --release "$SMOKE_L06_RELEASE_STATE"
  )
  for index in {0..7}; do
    command+=(--racer "$((index + 1)):${SMOKE_L06_RACER_PIDS[$index]}:${SMOKE_L06_RACER_STATUSES[$index]}:${SMOKE_L06_RACER_OUTCOMES[$index]}")
  done
  "${command[@]}"
}

smoke_count_entries() {
  local root="$1"
  if [[ ! -d "$root" ]]; then
    printf '0\n'
    return
  fi
  find "$root" -mindepth 1 -maxdepth 3 -print 2>/dev/null | awk 'NR <= 128 { count++ } END { print count + 0 }'
}

smoke_l08_failure_diagnostic() {
  local inventory controller_entries hook_processes owned_processes socket_entries
  inventory="$(smoke_owned_process_inventory)"
  owned_processes="$(printf '%s\n' "$inventory" | awk 'NF && NR <= 128 { count++ } END { print count + 0 }')"
  hook_processes="$(printf '%s\n' "$inventory" | grep -c $'executable=projmux\t' || true)"
  controller_entries="$(smoke_count_entries "$SMOKE_L08_CONTROLLER_ROOT")"
  if [[ -d "$SMOKE_L08_SOCKET_ROOT" ]]; then
    socket_entries="$(find "$SMOKE_L08_SOCKET_ROOT" -mindepth 1 -maxdepth 3 -type s -print 2>/dev/null | awk 'NR <= 128 { count++ } END { print count + 0 }')"
  else
    socket_entries=0
  fi
  python3 "$smoke_root/scripts/e2e-first-failure.py" l08 \
    --before "$SMOKE_L08_REGISTRY_BEFORE" \
    --after "$SMOKE_L08_REGISTRY_AFTER" \
    --controller-entries "$controller_entries" \
    --hook-processes "$hook_processes" \
    --owned-processes "$owned_processes" \
    --socket-entries "$socket_entries"
}

smoke_l16_failure_diagnostic() {
  local child_pid clients panes
  child_pid="$SMOKE_L16_CHILD_PID"
  if [[ -f "$SMOKE_L16_CHILD_PID_PATH" ]]; then
    read -r child_pid <"$SMOKE_L16_CHILD_PID_PATH" || child_pid=0
    [[ "$child_pid" =~ ^[0-9]+$ ]] || child_pid=0
  fi
  clients="$PROJMUX_SMOKE_WORKDIR/l16-clients.failure"
  panes="$PROJMUX_SMOKE_WORKDIR/l16-panes.failure"
  : >"$clients"
  : >"$panes"
  if [[ -n "$SMOKE_L16_TMUX_FUNCTION" ]] && declare -F "$SMOKE_L16_TMUX_FUNCTION" >/dev/null; then
    "$SMOKE_L16_TMUX_FUNCTION" list-clients \
      -F '#{client_pid}|#{session_id}|#{window_id}|#{pane_id}|#{client_key_table}' \
      >"$clients" 2>/dev/null || true
    "$SMOKE_L16_TMUX_FUNCTION" list-panes -a \
      -F '#{pane_pid}|#{session_id}|#{window_id}|#{pane_id}|#{pane_active}|#{pane_dead}|#{pane_dead_status}' \
      >"$panes" 2>/dev/null || true
  fi
  python3 "$smoke_root/scripts/e2e-first-failure.py" l16 \
    --child-pid "$child_pid" \
    --rc "$SMOKE_L16_RC_PATH" \
    --out "$SMOKE_L16_OUT_PATH" \
    --err "$SMOKE_L16_ERR_PATH" \
    --clients "$clients" \
    --panes "$panes" \
    --tail "$SMOKE_L16_TAIL_PATH"
}

smoke_contract_failure_diagnostic() {
  case "$SMOKE_CONTRACT_ID" in
    L06) smoke_l06_failure_diagnostic ;;
    L08) smoke_l08_failure_diagnostic ;;
    L16) smoke_l16_failure_diagnostic ;;
  esac
}

# Tee one first-failure emitter: its record still reaches the job log on stderr,
# and the JSON body is returned so the same bytes can be folded into the attempt
# artifact. A runner disappears at the end of the job; the artifact does not.
smoke_contract_capture() {
  local prefix="$1"
  shift
  local emitted line
  emitted="$("$@" 2>&1 >/dev/null)" || true
  [[ -n "$emitted" ]] || return 0
  printf '%s\n' "$emitted" >&2
  while IFS= read -r line; do
    if [[ "$line" == "$prefix "* ]]; then
      printf '%s' "${line#"$prefix" }"
      return 0
    fi
  done <<<"$emitted"
}

# One terminal path for every failure branch: attribute, diagnose, then record.
# The record runs last so the artifact carries what the log just reported.
smoke_contract_fail() {
  local status="$1"
  local line="$2"
  SMOKE_CONTRACT_TERMINAL_JSON="$(smoke_contract_capture E2E_TERMINAL smoke_contract_terminal "$status" "$line")"
  SMOKE_CONTRACT_DIAGNOSTIC_JSON="$(smoke_contract_capture E2E_DIAGNOSTIC smoke_contract_failure_diagnostic)"
  smoke_contract_record fail "${PROJMUX_E2E_FAILURE_CLASS:-}" >&2
  SMOKE_CONTRACT_TERMINAL=1
}

# Bash does not run ERR for the explicit `exit 1` branches used by assertion
# blocks. Keep exit semantics intact while giving those branches the same exact
# call-site source line as command failures. This function is not exported, so
# fixture children and product commands retain the shell builtin unchanged.
exit() {
  local status="${1:-0}"
  local line="${BASH_LINENO[0]:-0}"
  if [[ "$status" != "0" && -n "$SMOKE_CONTRACT_ID" && "$SMOKE_CONTRACT_TERMINAL" != "1" ]]; then
    set +e
    smoke_contract_fail "$status" "$line"
  fi
  builtin exit "$status"
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
  SMOKE_CONTRACT_SOURCE="${BASH_SOURCE[1]#"$smoke_root/"}"
  SMOKE_CONTRACT_TERMINAL_STATE_PATH=""
  SMOKE_CONTRACT_STARTED_MS="$(smoke_now_ms)"
  SMOKE_CONTRACT_TERMINAL=0
  SMOKE_CONTRACT_TERMINAL_JSON=""
  SMOKE_CONTRACT_DIAGNOSTIC_JSON=""
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
    smoke_contract_fail "$status" "$line"
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

smoke_owned_quiet_streak=0

smoke_owned_quiet_sample() {
  if [[ -n "$(smoke_owned_process_inventory)" ]]; then
    smoke_owned_quiet_streak=0
    return 1
  fi
  smoke_owned_quiet_streak=$((smoke_owned_quiet_streak + 1))
  ((smoke_owned_quiet_streak >= 3))
}

# Cleanup escalation treats a non-quiescent root as ordinary control flow, so
# this wait stays quiet. It still takes a scaled time budget rather than a fixed
# sample count, because a loaded machine is exactly when reaping takes longer.
smoke_wait_owned_quiet() {
  smoke_owned_quiet_streak=0
  smoke_wait_until_quiet 1 smoke_owned_quiet_sample
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
    smoke_contract_record fail "${PROJMUX_E2E_FAILURE_CLASS:-}" >&2
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

smoke_current_frame_contains() {
  local path="$1"
  local offset="$2"
  local needle="$3"
  tail -c "+$((offset + 1))" "$path" 2>/dev/null | grep -aFq "$needle"
}

smoke_wait_for_current_frame() {
  local description="$1"
  local path="$2"
  local offset="$3"
  local needle="$4"
  if smoke_wait_until 5 "current-frame $description offset=$offset" \
    smoke_current_frame_contains "$path" "$offset" "$needle"; then
    return 0
  fi
  tail -c "+$((offset + 1))" "$path" >&2 || true
  return 1
}

# The default scenario wait. It is smoke_wait_until with the 5s budget this
# harness has always used for a settling assertion.
smoke_wait_for() {
  local description="$1"
  shift
  smoke_wait_until 5 "$description" "$@"
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
