#!/usr/bin/env bash
set -euo pipefail

unset TMUX TMUX_PANE
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
PROJMUX_E2E_SUITE="codex-lifecycle"
export PROJMUX_E2E_SUITE
smoke_contract_install_trap
smoke_contract_begin C01 native-lifecycle codex-appserver-adapter
cd "$smoke_root"
smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

lifecycle_root="$PROJMUX_SMOKE_WORKDIR/codex-lifecycle"
lifecycle_socket="projmux-codex-lifecycle-$$-$RANDOM"
lifecycle_session="projects-lifecycle"
lifecycle_project="$lifecycle_root/projects/lifecycle"
lifecycle_shim="$lifecycle_root/shim"
lifecycle_fixture_state="$lifecycle_root/fake-codex-state"
lifecycle_codex_home="$lifecycle_root/codex-home"
lifecycle_notify_log="$lifecycle_root/desktop-notify-count"
lifecycle_notify_hook="$lifecycle_root/desktop-notify-hook"
lifecycle_real_tmux="$(command -v tmux)"
lifecycle_started=0
lifecycle_control_pid=""
lifecycle_control_socket="$lifecycle_codex_home/app-server-control/app-server-control.sock"
lifecycle_agent_uid=""
lifecycle_sibling_agent_uid=""
lifecycle_pane_uid=""
lifecycle_sibling_pane_uid=""
mkdir -p "$lifecycle_project" "$lifecycle_shim" "$lifecycle_fixture_state" "$lifecycle_codex_home"
export CODEX_HOME="$lifecycle_codex_home"

# shellcheck disable=SC2016 # Expands in the generated notification hook at runtime.
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'printf "1\\n" >>"${PROJMUX_FAKE_NOTIFY_LOG:?}"' >"$lifecycle_notify_hook"
chmod 0755 "$lifecycle_notify_hook"
export PROJMUX_NOTIFY_HOOK="$lifecycle_notify_hook"
export PROJMUX_FAKE_NOTIFY_LOG="$lifecycle_notify_log"

go build -o "$lifecycle_shim/codex" ./test/e2e/fake_codex_appserver_fixture.go
cat >"$lifecycle_shim/tmux" <<TMUX_SHIM
#!/usr/bin/env bash
case "\${1:-}" in
  -L|-S)
    exec env TMUX_TMPDIR=$(printf %q "$TMUX_TMPDIR") $(printf %q "$lifecycle_real_tmux") "\$@"
    ;;
  *)
    exec env TMUX_TMPDIR=$(printf %q "$TMUX_TMPDIR") $(printf %q "$lifecycle_real_tmux") -L $(printf %q "$lifecycle_socket") "\$@"
    ;;
esac
TMUX_SHIM
chmod 0755 "$lifecycle_shim/tmux"

lifecycle_tmux() {
  env -u TMUX -u TMUX_PANE "$lifecycle_real_tmux" -L "$lifecycle_socket" "$@"
}

lifecycle_pmx() {
  env -u TMUX -u TMUX_PANE -u PMX_INTERNAL_ACTIVATION_PANE_UID -u PMX_INTERNAL_ACTIVATION_GENERATION \
    PATH="$lifecycle_shim:$PATH" \
    PROJMUX_FAKE_CODEX_STATE="$lifecycle_fixture_state" \
    PROJMUX_MANAGED_ROOTS="$lifecycle_root/projects" \
    "$bin" "$@"
}

# Keep install topology diagnosis inside C01 while leaving the existing native
# lifecycle fixture and tmux authority sequence unchanged. These Doctor calls
# run before the isolated tmux server exists and always strip caller identity.
lifecycle_topology_root="$lifecycle_root/topology"
lifecycle_topology_ready_shim="$lifecycle_topology_root/ready-path"
lifecycle_topology_missing_shim="$lifecycle_topology_root/missing-path"
lifecycle_topology_invocations="$lifecycle_topology_root/codex-invocations"
lifecycle_topology_stderr_sentinel='managed standalone Codex install not found at /private/e2e-topology token=e2e-topology-secret prompt=e2e-topology-secret'
mkdir -p "$lifecycle_topology_ready_shim" "$lifecycle_topology_missing_shim"
: >"$lifecycle_topology_invocations"

cat >"$lifecycle_topology_ready_shim/codex" <<'READY_CODEX'
#!/bin/bash
set -euo pipefail
printf '%s\n' "$*" >>"${PROJMUX_FAKE_CODEX_INVOCATIONS:?}"
exec "${PROJMUX_FAKE_CODEX_BINARY:?}" "$@"
READY_CODEX
cat >"$lifecycle_topology_missing_shim/codex" <<'MISSING_CODEX'
#!/bin/bash
set -euo pipefail
printf '%s\n' "$*" >>"${PROJMUX_FAKE_CODEX_INVOCATIONS:?}"
printf '%s\n' 'managed standalone Codex install not found at /private/e2e-topology token=e2e-topology-secret prompt=e2e-topology-secret' >&2
exit 0
MISSING_CODEX
chmod 0755 "$lifecycle_topology_ready_shim/codex" "$lifecycle_topology_missing_shim/codex"

lifecycle_topology_snapshot() {
  local root="$1" path relative metadata digest
  while IFS= read -r path; do
    relative="${path#"$root"}"
    metadata="$(stat -c '%F|%a|%s' "$path")"
    digest=directory
    if [[ -f "$path" ]]; then
      digest="$(sha256sum "$path" | awk '{print $1}')"
    fi
    printf '%s|%s|%s\n' "$relative" "$metadata" "$digest"
  done < <(find "$root" -print | LC_ALL=C sort)
}

