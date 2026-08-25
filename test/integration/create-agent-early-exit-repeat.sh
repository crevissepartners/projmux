#!/usr/bin/env bash
set -euo pipefail

# A focused, host-local real-tmux recurrence harness for the create transaction
# authority boundary. The product binary is built once, then every sample gets
# an independent HOME/XDG/Registry/tmux root and two unique socket owners. A
# failed sample is preserved; a green sample is removed only after its queried
# sockets and product routes are proven quiescent.

unset TMUX TMUX_PANE

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ "$#" != "0" ]]; then
  echo "usage: PMX_TEST_EARLY_EXIT_REPEATS=<1..30> $0" >&2
  exit 2
fi
repeats="${PMX_TEST_EARLY_EXIT_REPEATS:-1}"
if [[ ! "$repeats" =~ ^[1-9][0-9]*$ || "$repeats" -gt 30 ]]; then
  echo "usage: PMX_TEST_EARLY_EXIT_REPEATS=<1..30> $0" >&2
  exit 2
fi

attempt_root="$(mktemp -d "${TMPDIR:-/tmp}/projmux-create-authority.XXXXXX")"
build_root="$attempt_root/build"
build_cache="${GOCACHE:-$attempt_root/go-cache}"
mkdir -p "$build_root" "$build_cache"
set +e
GOCACHE="$build_cache" GOTOOLCHAIN=local make -C "$root" build \
  BUILD_DIR="$build_root" PROJMUX_BIN="$build_root/projmux"
build_status=$?
set -e
if [[ "$build_status" != "0" ]]; then
  printf 'scenario=create-agent-early-exit\nphase=attempt-build\nowner=harness\nclass=harness-build\nstatus=%s\nroot=%s\nreplay=%s\n' \
    "$build_status" "$attempt_root" "PMX_TEST_EARLY_EXIT_REPEATS=$repeats $0" >"$attempt_root/failure-summary"
  chmod 0600 "$attempt_root/failure-summary"
  echo "PRESERVED attempt owner=harness class=harness-build root=$attempt_root replay='PMX_TEST_EARLY_EXIT_REPEATS=$repeats $0'" >&2
  exit "$build_status"
fi
bin="$build_root/projmux"
bin_hash="$(sha256sum "$bin" | awk '{print $1}')"
real_tmux="$(command -v tmux)"
echo ">> create authority isolated attempt root=$attempt_root runs=$repeats binary_sha256=$bin_hash owner=harness"

case_root=""
app_socket_path=""
sibling_socket_path=""
app_socket=""
sibling_socket=""
app_started=0
sibling_started=0
case_status="harness"
case_phase="setup"
iteration="0"

case_tmux() {
  local socket="$1"
  shift
  env -u TMUX -u TMUX_PANE \
    HOME="$case_root/home" \
    XDG_CONFIG_HOME="$case_root/config" \
    XDG_RUNTIME_DIR="$case_root/runtime" \
    XDG_STATE_HOME="$case_root/state" \
    TMPDIR="$case_root/tmp" \
    TMUX_TMPDIR="$case_root/tmux" \
    PROJMUX_MANAGED_ROOTS="$case_root/work" \
    PATH="$case_root/bin:$PATH" \
    SHELL=/bin/bash \
    "$real_tmux" -L "$socket" "$@"
}

