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
lifecycle_notify_log="$lifecycle_root/desktop-notify-count"
lifecycle_notify_hook="$lifecycle_root/desktop-notify-hook"
lifecycle_real_tmux="$(command -v tmux)"
lifecycle_started=0
mkdir -p "$lifecycle_project" "$lifecycle_shim" "$lifecycle_fixture_state"

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
  smoke_assert_file_contains "$output" '"lifecycle_outcome": "not-attempted"'
  smoke_assert_file_contains "$output" '"lifecycle_reason": "read-only"'
}

lifecycle_topology_doctor external-ready "$lifecycle_topology_ready_shim" external-cli-only app-server available none none ready
lifecycle_topology_doctor managed-ready "$lifecycle_topology_ready_shim" managed-ready app-server available none none ready
lifecycle_topology_doctor endpoint-missing "$lifecycle_topology_missing_shim" external-cli-only unavailable unavailable hook-unavailable daemon-not-running disconnected

if grep -Fq 'daemon start' "$lifecycle_topology_invocations" ||
  [[ "$(grep -Fxc 'app-server proxy' "$lifecycle_topology_invocations" || true)" != "3" ]]; then
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
  ps -eo pid=,args= | PROJMUX_ROUTE_BIN="$bin" awk '
    index($0, ENVIRON["PROJMUX_ROUTE_BIN"] " internal agent-hook ingest codex-appserver-watch") > 0 { print }'
}

lifecycle_cleanup() {
  local actual=""
  if [[ "$lifecycle_started" == "1" ]]; then
    actual="$(lifecycle_tmux display-message -p -t "$lifecycle_session" '#{socket_path}' 2>/dev/null || true)"
    if [[ -n "$actual" ]]; then
      case "$actual" in
        "$PROJMUX_SMOKE_TMUX_ROOT"/*)
          env -u TMUX -u TMUX_PANE "$lifecycle_real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
          ;;
        *)
          echo "refusing Codex lifecycle cleanup outside smoke root: $actual" >&2
          return 1
          ;;
      esac
    fi
    lifecycle_started=0
  fi
  for _ in $(seq 1 200); do
    if [[ -z "$(lifecycle_background_routes)" ]]; then
      return 0
    fi
    sleep 0.025
  done
  echo "Codex lifecycle watcher survived exact server cleanup:" >&2
  lifecycle_background_routes >&2
  return 1
}
trap 'lifecycle_cleanup; smoke_cleanup_env' EXIT

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
if [[ -z "$lifecycle_pane_uid" || -z "$lifecycle_generation" ]]; then
  echo "native lifecycle fixture could not resolve exact Pane/generation identity" >&2
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

wait_lifecycle_option() {
  local option="$1" expected="$2" label="$3" actual=""
  for _ in $(seq 1 400); do
    actual="$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" "$option" 2>/dev/null || true)"
    if [[ "$actual" == "$expected" ]]; then
      return 0
    fi
    sleep 0.025
  done
  echo "timed out waiting for $label: option=$option actual=$actual expected=$expected" >&2
  return 1
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
epoch_one="$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" @projmux_codex_authority_epoch)"
if [[ -z "$epoch_one" ]]; then
  echo "native lifecycle epoch was empty" >&2
  exit 1
fi

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

touch "$lifecycle_fixture_state/disconnect"
wait_lifecycle_option @projmux_codex_authority provider-hook "ordered disconnect fallback"
wait_lifecycle_option @projmux_ai_badge_kind "" "disconnect stale badge clear"

# Only after the invalidation clear made provider-hook current may the same raw
# hook path project a badge again.
printf '%s' '{"hook_event_name":"Stop","thread_id":"thread-phase3","turn_id":"turn-fallback","cwd":"'"$lifecycle_project"'"}' |
  lifecycle_pmx_hook internal agent-hook ingest codex-hook
if ! wait_lifecycle_option @projmux_ai_badge_kind response_complete "hook fallback activation"; then
  echo "Codex fallback ingest diagnostics:" >&2
  tail -n 8 "$XDG_STATE_HOME/projmux/ai-ingest.log" >&2 || true
  lifecycle_tmux display-message -p -t "$lifecycle_pane" \
    '#{@projmux_pane_uid}|#{@projmux_ai_thread_id}|#{@projmux_codex_authority}|#{@projmux_codex_authority_epoch}|#{@projmux_codex_authority_reason}' >&2 || true
  exit 1
fi

touch "$lifecycle_fixture_state/allow-reconnect"
wait_lifecycle_option @projmux_codex_authority provider-control-plane "reconnected native authority"
wait_lifecycle_option @projmux_ai_state idle "reconnect snapshot convergence"
wait_lifecycle_option @projmux_ai_badge_kind "" "reconnect stale fallback clear"
epoch_two="$(lifecycle_tmux show-options -pqv -t "$lifecycle_pane" @projmux_codex_authority_epoch)"
if [[ -z "$epoch_two" || "$epoch_two" == "$epoch_one" ]]; then
  echo "reconnect did not replace lifecycle epoch: first=$epoch_one second=$epoch_two" >&2
  exit 1
fi

lifecycle_cleanup
trap smoke_cleanup_env EXIT
echo ">> Codex native lifecycle E2E passed: socket=$lifecycle_socket path=$lifecycle_socket_path epochs=$epoch_one,$epoch_two"
smoke_contract_pass