lifecycle_topology_doctor() {
  local name="$1" path="$2" capability="$3" source="$4" availability="$5" reason="$6" probe_reason="$7" connection="$8"
  local endpoint_readiness="$9" running_executable="${10}" version_relation="${11}" manager_ownership="${12}" remote_control="${13}"
  local native_action="${14}" native_refusal="${15}" interruption_risk="${16}" operator_recovery="${17}"
  local codex_home="$lifecycle_topology_root/$name-codex-home"
  local before="$lifecycle_topology_root/$name.before" after="$lifecycle_topology_root/$name.after"
  local output="$lifecycle_topology_root/$name.json" stderr="$lifecycle_topology_root/$name.err"
  mkdir -p "$codex_home"
  printf '%s\n' 'user-state-must-remain-byte-identical' >"$codex_home/user-state-sentinel"
  chmod 0600 "$codex_home/user-state-sentinel"
  if [[ "$capability" == "managed-ready" ]]; then
    mkdir -p "$codex_home/packages/standalone/current/bin"
    printf '%s\n' managed >"$codex_home/packages/standalone/current/bin/codex"
    chmod 0755 "$codex_home/packages/standalone/current/bin/codex"
  fi
  lifecycle_topology_snapshot "$codex_home" >"$before"
  env -u TMUX -u TMUX_PANE \
    CODEX_HOME="$codex_home" \
    PATH="$path" \
    PROJMUX_FAKE_CODEX_BINARY="$lifecycle_shim/codex" \
    PROJMUX_FAKE_CODEX_INVOCATIONS="$lifecycle_topology_invocations" \
    PROJMUX_FAKE_CODEX_STATE="$lifecycle_fixture_state" \
    "$bin" doctor --section integrations --json >"$output" 2>"$stderr"
  lifecycle_topology_snapshot "$codex_home" >"$after"
  if ! cmp -s "$before" "$after"; then
    echo "Codex lifecycle topology Doctor changed CODEX_HOME for $name" >&2
    diff -u "$before" "$after" >&2 || true
    exit 1
  fi
  smoke_assert_file_contains "$output" '"source": "'"$source"'"'
  smoke_assert_file_contains "$output" '"availability": "'"$availability"'"'
  smoke_assert_file_contains "$output" '"reason": "'"$reason"'"'
  smoke_assert_file_contains "$output" '"probe_reason": "'"$probe_reason"'"'
  smoke_assert_file_contains "$output" '"install_capability": "'"$capability"'"'
  smoke_assert_file_contains "$output" '"connection_state": "'"$connection"'"'
  smoke_assert_file_contains "$output" '"endpoint_readiness": "'"$endpoint_readiness"'"'
  smoke_assert_file_contains "$output" '"running_executable": "'"$running_executable"'"'
  smoke_assert_file_contains "$output" '"version_relation": "'"$version_relation"'"'
  smoke_assert_file_contains "$output" '"manager_ownership": "'"$manager_ownership"'"'
  smoke_assert_file_contains "$output" '"remote_control_capability": "'"$remote_control"'"'
  smoke_assert_file_contains "$output" '"native_action_readiness": "'"$native_action"'"'
  smoke_assert_file_contains "$output" '"native_action_refusal": "'"$native_refusal"'"'
  smoke_assert_file_contains "$output" '"interruption_risk": "'"$interruption_risk"'"'
  smoke_assert_file_contains "$output" '"operator_recovery": "'"$operator_recovery"'"'
  smoke_assert_file_contains "$output" '"lifecycle_outcome": "not-attempted"'
  smoke_assert_file_contains "$output" '"lifecycle_reason": "read-only"'
}

lifecycle_topology_doctor external-ready "$lifecycle_topology_ready_shim" external-cli-only app-server available none none ready \
  ready managed current managed disabled ready none none none
lifecycle_topology_doctor managed-ready "$lifecycle_topology_ready_shim" managed-ready app-server available none none ready \
  ready managed current managed disabled ready none none none
# uncaptured-default: this argument is the app-server `connection_state`, an
# observed transport state, not the managed Codex authority reason whose
# literal default this gate exists to keep out of assertions.
lifecycle_topology_doctor endpoint-missing "$lifecycle_topology_missing_shim" external-cli-only unavailable unavailable hook-unavailable daemon-not-running disconnected \
  dead unknown unknown unknown unavailable ready none none none

if grep -Eq 'app-server daemon (start|stop|restart|kill)|enable-remote-control|disable-remote-control|(^| )login($| )|(^| )logout($| )|config (set|write)' "$lifecycle_topology_invocations" ||
  [[ "$(grep -Fxc 'app-server proxy' "$lifecycle_topology_invocations" || true)" != "3" ]] ||
  [[ "$(grep -Fxc 'app-server daemon version' "$lifecycle_topology_invocations" || true)" != "3" ]]; then
  echo "Codex lifecycle topology issued unexpected process calls" >&2
  cat "$lifecycle_topology_invocations" >&2
  exit 1