exact_tmux() {
  local socket_path="$1"
  shift
  case "$socket_path" in
    "$case_root"/*) ;;
    *)
      echo "HARNESS exact tmux route escaped case root: $socket_path" >&2
      return 1
      ;;
  esac
  env -u TMUX -u TMUX_PANE "$real_tmux" -S "$socket_path" "$@"
}

pmx() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE \
    HOME="$case_root/home" \
    XDG_CONFIG_HOME="$case_root/config" \
    XDG_RUNTIME_DIR="$case_root/runtime" \
    XDG_STATE_HOME="$case_root/state" \
    TMPDIR="$case_root/tmp" \
    TMUX_TMPDIR="$case_root/tmux" \
    PROJMUX_MANAGED_ROOTS="$case_root/work" \
    PATH="$case_root/bin:$PATH" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

pmx_inside() {
  local server_pid="$1" anchor_pane="$2"
  shift 2
  env -u __PROJMUX_RUNTIME_ANCHOR_PANE \
    TMUX="$app_socket_path,$server_pid,0" \
    TMUX_PANE="$anchor_pane" \
    HOME="$case_root/home" \
    XDG_CONFIG_HOME="$case_root/config" \
    XDG_RUNTIME_DIR="$case_root/runtime" \
    XDG_STATE_HOME="$case_root/state" \
    TMPDIR="$case_root/tmp" \
    TMUX_TMPDIR="$case_root/tmux" \
    PROJMUX_MANAGED_ROOTS="$case_root/work" \
    PATH="$case_root/bin:$PATH" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

owned_routes() {
  local proc cwd cmdline cmd_hash executable
  for proc in /proc/[0-9]*; do
    [[ -r "$proc/cmdline" ]] || continue
    cwd="$(readlink "$proc/cwd" 2>/dev/null || true)"
    executable="$(readlink "$proc/exe" 2>/dev/null || true)"
    # /proc entries can disappear between the readability probe and open. Put
    # both the redirection and read inside one stderr-suppressed group so that
    # an ordinary process exit is an empty snapshot, never harness noise.
    cmdline="$({ tr '\0' '\n' <"$proc/cmdline"; } 2>/dev/null || true)"
    if [[ "$cwd" == "$case_root" || "$cwd" == "$case_root"/* || "$executable" == "$bin" ]] ||
      grep -Fxq -e "$case_root" -e "$bin" <<<"$cmdline"; then
      cmd_hash="$(sha256sum "$proc/cmdline" 2>/dev/null | awk '{print $1}')"
      printf 'pid=%s executable=%s cmdline_sha256=%s\n' \
        "${proc##*/}" "${executable##*/}" "$cmd_hash"
    fi
  done
}

await_quiescent_routes() {
  local routes quiet=0
  for _ in $(seq 1 200); do
    routes="$(owned_routes)"
    if [[ -z "$routes" ]]; then
      quiet=$((quiet + 1))
      [[ "$quiet" == "10" ]] && return 0
    else
      quiet=0
    fi
    sleep 0.05
  done
  echo "HARNESS owned routes did not quiesce" >&2
  owned_routes >"$case_root/residual-processes.redacted" || true
  return 1
}