fi
if grep -Fq "$lifecycle_topology_stderr_sentinel" "$lifecycle_topology_root"/*.json "$lifecycle_topology_root"/*.err ||
  grep -Fq '/private/e2e-topology' "$lifecycle_topology_root"/*.json "$lifecycle_topology_root"/*.err ||
  grep -Fq 'token=e2e-topology-secret' "$lifecycle_topology_root"/*.json "$lifecycle_topology_root"/*.err ||
  grep -Fq 'prompt=e2e-topology-secret' "$lifecycle_topology_root"/*.json "$lifecycle_topology_root"/*.err; then
  echo "Codex lifecycle topology exposed raw process output" >&2
  exit 1
fi

lifecycle_pmx_at_anchor() {
  env -u PMX_INTERNAL_ACTIVATION_PANE_UID -u PMX_INTERNAL_ACTIVATION_GENERATION \
    TMUX="$lifecycle_socket_path,$lifecycle_server_pid,0" \
    TMUX_PANE="$lifecycle_anchor_pane" \
    PATH="$lifecycle_shim:$PATH" \
    PROJMUX_FAKE_CODEX_STATE="$lifecycle_fixture_state" \
    PROJMUX_MANAGED_ROOTS="$lifecycle_root/projects" \
    "$bin" "$@"
}

lifecycle_background_routes() {
  ps -eo pid=,args= | \
    PROJMUX_ROUTE_BIN="$bin" \
    PROJMUX_ROUTE_AGENT_UID="$lifecycle_agent_uid" \
    PROJMUX_ROUTE_SIBLING_AGENT_UID="$lifecycle_sibling_agent_uid" \
    PROJMUX_ROUTE_PANE_UID="$lifecycle_pane_uid" \
    PROJMUX_ROUTE_SIBLING_PANE_UID="$lifecycle_sibling_pane_uid" \
    awk '
      index($0, ENVIRON["PROJMUX_ROUTE_BIN"] " internal agent-hook ingest codex-broker-watch") == 0 { next }
      ENVIRON["PROJMUX_ROUTE_AGENT_UID"] != "" &&
        index($0, "--agent-uid " ENVIRON["PROJMUX_ROUTE_AGENT_UID"] " ") > 0 { print; next }
      ENVIRON["PROJMUX_ROUTE_SIBLING_AGENT_UID"] != "" &&
        index($0, "--agent-uid " ENVIRON["PROJMUX_ROUTE_SIBLING_AGENT_UID"] " ") > 0 { print; next }
      ENVIRON["PROJMUX_ROUTE_PANE_UID"] != "" &&
        index($0, "--pane-uid " ENVIRON["PROJMUX_ROUTE_PANE_UID"] " ") > 0 { print; next }
      ENVIRON["PROJMUX_ROUTE_SIBLING_PANE_UID"] != "" &&
        index($0, "--pane-uid " ENVIRON["PROJMUX_ROUTE_SIBLING_PANE_UID"] " ") > 0 { print }
    '
}

lifecycle_control_pids() {
  ps -eo pid=,args= | \
    PROJMUX_CONTROL_BIN="$lifecycle_shim/codex" \
    awk '
      {
        pid = $1
        argv = $0
        sub(/^[[:space:]]*[0-9]+[[:space:]]+/, "", argv)
        if (pid ~ /^[1-9][0-9]*$/ && argv == ENVIRON["PROJMUX_CONTROL_BIN"] " app-server fixture-control") {
          print pid
        }
      }
    '
}

start_lifecycle_control_server() {
  local actual=""
  if [[ -n "$lifecycle_control_pid" ]] && kill -0 "$lifecycle_control_pid" 2>/dev/null; then
    echo "refusing to start a second Codex lifecycle control peer: pid=$lifecycle_control_pid" >&2
    return 1
  fi
  env -u TMUX -u TMUX_PANE \
    CODEX_HOME="$lifecycle_codex_home" \
    PROJMUX_FAKE_CODEX_STATE="$lifecycle_fixture_state" \
    PROJMUX_SMOKE_WORKDIR="$PROJMUX_SMOKE_WORKDIR" \
    "$lifecycle_shim/codex" app-server fixture-control \
    >>"$lifecycle_root/control.out" 2>>"$lifecycle_root/control.err" &
  lifecycle_control_pid=$!
  for _ in $(seq 1 400); do
    if ! kill -0 "$lifecycle_control_pid" 2>/dev/null; then
      wait "$lifecycle_control_pid" 2>/dev/null || true
      echo "Codex lifecycle control peer exited before publishing its socket" >&2
      cat "$lifecycle_root/control.err" >&2 2>/dev/null || true
      lifecycle_control_pid=""
      return 1
    fi
    actual="$(lifecycle_control_pids)"
    if [[ -S "$lifecycle_control_socket" && "$actual" == "$lifecycle_control_pid" ]]; then
      return 0
    fi
    sleep 0.025
  done
  echo "Codex lifecycle control peer did not publish one exact isolated socket: pid=$lifecycle_control_pid actual=$actual socket=$lifecycle_control_socket" >&2
  return 1
}

stop_lifecycle_control_server() {
  local pid="$lifecycle_control_pid" actual="" expected="$lifecycle_shim/codex app-server fixture-control"
  if [[ -z "$pid" ]]; then
    return 0
  fi
  if kill -0 "$pid" 2>/dev/null; then
    actual="$(ps -p "$pid" -o args= 2>/dev/null || true)"
    if [[ "$actual" != "$expected" ]]; then
      echo "refusing to terminate a drifted Codex lifecycle control peer: pid=$pid argv=$actual" >&2
      return 1
    fi
    kill -TERM "$pid"
  fi
  for _ in $(seq 1 400); do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" 2>/dev/null || true
      lifecycle_control_pid=""
      if [[ -S "$lifecycle_control_socket" ]]; then
        echo "Codex lifecycle control socket survived its exact peer: $lifecycle_control_socket" >&2
        return 1
      fi
      return 0
    fi
    sleep 0.025
  done
  echo "Codex lifecycle control peer survived TERM: pid=$pid" >&2
  return 1
}

lifecycle_cleanup() {
  local actual="" cleanup_status=0 routes_stopped=0
  if [[ "$lifecycle_started" == "1" ]]; then
    actual="$(lifecycle_tmux display-message -p -t "$lifecycle_session" '#{socket_path}' 2>/dev/null || true)"
    if [[ -n "$actual" ]]; then
      case "$actual" in
        "$PROJMUX_SMOKE_TMUX_ROOT"/*)
          env -u TMUX -u TMUX_PANE "$lifecycle_real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
          ;;
        *)
          echo "refusing Codex lifecycle cleanup outside smoke root: $actual" >&2
          cleanup_status=1
          ;;
      esac
    fi
    lifecycle_started=0
  fi
  for _ in $(seq 1 200); do
    if [[ -z "$(lifecycle_background_routes)" ]]; then
      routes_stopped=1
      break
    fi
    sleep 0.025
  done
  if [[ "$routes_stopped" != "1" ]]; then
    echo "Codex lifecycle watcher survived exact server cleanup:" >&2
    lifecycle_background_routes >&2
    cleanup_status=1
  fi
  if ! stop_lifecycle_control_server; then
    cleanup_status=1
  fi
  return "$cleanup_status"
}
trap 'lifecycle_cleanup; smoke_cleanup_env' EXIT

start_lifecycle_control_server
lifecycle_started=1
lifecycle_anchor_pane="$(lifecycle_tmux new-session -d -P -F '#{pane_id}' -s "$lifecycle_session" -c "$lifecycle_project" sleep 600)"
if [[ ! "$lifecycle_anchor_pane" =~ ^%[0-9]+$ ]]; then
  echo "Codex lifecycle standalone server returned no exact anchor Pane: $lifecycle_anchor_pane" >&2
  exit 1
fi
lifecycle_tmux set-option -t "$lifecycle_session" -q @projmux_project_path "$lifecycle_project"
lifecycle_initial_receipt="$(lifecycle_tmux display-message -p -t "$lifecycle_anchor_pane" \
  '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_app}|#{@projmux_socket_name}')"
IFS='|' read -r lifecycle_socket_path lifecycle_server_pid lifecycle_session_id lifecycle_window_id \
  lifecycle_receipt_pane lifecycle_app_marker lifecycle_logical_marker <<<"$lifecycle_initial_receipt"
case "$lifecycle_socket_path" in
  "$PROJMUX_SMOKE_TMUX_ROOT"/*) ;;
  *)
    echo "Codex lifecycle socket escaped isolated root: $lifecycle_socket_path" >&2
    exit 1
    ;;
esac
if [[ ! "$lifecycle_server_pid" =~ ^[1-9][0-9]*$ ]] ||
  [[ ! "$lifecycle_session_id" =~ ^\$[0-9]+$ ]] ||
  [[ ! "$lifecycle_window_id" =~ ^@[0-9]+$ ]] ||
  [[ "$lifecycle_receipt_pane" != "$lifecycle_anchor_pane" ]] ||
  [[ -n "$lifecycle_app_marker" || -n "$lifecycle_logical_marker" ]]; then
  echo "Codex lifecycle standalone authority receipt is incomplete or marked: $lifecycle_initial_receipt" >&2
  exit 1
fi

lifecycle_project_uid="$(lifecycle_pmx create project --root "$lifecycle_project" --name lifecycle -o uid)"
if [[ -z "$lifecycle_project_uid" ]]; then
  echo "Codex lifecycle fixture did not create an exact Registry Project" >&2
  exit 1
fi
lifecycle_pmx reconcile resources --socket-path "$lifecycle_socket_path" >/dev/null
lifecycle_managed_receipt="$(lifecycle_tmux display-message -p -t "$lifecycle_anchor_pane" \
  '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_project_uid}|#{@projmux_window_uid}|#{@projmux_pane_uid}|#{@projmux_app}|#{@projmux_socket_name}')"
IFS='|' read -r lifecycle_managed_path lifecycle_managed_pid lifecycle_managed_session lifecycle_managed_window \
  lifecycle_managed_pane lifecycle_runtime_project_uid lifecycle_window_uid lifecycle_anchor_uid \
  lifecycle_managed_app lifecycle_managed_logical <<<"$lifecycle_managed_receipt"
if [[ "$lifecycle_managed_path" != "$lifecycle_socket_path" ]] ||
  [[ "$lifecycle_managed_pid" != "$lifecycle_server_pid" ]] ||
  [[ "$lifecycle_managed_session" != "$lifecycle_session_id" ]] ||
  [[ "$lifecycle_managed_window" != "$lifecycle_window_id" ]] ||
  [[ "$lifecycle_managed_pane" != "$lifecycle_anchor_pane" ]] ||
  [[ "$lifecycle_runtime_project_uid" != "$lifecycle_project_uid" ]] ||
  [[ -z "$lifecycle_window_uid" || -z "$lifecycle_anchor_uid" ]] ||
  [[ -n "$lifecycle_managed_app" || -n "$lifecycle_managed_logical" ]]; then
  echo "Codex lifecycle reconcile did not preserve the exact standalone managed chain: $lifecycle_managed_receipt" >&2
  exit 1
fi
lifecycle_registry_window_uid="$(lifecycle_pmx get windows --project "uid:$lifecycle_project_uid" -o uid)"
lifecycle_registry_pane_uid="$(lifecycle_pmx get panes --project "uid:$lifecycle_project_uid" --window "uid:$lifecycle_window_uid" -o uid)"
if [[ "$lifecycle_registry_window_uid" != "$lifecycle_window_uid" ]] ||
  [[ "$lifecycle_registry_pane_uid" != "$lifecycle_anchor_uid" ]]; then
  echo "Codex lifecycle exact runtime chain disagrees with Registry: window=$lifecycle_registry_window_uid pane=$lifecycle_registry_pane_uid" >&2
  exit 1
fi

lifecycle_pane="$(lifecycle_pmx_at_anchor create agent --provider codex --project "uid:$lifecycle_project_uid" \
  --window "uid:$lifecycle_window_uid" -o pane-id -- "phase3 lifecycle smoke")"
if [[ ! "$lifecycle_pane" =~ ^%[0-9]+$ ]]; then
  echo "native lifecycle create returned invalid pane: $lifecycle_pane" >&2
  exit 1
fi
lifecycle_pane_uid="$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" @projmux_pane_uid)"
lifecycle_agent_receipt="$(lifecycle_tmux display-message -p -t "$lifecycle_pane" \
  '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_project_uid}|#{@projmux_window_uid}|#{@projmux_pane_uid}')"
IFS='|' read -r lifecycle_agent_path lifecycle_agent_pid lifecycle_agent_session lifecycle_agent_window \
  lifecycle_agent_pane lifecycle_agent_project_uid lifecycle_agent_window_uid lifecycle_agent_pane_uid <<<"$lifecycle_agent_receipt"
if [[ "$lifecycle_agent_path" != "$lifecycle_socket_path" ]] ||
  [[ "$lifecycle_agent_pid" != "$lifecycle_server_pid" ]] ||
  [[ "$lifecycle_agent_session" != "$lifecycle_session_id" ]] ||
  [[ "$lifecycle_agent_window" != "$lifecycle_window_id" ]] ||
  [[ "$lifecycle_agent_pane" != "$lifecycle_pane" ]] ||
  [[ "$lifecycle_agent_project_uid" != "$lifecycle_project_uid" ]] ||
  [[ "$lifecycle_agent_window_uid" != "$lifecycle_window_uid" ]] ||
  [[ -z "$lifecycle_agent_pane_uid" || "$lifecycle_agent_pane_uid" != "$lifecycle_pane_uid" ]] ||
  [[ "$(lifecycle_pmx get panes --project "uid:$lifecycle_project_uid" --window "uid:$lifecycle_window_uid" -o uid | grep -Fxc "$lifecycle_pane_uid")" != "1" ]]; then
  echo "native lifecycle create escaped the exact standalone managed chain: $lifecycle_agent_receipt" >&2
  exit 1
fi
lifecycle_generation="$(lifecycle_pmx describe pane "uid:$lifecycle_pane_uid" | awk '$1 == "BindingGeneration:" { print $2; exit }')"
lifecycle_agent_uid="$(lifecycle_pmx get agents --project "uid:$lifecycle_project_uid" --window "uid:$lifecycle_window_uid" -o uid)"
if [[ -z "$lifecycle_pane_uid" || -z "$lifecycle_generation" || -z "$lifecycle_agent_uid" ]]; then
	echo "native lifecycle fixture could not resolve exact Pane/generation identity" >&2
	exit 1
fi

# Materialize a second exact Codex Agent on the same managed Window and broker
# runtime. Its thread remains live while the target binding alone is forced
# through resync, so it is the installed real-tmux sibling isolation witness.
lifecycle_sibling_pane="$(lifecycle_pmx_at_anchor create agent --provider codex --project "uid:$lifecycle_project_uid" \
  --window "uid:$lifecycle_window_uid" -o pane-id -- "phase3 sibling lifecycle smoke")"
if [[ ! "$lifecycle_sibling_pane" =~ ^%[0-9]+$ || "$lifecycle_sibling_pane" == "$lifecycle_pane" ]]; then
  echo "native lifecycle sibling create returned invalid Pane: $lifecycle_sibling_pane" >&2
  exit 1
fi
lifecycle_sibling_pane_uid="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_pane_uid)"
lifecycle_sibling_generation="$(lifecycle_pmx describe pane "uid:$lifecycle_sibling_pane_uid" | awk '$1 == "BindingGeneration:" { print $2; exit }')"
lifecycle_sibling_agent_uid="$(lifecycle_pmx get agents --project "uid:$lifecycle_project_uid" --window "uid:$lifecycle_window_uid" -o uid | grep -Fvx "$lifecycle_agent_uid")"
if [[ -z "$lifecycle_sibling_pane_uid" || -z "$lifecycle_sibling_generation" || -z "$lifecycle_sibling_agent_uid" ]] ||
  [[ "$(printf '%s\n' "$lifecycle_sibling_agent_uid" | wc -l | tr -d '[:space:]')" != "1" ]] ||
  [[ "$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_ai_thread_id 2>/dev/null || true)" != "thread-sibling" ]]; then
  echo "native lifecycle fixture could not resolve exact sibling Agent/Pane/thread identity" >&2
  exit 1
fi

lifecycle_pmx_hook() {
  env -u TMUX -u TMUX_PANE \
    PATH="$lifecycle_shim:$PATH" \
    PROJMUX_FAKE_CODEX_STATE="$lifecycle_fixture_state" \
    PROJMUX_MANAGED_ROOTS="$lifecycle_root/projects" \
    PMX_INTERNAL_ACTIVATION_PANE_UID="$lifecycle_pane_uid" \
    PMX_INTERNAL_ACTIVATION_GENERATION="$lifecycle_generation" \
    "$bin" "$@"
}

wait_lifecycle_pane_option() {
  local pane="$1" option="$2" expected="$3" label="$4" actual=""
  for _ in $(seq 1 400); do
    actual="$(lifecycle_tmux show-options -pqv -t "$pane" "$option" 2>/dev/null || true)"
    if [[ "$actual" == "$expected" ]]; then
      return 0
    fi
    sleep 0.025
  done
  echo "timed out waiting for $label: option=$option actual=$actual expected=$expected" >&2
  return 1
}

wait_lifecycle_option() {
  wait_lifecycle_pane_option "$lifecycle_pane" "$@"
}

wait_lifecycle_gate() {
  local gate="$1"
  for _ in $(seq 1 400); do
    if [[ -f "$lifecycle_fixture_state/$gate" ]]; then
      return 0
    fi
    sleep 0.025
  done
  echo "timed out waiting for fake app-server gate: $gate" >&2
  dump_lifecycle_diagnostics
  return 1
}

# Arm one pane-scoped projection barrier before the transition that must close
# it. Replacement authority follows Apply; disconnect authority precedes Apply
# but cannot match until the semantic fields are cleared. tmux runs
# after-set-option synchronously, so both signals observe the exact semantic +
# authority commit without sleeping, polling, or retrying the transition.
lifecycle_projection_barrier_serial=0
lifecycle_projection_barrier_pane=""
lifecycle_projection_barrier_channel=""
lifecycle_projection_barrier_label=""
lifecycle_arm_projection_condition() {
  local pane="$1" condition="$2" label="$3"
  lifecycle_projection_barrier_serial="$((lifecycle_projection_barrier_serial + 1))"
  lifecycle_projection_barrier_pane="$pane"
  lifecycle_projection_barrier_channel="codex-lifecycle-$lifecycle_server_pid-$lifecycle_projection_barrier_serial"
  lifecycle_projection_barrier_label="$label"
  lifecycle_tmux set-hook -p -t "$pane" after-set-option \
    "if-shell -F '$condition' 'wait-for -S $lifecycle_projection_barrier_channel'"
  # Close the lost-wakeup window without polling: if the exact level became
  # current before the hook was installed, signal the same one-shot channel
  # synchronously. Otherwise the hook owns the future edge.
  lifecycle_tmux if-shell -F -t "$pane" "$condition" \
    "wait-for -S $lifecycle_projection_barrier_channel"
}

# The attention field is part of the condition because the projection writes
# its three pane options as three separate set-option calls, and attention is
# the last of them. Gating on only the first two let the barrier release inside
# that window, so a caller that then asserts the complete five-field tuple read
# a torn projection. The barrier makes the timing deterministic; the assertions
# behind it still own the verdict, so a real attention defect surfaces there
# rather than being absorbed here.
lifecycle_arm_projection_barrier() {
  local pane="$1" source="$2" reason="$3" state="$4" badge="$5" label="$6" attention="${7-}"
  local expected="$source|$reason|$state|$badge|$attention" condition=""
  condition='#{==:#{@projmux_codex_authority}|#{@projmux_codex_authority_reason}|#{@projmux_ai_state}|#{@projmux_ai_badge_kind}|#{@projmux_attention_state},'"$expected"'}'
  lifecycle_arm_projection_condition "$pane" "$condition" "$label"
}

lifecycle_arm_replacement_projection_barrier() {
  local pane="$1" previous_epoch="$2" source="$3" reason="$4" state="$5" badge="$6" label="$7"
  local expected="$source|$reason|$state|$badge" condition=""
  # The opaque epoch is only the replaceable connection-axis witness here. Do
  # not parse it or require it to remain byte-identical after this barrier.
  condition='#{&&:#{!=:#{@projmux_codex_authority_epoch},},#{&&:#{!=:#{@projmux_codex_authority_epoch},'"$previous_epoch"'},#{==:#{@projmux_codex_authority}|#{@projmux_codex_authority_reason}|#{@projmux_ai_state}|#{@projmux_ai_badge_kind},'"$expected"'}}}'
  lifecycle_arm_projection_condition "$pane" "$condition" "$label"
}

lifecycle_wait_projection_barrier() {
  if ! env -u TMUX -u TMUX_PANE timeout 30 "$lifecycle_real_tmux" -L "$lifecycle_socket" \
    wait-for "$lifecycle_projection_barrier_channel"; then
    lifecycle_tmux set-hook -p -u -t "$lifecycle_projection_barrier_pane" after-set-option || true
    echo "timed out waiting for deterministic $lifecycle_projection_barrier_label projection barrier" >&2
    dump_lifecycle_diagnostics
    return 1
  fi
  lifecycle_tmux set-hook -p -u -t "$lifecycle_projection_barrier_pane" after-set-option
}

assert_lifecycle_field() {
  local stage="$1" field="$2" actual="$3" expected="$4"
  if [[ "$actual" != "$expected" ]]; then
    echo "$stage changed sibling $field: actual=$actual expected=$expected" >&2
    return 1
  fi
}

assert_lifecycle_sibling_semantics() {
  local stage="$1" pane_uid thread_id generation pane_ref source reason state badge
  pane_uid="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_pane_uid 2>/dev/null || true)"
  thread_id="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_ai_thread_id 2>/dev/null || true)"
  generation="$(lifecycle_pmx describe pane "uid:$lifecycle_sibling_pane_uid" | awk '$1 == "BindingGeneration:" { print $2; exit }')"
  pane_ref="$(lifecycle_pmx describe agent "uid:$lifecycle_sibling_agent_uid" | awk '$1 == "PaneRef:" { print $2; exit }')"
  source="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_codex_authority 2>/dev/null || true)"
  reason="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_codex_authority_reason 2>/dev/null || true)"
  state="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_ai_state 2>/dev/null || true)"
  badge="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_ai_badge_kind 2>/dev/null || true)"

  assert_lifecycle_field "$stage" "Pane UID" "$pane_uid" "$lifecycle_sibling_pane_uid"
  assert_lifecycle_field "$stage" "thread identity" "$thread_id" thread-sibling
  assert_lifecycle_field "$stage" "activation binding generation" "$generation" "$lifecycle_sibling_generation"
  assert_lifecycle_field "$stage" "Agent PaneRef" "$pane_ref" "$lifecycle_sibling_pane_uid"
  assert_lifecycle_field "$stage" "authority source" "$source" provider-control-plane
  assert_lifecycle_field "$stage" "authority reason" "$reason" ready
  assert_lifecycle_field "$stage" "AI state" "$state" thinking
  assert_lifecycle_field "$stage" "badge/actionability" "$badge" in_progress
}

lifecycle_queue_json() {
  lifecycle_pmx get notifications --json
}

lifecycle_queue_count() {
  local output=""
  output="$(lifecycle_queue_json)" || return
  printf '%s\n' "$output" | grep -c '^[[:space:]]*"id":' || true
}

lifecycle_desktop_count() {
  if [[ ! -f "$lifecycle_notify_log" ]]; then
    printf '0\n'
    return
  fi
  wc -l <"$lifecycle_notify_log" | tr -d '[:space:]'
}

wait_lifecycle_count() {
  local kind="$1" expected="$2" actual=""
  for _ in $(seq 1 400); do
    case "$kind" in
      queue) actual="$(lifecycle_queue_count)" ;;
      desktop) actual="$(lifecycle_desktop_count)" ;;
      *) echo "unknown lifecycle count kind: $kind" >&2; return 1 ;;
    esac
    if [[ "$actual" == "$expected" ]]; then
      return 0
    fi
    sleep 0.025
  done
  echo "timed out waiting for lifecycle $kind count: actual=$actual expected=$expected" >&2
  return 1
}

assert_lifecycle_queue_exact() {
  local expected_id="$1" expected_severity="$2" output=""
  output="$(lifecycle_queue_json)"
  if [[ "$(printf '%s\n' "$output" | grep -c '^[[:space:]]*"id":' || true)" != "1" ]] ||
    ! printf '%s\n' "$output" | grep -Fq '"id": "'"$expected_id"'"' ||
    ! printf '%s\n' "$output" | grep -Fq '"severity": "'"$expected_severity"'"'; then
    echo "unexpected isolated lifecycle queue identity/severity" >&2
    printf '%s\n' "$output" >&2
    return 1
  fi
}

dump_lifecycle_diagnostics() {
  echo "Codex lifecycle bounded diagnostics:" >&2
  lifecycle_tmux display-message -p -t "$lifecycle_pane" \
    '#{@projmux_codex_authority}|#{@projmux_codex_authority_reason}|#{@projmux_codex_authority_epoch}|#{@projmux_ai_state}|#{@projmux_ai_badge_kind}' >&2 || true
  local gate=""
  for gate in auto-approved-emitted resolved-sent waiting-completion-gate completion-gate-seen first-completion-sent duplicate-completion-sent proxy-exited disconnect allow-reconnect; do
    if [[ -f "$lifecycle_fixture_state/$gate" ]]; then
      printf 'milestone:%s=yes\n' "$gate" >&2
    else
      printf 'milestone:%s=no\n' "$gate" >&2
    fi
  done
  lifecycle_queue_json >&2 || true
  tail -n 8 "$XDG_STATE_HOME/projmux/ai-ingest.log" >&2 2>/dev/null || true
}

wait_lifecycle_option @projmux_codex_authority provider-control-plane "native authority"
wait_lifecycle_option @projmux_ai_state thinking "native active snapshot"
wait_lifecycle_pane_option "$lifecycle_sibling_pane" @projmux_codex_authority provider-control-plane "sibling native authority"
wait_lifecycle_pane_option "$lifecycle_sibling_pane" @projmux_ai_state thinking "sibling active snapshot"
epoch_one="$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" @projmux_codex_authority_epoch)"
sibling_epoch_one="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_codex_authority_epoch)"
lifecycle_control_epoch_one_pid="$(lifecycle_control_pids)"
if [[ -z "$epoch_one" || -z "$sibling_epoch_one" ]] ||
  [[ ! "$lifecycle_control_epoch_one_pid" =~ ^[1-9][0-9]*$ ]] ||
  [[ "$lifecycle_control_epoch_one_pid" != "$lifecycle_control_pid" ]]; then
  echo "native lifecycle target or sibling authority epoch was empty: target=$epoch_one sibling=$sibling_epoch_one" >&2
  echo "native lifecycle direct control peer PID was not exact: tracked=$lifecycle_control_pid actual=$lifecycle_control_epoch_one_pid" >&2
  exit 1
fi
assert_lifecycle_sibling_semantics "initial snapshot"

if [[ "$(lifecycle_queue_count)" != "0" || "$(lifecycle_desktop_count)" != "0" ]]; then
  echo "native lifecycle started with non-empty notification surfaces" >&2
  exit 1
fi

touch "$lifecycle_fixture_state/emit-auto-approved"
wait_lifecycle_gate auto-approved-emitted
sleep 0.2
if [[ "$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" @projmux_ai_badge_kind 2>/dev/null || true)" == "approval_required" ]] ||
  [[ "$(lifecycle_queue_count)" != "0" ]] || [[ "$(lifecycle_desktop_count)" != "0" ]]; then
  echo "auto-approved request projected user-visible attention" >&2
  exit 1
fi

touch "$lifecycle_fixture_state/emit-actionable"
wait_lifecycle_option @projmux_ai_badge_kind approval_required "exact actionable approval badge"
wait_lifecycle_count queue 1
wait_lifecycle_count desktop 1
assert_lifecycle_queue_exact \
  "ai:codex:native:approval:thread-phase3:turn-phase3:item-actionable:request-actionable" \
  critical

touch "$lifecycle_fixture_state/resolve-actionable"
wait_lifecycle_option @projmux_ai_badge_kind in_progress "resolved approval badge clear"
wait_lifecycle_count queue 0
if [[ "$(lifecycle_desktop_count)" != "1" ]]; then
  echo "resolved approval unexpectedly dispatched a desktop notification" >&2
  exit 1
fi
wait_lifecycle_gate resolved-sent
resolved_authority="$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" @projmux_codex_authority 2>/dev/null || true)"
resolved_epoch="$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" @projmux_codex_authority_epoch 2>/dev/null || true)"
if [[ "$resolved_authority" != "provider-control-plane" || "$resolved_epoch" != "$epoch_one" ]]; then
  echo "resolved approval did not preserve the healthy native epoch" >&2
  dump_lifecycle_diagnostics
  exit 1
fi

touch "$lifecycle_fixture_state/emit-complete"
wait_lifecycle_gate first-completion-sent
wait_lifecycle_gate duplicate-completion-sent
if ! wait_lifecycle_option @projmux_ai_badge_kind response_complete "native successful completion"; then
  dump_lifecycle_diagnostics
  exit 1
fi
wait_lifecycle_count queue 1
wait_lifecycle_count desktop 2
assert_lifecycle_queue_exact \
  "ai:codex:native:completed:thread-phase3:turn-phase3" \
  info
sleep 0.2
if [[ "$(lifecycle_queue_count)" != "1" || "$(lifecycle_desktop_count)" != "2" ]]; then
  echo "duplicate successful completion produced duplicate notification writes" >&2
  exit 1
fi

# A healthy app-server epoch owns every Codex hook. PermissionRequest would
# visibly replace this badge if the fallback lane were allowed to dual-write.
printf '%s' '{"hook_event_name":"PermissionRequest","thread_id":"thread-phase3","turn_id":"turn-phase3","cwd":"'"$lifecycle_project"'"}' |
  lifecycle_pmx_hook internal agent-hook ingest codex-hook
wait_lifecycle_option @projmux_ai_badge_kind response_complete "healthy epoch hook suppression"
if [[ "$(lifecycle_queue_count)" != "1" || "$(lifecycle_desktop_count)" != "2" ]]; then
  echo "healthy PermissionRequest hook wrote queue or desktop state" >&2
  exit 1
fi

# endpoint-suspended, not a generic disconnect: killing the exact E1 direct peer
# takes both same-process shared and owned connections away, and the broker epoch
# records which call closed its stream. Pinning that exact process identity keeps
# a reason the observer failed to capture from passing as a real disconnect.
lifecycle_arm_projection_barrier "$lifecycle_pane" invalidating endpoint-suspended "" "" "disconnect"
# The exact-peer EOF is the only disconnect trigger. The old fixture burst can
# revoke only the target binding before TERM and let E1 authority briefly return,
# so it is deliberately not armed here. Re-observe the complete unique absolute
# argv immediately before TERM and fail closed on any PID drift.
lifecycle_control_before_term="$(lifecycle_control_pids)"
if [[ "$lifecycle_control_before_term" != "$lifecycle_control_epoch_one_pid" ]] ||
  ! stop_lifecycle_control_server; then
  echo "refusing to terminate a drifted Codex lifecycle E1 control peer: pid=$lifecycle_control_epoch_one_pid actual=$lifecycle_control_before_term" >&2
  exit 1
fi
lifecycle_wait_projection_barrier

# One representative public native mutation must stop at the live-binding
# boundary throughout the reconnect gap. The CLI unit matrix owns all action
# kinds; C01 keeps the user-visible start refusal and its exact wire-zero canary.
provider_writes_before="$(wc -l <"$lifecycle_fixture_state/provider-writes" | tr -d '[:space:]')"
if lifecycle_pmx_at_anchor agent turn start "uid:$lifecycle_agent_uid" -- "gap start" \
  >"$lifecycle_root/gap-start.out" 2>"$lifecycle_root/gap-start.err"; then
  echo "reconnect gap unexpectedly accepted public start control" >&2
  exit 1
fi
provider_writes_after="$(wc -l <"$lifecycle_fixture_state/provider-writes" | tr -d '[:space:]')"
if [[ "$provider_writes_after" != "$provider_writes_before" ]]; then
	echo "reconnect gap reached the fake app-server wire: before=$provider_writes_before after=$provider_writes_after" >&2
	cat "$lifecycle_fixture_state/provider-writes" >&2
	exit 1
fi

# Native reconnect remains authoritative during the gap. The same raw hook
# event must therefore write no pane semantic, Registry interaction, queue, or
# desktop delivery and must not move the exact invalidating projection.
lifecycle_gap_before_hook="$(lifecycle_tmux display-message -p -t "$lifecycle_pane" \
  '#{@projmux_codex_authority}|#{@projmux_codex_authority_reason}|#{@projmux_ai_state}|#{@projmux_ai_badge_kind}|#{@projmux_attention_state}')"
lifecycle_gap_registry_before_hook="$(lifecycle_pmx describe agent "uid:$lifecycle_agent_uid" |
  awk '$1 == "Interaction:" || $1 == "InteractionSource:" { values = values sep $2; sep = "|" } END { print values }')"
lifecycle_gap_queue_before_hook="$(lifecycle_queue_count)"
lifecycle_gap_desktop_before_hook="$(lifecycle_desktop_count)"
printf '%s' '{"hook_event_name":"Stop","thread_id":"thread-phase3","turn_id":"turn-fallback","cwd":"'"$lifecycle_project"'"}' |
  lifecycle_pmx_hook internal agent-hook ingest codex-hook
lifecycle_gap_after_hook="$(lifecycle_tmux display-message -p -t "$lifecycle_pane" \
  '#{@projmux_codex_authority}|#{@projmux_codex_authority_reason}|#{@projmux_ai_state}|#{@projmux_ai_badge_kind}|#{@projmux_attention_state}')"
lifecycle_gap_registry_after_hook="$(lifecycle_pmx describe agent "uid:$lifecycle_agent_uid" |
  awk '$1 == "Interaction:" || $1 == "InteractionSource:" { values = values sep $2; sep = "|" } END { print values }')"
lifecycle_gap_queue_after_hook="$(lifecycle_queue_count)"
lifecycle_gap_desktop_after_hook="$(lifecycle_desktop_count)"
if [[ "$lifecycle_gap_before_hook" != "invalidating|endpoint-suspended|||" ]] ||
  [[ "$lifecycle_gap_after_hook" != "$lifecycle_gap_before_hook" ]] ||
  [[ "$lifecycle_gap_registry_after_hook" != "$lifecycle_gap_registry_before_hook" ]] ||
  [[ "$lifecycle_gap_queue_before_hook" != "1" || "$lifecycle_gap_queue_after_hook" != "$lifecycle_gap_queue_before_hook" ]] ||
  [[ "$lifecycle_gap_desktop_before_hook" != "2" || "$lifecycle_gap_desktop_after_hook" != "$lifecycle_gap_desktop_before_hook" ]]; then
  echo "Codex reconnect gap admitted provider-hook projection: pane=$lifecycle_gap_before_hook->$lifecycle_gap_after_hook Registry=$lifecycle_gap_registry_before_hook->$lifecycle_gap_registry_after_hook queue=$lifecycle_gap_queue_before_hook->$lifecycle_gap_queue_after_hook desktop=$lifecycle_gap_desktop_before_hook->$lifecycle_gap_desktop_after_hook" >&2
  exit 1
fi

lifecycle_arm_replacement_projection_barrier "$lifecycle_pane" "$epoch_one" provider-control-plane ready idle "" "replacement snapshot/control"
start_lifecycle_control_server
touch "$lifecycle_fixture_state/allow-reconnect"
lifecycle_wait_projection_barrier
epoch_two="$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" @projmux_codex_authority_epoch)"
lifecycle_control_epoch_two_pid="$(lifecycle_control_pids)"
if [[ -z "$epoch_two" || "$epoch_two" == "$epoch_one" ]] ||
  [[ ! "$lifecycle_control_epoch_two_pid" =~ ^[1-9][0-9]*$ ]] ||
  [[ "$lifecycle_control_epoch_two_pid" != "$lifecycle_control_pid" ]] ||
  [[ "$lifecycle_control_epoch_two_pid" == "$lifecycle_control_epoch_one_pid" ]]; then
  echo "reconnect did not replace lifecycle epoch: first=$epoch_one second=$epoch_two" >&2
  echo "reconnect did not replace the exact direct control peer: first=$lifecycle_control_epoch_one_pid tracked=$lifecycle_control_pid second=$lifecycle_control_epoch_two_pid" >&2
  exit 1
fi

# The broker may synchronously finish the target's replacement thread/read
# before it asks for the sibling snapshot. Arm this level-triggered barrier only
# after target recovery unblocks that request ordering. It still requires the
# sibling's opaque connection witness to advance and the exact semantic tuple
# to settle before the sole sibling control receipt.
lifecycle_arm_replacement_projection_barrier "$lifecycle_sibling_pane" "$sibling_epoch_one" provider-control-plane ready thinking in_progress "sibling replacement snapshot/control"
lifecycle_wait_projection_barrier
sibling_epoch_two="$(lifecycle_tmux show-options -pqv -t "$lifecycle_sibling_pane" @projmux_codex_authority_epoch)"
assert_lifecycle_sibling_semantics "sibling replacement barrier"
lifecycle_sibling_writes_before="$(grep -Fxc 'thread-sibling|turn/steer' "$lifecycle_fixture_state/provider-writes" || true)"
if [[ "$lifecycle_sibling_writes_before" != "0" ]]; then
  echo "sibling steer ledger was not empty before its exact reconnect receipt: writes=$lifecycle_sibling_writes_before" >&2
  exit 1
fi
if ! lifecycle_pmx_at_anchor agent turn steer "uid:$lifecycle_sibling_agent_uid" -- "sibling replacement steer" >"$lifecycle_root/sibling-replacement-steer.out" 2>"$lifecycle_root/sibling-replacement-steer.err"; then
  echo "healthy sibling native steer failed after its replacement barrier" >&2
  cat "$lifecycle_root/sibling-replacement-steer.err" >&2
  exit 1
fi
lifecycle_sibling_writes_after="$(grep -Fxc 'thread-sibling|turn/steer' "$lifecycle_fixture_state/provider-writes" || true)"
if [[ "$lifecycle_sibling_writes_after" != "1" ]]; then
  echo "sibling replacement steer did not produce the exact 0->1 thread-qualified write: before=$lifecycle_sibling_writes_before after=$lifecycle_sibling_writes_after" >&2
  exit 1
fi
assert_lifecycle_sibling_semantics "sibling replacement steer"

# Same-Agent native control is recovered directly through the exact Agent UID;
# no focus, reopen, send-keys, or fresh-Agent lane participates. Wait until both
# replacement snapshot barriers have closed before adding this target RPC to
# the shared connection, then require exactly one target-qualified write.
lifecycle_target_starts_before="$(grep -Fxc 'thread-phase3|turn/start' "$lifecycle_fixture_state/provider-writes" || true)"
if ! lifecycle_pmx_at_anchor agent turn start "uid:$lifecycle_agent_uid" -- "target replacement exact start" >"$lifecycle_root/target-e2-start.out" 2>"$lifecycle_root/target-e2-start.err"; then
  echo "target same-Agent exact native start failed after replacement epoch" >&2
  cat "$lifecycle_root/target-e2-start.err" >&2
  exit 1
fi
lifecycle_target_starts_after="$(grep -Fxc 'thread-phase3|turn/start' "$lifecycle_fixture_state/provider-writes" || true)"
if [[ "$lifecycle_target_starts_after" != "$((lifecycle_target_starts_before + 1))" ]]; then
  echo "target E2 start did not produce one target-qualified provider write: before=$lifecycle_target_starts_before after=$lifecycle_target_starts_after" >&2
  exit 1
fi

if [[ -f "$lifecycle_fixture_state/fixture-error" ]]; then
  echo "fake Codex two-Agent fixture reported an asynchronous failure" >&2
  cat "$lifecycle_fixture_state/fixture-error" >&2
  exit 1
fi

lifecycle_cleanup
trap smoke_cleanup_env EXIT
echo ">> Codex native lifecycle E2E passed: socket=$lifecycle_socket path=$lifecycle_socket_path target-epochs=$epoch_one,$epoch_two sibling-epochs=$sibling_epoch_one,$sibling_epoch_two sibling-steer-writes=0,1"
smoke_contract_pass