cleanup_owned_socket() {
  local socket_name="$1" recorded_path="$2" started="$3"
  local socket_path="$recorded_path" current candidates
  [[ "$started" == "1" ]] || return 0
  if [[ -z "$socket_path" ]]; then
    socket_path="$(case_tmux "$socket_name" display-message -p '#{socket_path}' 2>/dev/null || true)"
  fi
  if [[ -z "$socket_path" ]]; then
    candidates="$(find "$case_root/tmux" -type s -name "$socket_name" -print 2>/dev/null || true)"
    if [[ "$(printf '%s\n' "$candidates" | grep -c .)" == "1" ]]; then
      socket_path="$candidates"
    elif [[ -n "$candidates" ]]; then
      echo "HARNESS socket name resolved multiple cleanup targets: $socket_name" >&2
      return 1
    else
      return 0
    fi
  fi
  case "$socket_path" in
    "$case_root"/*) ;;
    *)
      echo "HARNESS refusing cleanup outside case root: $socket_path" >&2
      return 1
      ;;
  esac
  current="$(exact_tmux "$socket_path" display-message -p '#{socket_path}' 2>/dev/null || true)"
  if [[ -n "$current" ]]; then
    if [[ "$current" != "$socket_path" ]]; then
      echo "HARNESS refusing cleanup after socket identity changed: expected=$socket_path current=$current" >&2
      return 1
    fi
    printf '%s\n' "$socket_path" >>"$case_root/exact-cleanup-targets"
    exact_tmux "$socket_path" kill-server >/dev/null 2>&1 || true
  fi
  for _ in $(seq 1 200); do
    if ! exact_tmux "$socket_path" list-sessions >/dev/null 2>&1; then
      break
    fi
    sleep 0.05
  done
  if exact_tmux "$socket_path" list-sessions >/dev/null 2>&1; then
    echo "HARNESS exact cleanup left server live: $socket_path" >&2
    return 1
  fi
  # tmux can leave an unqueryable Unix socket after its server has exited. It
  # has zero routing authority now; unlink only the exact root-contained path
  # resolved above, then require physical absence.
  if [[ -S "$socket_path" ]]; then
    rm -f -- "$socket_path"
  fi
  if [[ -S "$socket_path" ]]; then
    echo "HARNESS exact cleanup left socket live: $socket_path" >&2
    return 1
  fi
}

cleanup_case() {
  local cleanup_status=0
  cleanup_owned_socket "$app_socket" "$app_socket_path" "$app_started" || cleanup_status=1
  cleanup_owned_socket "$sibling_socket" "$sibling_socket_path" "$sibling_started" || cleanup_status=1
  await_quiescent_routes || cleanup_status=1
  return "$cleanup_status"
}

preserve_failure() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  cleanup_case || true
  printf 'scenario=create-agent-early-exit\niteration=%s\nphase=%s\nowner=%s\nclass=%s\nstatus=%s\nbinary_sha256=%s\nroot=%s\napp_socket=%s\nsibling_socket=%s\nreplay=PMX_TEST_EARLY_EXIT_REPEATS=1 test/integration/create-agent-early-exit-repeat.sh\n' \
    "$iteration" "$case_phase" "${case_status%%-*}" "$case_status" "$status" "$bin_hash" \
    "$case_root" "$app_socket_path" "$sibling_socket_path" >"$case_root/failure-summary"
  chmod 0600 "$case_root/failure-summary"
  echo "PRESERVED first-attempt scenario=create-agent-early-exit iteration=$iteration phase=$case_phase owner=${case_status%%-*} class=$case_status root=$case_root replay='PMX_TEST_EARLY_EXIT_REPEATS=1 test/integration/create-agent-early-exit-repeat.sh'" >&2
  exit "$status"
}
trap preserve_failure EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

reconcile_to_noop() {
  local prefix="$1" socket="$2" pass report
  for pass in 1 2 3 4; do
    report="$prefix-$pass.json"
    pmx reconcile resources --socket "$socket" -o json >"$report"
    if grep -Fq '"outcome": "no-op"' "$report"; then
      return 0
    fi
    grep -Fq '"outcome": "changed"' "$report" || {
      case_status="harness-reconcile"
      return 1
    }
  done
  case_status="harness-reconcile"
  return 1
}

for iteration in $(seq 1 "$repeats"); do
  case_root="$attempt_root/case-$(printf '%02d' "$iteration")"
  app_socket="projmux-authority-$iteration-$$-$RANDOM"
  sibling_socket="projmux-authority-sibling-$iteration-$$-$RANDOM"
  app_socket_path=""
  sibling_socket_path=""
  app_started=0
  sibling_started=0
  case_status="harness-setup"
  case_phase="setup"
  mkdir -p "$case_root"/{home,config,runtime,state,tmp,tmux,bin,work/evidence}
  chmod 0700 "$case_root/runtime" "$case_root/tmux"
  printf '#!/usr/bin/env bash\nexit 42\n' >"$case_root/bin/codex"
  chmod 0755 "$case_root/bin/codex"

  case_tmux "$app_socket" new-session -d -s work-evidence -n main \
    -c "$case_root/work/evidence" sleep 600
  app_started=1
  case_tmux "$app_socket" set-option -t work-evidence -q @projmux_project_path \
    "$case_root/work/evidence"
  case_tmux "$app_socket" set-option -gq @projmux_app 1
  case_tmux "$app_socket" set-option -gq @projmux_socket_name "$app_socket"
  case_tmux "$sibling_socket" new-session -d -s sibling -n main sleep 600
  sibling_started=1
  case_tmux "$sibling_socket" set-option -gq @projmux_authority_sentinel untouched

  app_socket_path="$(case_tmux "$app_socket" display-message -p -t work-evidence '#{socket_path}')"
  sibling_socket_path="$(case_tmux "$sibling_socket" display-message -p -t sibling '#{socket_path}')"
  for socket_path in "$app_socket_path" "$sibling_socket_path"; do
    case "$socket_path" in
      "$case_root"/*) ;;
      *)
        echo "HARNESS queried socket escaped case root: $socket_path" >&2
        exit 1
        ;;
    esac
  done
  server_pid="$(case_tmux "$app_socket" display-message -p -t work-evidence '#{pid}')"
  pmx internal tmux apply --bin "$bin" \
    --config "$case_root/config/projmux/tmux.conf" --socket "$app_socket" \
    >"$case_root/apply.out"

  project_uid="$(pmx create project --root "$case_root/work/evidence" --name evidence -o uid)"
  [[ "$project_uid" =~ ^proj-[a-z2-7]+$ ]] || {
    echo "HARNESS project registration returned malformed receipt: $project_uid" >&2
    exit 1
  }
  reconcile_to_noop "$case_root/reconcile-seed" "$app_socket"
  anchor_receipt="$(exact_tmux "$app_socket_path" list-panes -a \
    -F '#{@projmux_project_uid}|#{pane_id}|#{@projmux_pane_uid}' \
    | awk -F '|' -v project="$project_uid" '$1 == project && $3 != "" { print }')"
  if [[ "$(printf '%s\n' "$anchor_receipt" | grep -c .)" != "1" ]]; then
    echo "HARNESS app owner has no unique anchor: $anchor_receipt" >&2
    exit 1
  fi
  IFS='|' read -r _ anchor_pane anchor_uid <<<"$anchor_receipt"
  [[ "$anchor_pane" =~ ^%[0-9]+$ && "$anchor_uid" =~ ^pane-[a-z2-7]+$ ]] || {
    echo "HARNESS app owner anchor receipt is malformed: $anchor_receipt" >&2
    exit 1
  }

  sibling_before="$(exact_tmux "$sibling_socket_path" show-options -gqv @projmux_authority_sentinel):$(exact_tmux "$sibling_socket_path" list-panes -a -F '#{pane_id}')"
  case_status="product-authority"
  case_phase="claim-to-commit"
  set +e
  agent_uid="$(pmx_inside "$server_pid" "$anchor_pane" create agent \
    --provider codex --project "uid:$project_uid" -o uid \
    2>"$case_root/create.err")"
  create_status=$?
  set -e
  if [[ "$create_status" != "0" || ! "$agent_uid" =~ ^agent-[a-z2-7]+$ ]]; then
    echo "PRODUCT-AUTHORITY create failed status=$create_status agent=$agent_uid" >&2
    exit 1
  fi
  if grep -Fq 'rollback preserved pane' "$case_root/create.err"; then
    echo "PRODUCT-AUTHORITY blank/mismatched rollback ownership recurred" >&2
    exit 1
  fi

  journal="$case_root/state/projmux/termination-receipts.jsonl"
  receipt=""
  for _ in $(seq 1 200); do
    if [[ -s "$journal" ]]; then
      receipt="$(awk -v agent="\"agentUID\":\"$agent_uid\"" \
        -v class='"classification":"abnormal"' -v source='"source":"supervisor"' \
        'index($0, agent) && index($0, class) && index($0, source) { print; exit }' \
        "$journal")"
    fi
    [[ -n "$receipt" ]] && break
    sleep 0.05
  done
  pane_uid="$(printf '%s\n' "$receipt" | sed -n 's/.*"paneUID":"\([^"]*\)".*/\1/p')"
  generation="$(printf '%s\n' "$receipt" | sed -n 's/.*"generation":"\([^"]*\)".*/\1/p')"
  operation="$(printf '%s\n' "$receipt" | sed -n 's/.*"operationID":"\([^"]*\)".*/\1/p')"
  if [[ ! "$pane_uid" =~ ^pane-[a-z2-7]+$ || ! "$generation" =~ ^gen-[a-z2-7]+$ ||
        ! "$operation" =~ ^op-[a-f0-9]+$ ]]; then
    echo "PRODUCT-AUTHORITY missing exact Agent/Pane/generation/operation receipt" >&2
    exit 1
  fi

  case_phase="convergence"
  pmx internal tmux converge --socket-path "$app_socket_path" --reason pane-killed \
    >"$case_root/absorb.out"
  stable=0
  for pass in 1 2 3 4; do
    pmx reconcile resources --socket "$app_socket" -o json >"$case_root/reconcile-final-$pass.json"
    if grep -Fq '"outcome": "no-op"' "$case_root/reconcile-final-$pass.json"; then
      stable=1
      break
    fi
  done
  [[ "$stable" == "1" ]] || {
    echo "PRODUCT-AUTHORITY committed exit did not reach bounded fixed point" >&2
    exit 1
  }
  pmx describe agent "uid:$agent_uid" -o json >"$case_root/agent.json"
  if ! grep -Fq '"phase": "Failed"' "$case_root/agent.json" ||
    grep -Fq '"paneRef":' "$case_root/agent.json" ||
    ! grep -Fq '"classification": "abnormal"' "$case_root/agent.json" ||
    ! grep -Fq '"source": "supervisor"' "$case_root/agent.json"; then
      echo "PRODUCT-AUTHORITY exact committed Agent did not converge" >&2
      exit 1
  fi
  pmx describe pane "uid:$pane_uid" -o json >"$case_root/pane.json"
  exact_tmux "$app_socket_path" list-panes -a -F '#{@projmux_pane_uid}|#{pane_dead}' \
    | grep -Fqx "$pane_uid|1" || {
      echo "PRODUCT-AUTHORITY exact abnormal Pane was not retained dead" >&2
      exit 1
    }
  registry="$case_root/state/projmux/metadata/registry.json"
  before="$(sha256sum "$registry" | awk '{print $1}')"
  pmx reconcile resources --socket "$app_socket" -o json >"$case_root/reconcile-repeat.json"
  after="$(sha256sum "$registry" | awk '{print $1}')"
  [[ "$before" == "$after" ]] || {
    echo "PRODUCT-AUTHORITY fixed-point reconciliation rewrote Registry" >&2
    exit 1
  }
  sibling_after="$(exact_tmux "$sibling_socket_path" show-options -gqv @projmux_authority_sentinel):$(exact_tmux "$sibling_socket_path" list-panes -a -F '#{pane_id}')"
  [[ "$sibling_before" == "$sibling_after" ]] || {
    echo "PRODUCT-AUTHORITY sibling server changed" >&2
    exit 1
  }
  exact_tmux "$app_socket_path" list-panes -a -F '#{@projmux_pane_uid}|#{pane_dead}' \
    | grep -Fqx "$anchor_uid|0" || {
      echo "PRODUCT-AUTHORITY owner anchor changed" >&2
      exit 1
    }
  [[ "$(sha256sum "$bin" | awk '{print $1}')" == "$bin_hash" ]] || {
    case_status="harness-binary"
    echo "HARNESS immutable product binary hash changed" >&2
    exit 1
  }

  case_status="harness-cleanup"
  case_phase="cleanup"
  cleanup_case
  [[ ! -S "$app_socket_path" && ! -S "$sibling_socket_path" ]] || {
    echo "HARNESS exact cleanup left a socket" >&2
    exit 1
  }
  rm -rf -- "$case_root"
  echo ">> create authority isolated iteration=$iteration/$repeats binary_sha256=$bin_hash receipt=exact rollback-blank=0 sibling=byte-identical repeat=fixed-point cleanup=residual0"
done

trap - EXIT INT TERM
rm -rf -- "$attempt_root"
echo ">> create authority isolated summary runs=$repeats recurrence=0 harness-failures=0 binary_sha256=$bin_hash orphan=0"
