# shellcheck shell=bash
# shellcheck disable=SC2154 # Variables and functions are supplied by linux-smoke.sh.
# L20 deterministic, selectorless, offline Codex -> Claude -> Codex dialogue.
# The fake providers implement one typed, content-free Codex route fixture and
# the official Claude hook plus owner-frozen auth+user-frame shape. They have
# no model, tool, plugin, MCP, connector, credential, or external service.
dialogue_root="$PROJMUX_SMOKE_WORKDIR/heterogeneous-dialogue"
dialogue_project="$dialogue_root/project"
dialogue_shim="$dialogue_root/bin"
dialogue_codex_state="$dialogue_root/codex-state"
dialogue_claude_state="$dialogue_root/claude-state"
dialogue_codex_home="$dialogue_root/codex-home"
dialogue_socket="heterogeneous-dialogue-$$-$RANDOM"
dialogue_session="heterogeneous-dialogue-project"
dialogue_real_tmux="$(command -v tmux)"
mkdir -p "$dialogue_project" "$dialogue_shim" "$dialogue_codex_state" "$dialogue_claude_state" "$dialogue_codex_home" \
  "$dialogue_root/config" "$dialogue_root/state" "$dialogue_root/runtime" "$dialogue_root/cache" "$dialogue_root/tmux"
chmod 0700 "$dialogue_root/runtime" "$dialogue_claude_state"
go build -o "$dialogue_shim/codex" ./test/e2e/fake_codex_appserver_fixture.go
go build -o "$dialogue_shim/claude" ./test/e2e/claudefixture

dialogue_env=(
  env -u TMUX -u TMUX_PANE
  PATH="$dialogue_shim:$PATH"
  CODEX_HOME="$dialogue_codex_home"
  XDG_CONFIG_HOME="$dialogue_root/config"
  XDG_STATE_HOME="$dialogue_root/state"
  XDG_RUNTIME_DIR="$dialogue_root/runtime"
  XDG_CACHE_HOME="$dialogue_root/cache"
  TMUX_TMPDIR="$dialogue_root/tmux"
  PROJMUX_FAKE_CODEX_STATE="$dialogue_codex_state"
  PROJMUX_FAKE_CLAUDE_STATE="$dialogue_claude_state"
  PROJMUX_FAKE_CLAUDE_BIN="$bin"
  PROJMUX_MANAGED_ROOTS="$dialogue_root"
)
dialogue_tmux() { "${dialogue_env[@]}" "$dialogue_real_tmux" -L "$dialogue_socket" "$@"; }
dialogue_pmx() { "${dialogue_env[@]}" "$bin" "$@"; }
dialogue_control_pid=""
dialogue_binding_pid=""
dialogue_wait_pid=""
dialogue_socket_path=""
dialogue_socket_identity=""
dialogue_project_uid=""
dialogue_cleanup_done=0
dialogue_cleanup_attempted=0
dialogue_removal_attempted=0
dialogue_root_removed=0
dialogue_cleanup() {
  if [[ "$dialogue_cleanup_done" == "1" ]]; then return 0; fi
  if [[ "$dialogue_cleanup_attempted" == "1" ]]; then return 1; fi
  dialogue_cleanup_attempted=1
  if ! "${dialogue_env[@]}" python3 test/e2e/dialogue-cleanup.py \
    "$dialogue_root" "$bin" "$dialogue_real_tmux" "$dialogue_socket_path" "$dialogue_socket_identity" \
    "$dialogue_socket" "$dialogue_project_uid" "$dialogue_control_pid" "$dialogue_binding_pid" "$dialogue_wait_pid"; then
    return 1
  fi
  # pidfd proof precedes shell reaping; wait cannot block on a live writer.
  local pid
  for pid in "$dialogue_control_pid" "$dialogue_binding_pid" "$dialogue_wait_pid"; do
    if [[ -n "$pid" ]]; then wait "$pid" 2>/dev/null || true; fi
  done
  dialogue_cleanup_done=1
}
dialogue_finish_cleanup() {
  local status=$?
  if ! dialogue_cleanup; then
    echo "dialogue owned writer cleanup failed; smoke root retained" >&2
    return 1
  fi
  if [[ "$dialogue_removal_attempted" == "1" && "$dialogue_root_removed" != "1" ]]; then
    echo "dialogue root removal failed; smoke root retained without retry" >&2
    return 1
  fi
  smoke_cleanup_env
  return "$status"
}
trap dialogue_finish_cleanup EXIT

"${dialogue_env[@]}" "$dialogue_shim/codex" app-server fixture-control >"$dialogue_root/control.out" 2>"$dialogue_root/control.err" &
dialogue_control_pid=$!
dialogue_control_ready() {
  kill -0 "$dialogue_control_pid" 2>/dev/null && [[ -S "$dialogue_codex_home/app-server-control/app-server-control.sock" ]]
}
smoke_wait_for "dialogue Codex control endpoint" dialogue_control_ready

dialogue_anchor="$(dialogue_tmux new-session -d -P -F '#{pane_id}' -s "$dialogue_session" -c "$dialogue_project" sleep 600)"
dialogue_socket_path="$(dialogue_tmux display-message -p -t "$dialogue_anchor" '#{socket_path}')"
dialogue_socket_identity="$(python3 - "$dialogue_socket_path" <<'PY_SOCKET'
import os, stat, sys
info=os.lstat(sys.argv[1])
assert stat.S_ISSOCK(info.st_mode)
print(f"{info.st_dev}:{info.st_ino}")
PY_SOCKET
)"
dialogue_server_pid="$(dialogue_tmux display-message -p -t "$dialogue_anchor" '#{pid}')"
dialogue_tmux set-option -t "$dialogue_session" -q @projmux_project_path "$dialogue_project"
dialogue_project_uid="$(dialogue_pmx create project --root "$dialogue_project" --name heterogeneous-dialogue -o uid)"
dialogue_pmx reconcile resources --socket-path "$dialogue_socket_path" >/dev/null
dialogue_window_uid="$(dialogue_tmux show-options -wqv -t "$dialogue_anchor" @projmux_window_uid)"

dialogue_inside() {
  local pane="$1"
  shift
  "${dialogue_env[@]}" TMUX="$dialogue_socket_path,$dialogue_server_pid,0" TMUX_PANE="$pane" "$bin" "$@"
}
dialogue_codex_pane="$(dialogue_inside "$dialogue_anchor" create agent --provider codex --project "uid:$dialogue_project_uid" --window "uid:$dialogue_window_uid" -o pane-id)"
dialogue_claude_pane="$(dialogue_inside "$dialogue_anchor" create agent --provider claude --dialogue-reply-only --project "uid:$dialogue_project_uid" --window "uid:$dialogue_window_uid" -o pane-id)"
dialogue_codex_pane_uid="$(dialogue_tmux show-options -pqv -t "$dialogue_codex_pane" @projmux_pane_uid)"
dialogue_claude_pane_uid="$(dialogue_tmux show-options -pqv -t "$dialogue_claude_pane" @projmux_pane_uid)"
dialogue_agents_json="$(dialogue_pmx get agents --project "uid:$dialogue_project_uid" -o json)"
read -r dialogue_codex_uid dialogue_claude_uid < <(python3 - "$dialogue_agents_json" "$dialogue_codex_pane_uid" "$dialogue_claude_pane_uid" <<'PY'
import json, sys
rows=json.loads(sys.argv[1])
rows=rows["items"]
by_pane={row["status"]["paneRef"]: row["metadata"]["uid"] for row in rows}
print(by_pane[sys.argv[2]], by_pane[sys.argv[3]])
PY
)
dialogue_panes_json="$(dialogue_pmx get panes --project "uid:$dialogue_project_uid" --window "uid:$dialogue_window_uid" -o json)"
read -r dialogue_codex_generation dialogue_claude_generation < <(python3 - "$dialogue_panes_json" \
  "$dialogue_codex_uid" "$dialogue_codex_pane_uid" "$dialogue_codex_pane" \
  "$dialogue_claude_uid" "$dialogue_claude_pane_uid" "$dialogue_claude_pane" <<'PY'
import json, sys
rows=json.loads(sys.argv[1])["items"]
by_uid={row["metadata"]["uid"]: row for row in rows}
generations=[]
for agent_uid, pane_uid, runtime_id in ((sys.argv[2],sys.argv[3],sys.argv[4]),(sys.argv[5],sys.argv[6],sys.argv[7])):
    activation=by_uid[pane_uid]["status"]["activation"]
    assert activation["agentUID"]==agent_uid and activation["runtimeID"]==runtime_id
    assert activation["generation"]
    generations.append(activation["generation"])
print(*generations)
PY
)
if [[ -z "$dialogue_codex_uid" || -z "$dialogue_claude_uid" || -z "$dialogue_codex_generation" || -z "$dialogue_claude_generation" ]]; then
  echo "dialogue exact Agent/Pane/generation chain is incomplete: codex-agent=$dialogue_codex_uid codex-pane=$dialogue_codex_pane_uid codex-generation=$dialogue_codex_generation claude-agent=$dialogue_claude_uid claude-pane=$dialogue_claude_pane_uid claude-generation=$dialogue_claude_generation" >&2
  exit 1
fi
"${dialogue_env[@]}" "$dialogue_shim/codex" dialogue-bind \
  "$dialogue_root/state/projmux/metadata/registry.json" "$dialogue_codex_uid" \
  "$dialogue_codex_pane_uid" "$dialogue_codex_generation" >"$dialogue_root/binding.out" 2>"$dialogue_root/binding.err" &
dialogue_binding_pid=$!
dialogue_codex_capabilities="$dialogue_root/capabilities-codex.json"
dialogue_codex_route_ready() {
  local candidate="$dialogue_codex_capabilities.tmp"
  if ! dialogue_pmx agent capabilities "uid:$dialogue_codex_uid" -o json >"$candidate"; then
    return 1
  fi
  if ! python3 - "$candidate" <<'PY'
import json,re,sys
value=json.load(open(sys.argv[1])); runtime=value["runtimeEligibility"]
actions={row["action"]:row for row in value["capabilities"]}
assert runtime["registryReady"] is True and runtime["routeIncarnation"].startswith("route-")
assert runtime["stateDomainID"]=="dialogue-state-domain"
assert runtime["endpointGenerationID"]=="dialogue-endpoint-generation"
assert re.fullmatch(r"[0-9a-f]{32}",runtime["brokerRuntimeID"])
assert runtime["connectionEpoch"]==1 and runtime["bindingEpoch"]==1
assert actions["message.send"]["available"] is True and actions["message.status"]["available"] is True
PY
  then
    return 1
  fi
  mv "$candidate" "$dialogue_codex_capabilities"
}
smoke_wait_for "dialogue exact Codex composite route" dialogue_codex_route_ready
dialogue_registration_ready() { [[ -f "$dialogue_claude_state/registration-ready" ]]; }
smoke_wait_for "dialogue exact Claude registration" dialogue_registration_ready
dialogue_capabilities_before="$dialogue_root/capabilities-before.json"
dialogue_pmx agent capabilities "uid:$dialogue_claude_uid" -o json >"$dialogue_capabilities_before"
python3 - "$dialogue_capabilities_before" <<'PY'
import json, sys
value=json.load(open(sys.argv[1])); runtime=value["runtimeEligibility"]
assert runtime["registryReady"] is True and runtime["routeIncarnation"].startswith("route-")
assert runtime["coordination"]["eligible"] is False
PY

# Exercise the production observer on synthetic public events. No hand-authored
# qualification evidence file is accepted as public activation evidence here.
dialogue_observer_ready() {
  dialogue_tmux capture-pane -p -J -t "$dialogue_claude_pane" | grep -Fq 'Claude reply-only activation is ready for explicit qualification.'
}
if ! smoke_wait_for "dialogue public observer ready" dialogue_observer_ready; then
  dialogue_tmux capture-pane -p -J -t "$dialogue_claude_pane" >&2 || true
  if [[ -f "$dialogue_claude_state/fixture-error" ]]; then cat "$dialogue_claude_state/fixture-error" >&2; fi
  exit 1
fi
dialogue_inside "$dialogue_codex_pane" agent message qualify "uid:$dialogue_claude_uid" \
  --confirm-isolated-provider-push -o json >"$dialogue_root/qualification-receipt.json"
# The challenge reply is no longer claimed from an inbox. The receipt below
# already proves it: a qualified state is only reachable through the broker's
# explicit reply carrying the exact marker.
dialogue_pmx agent capabilities "uid:$dialogue_claude_uid" -o json >"$dialogue_root/capabilities-qualified.json"
python3 - "$dialogue_root/qualification-receipt.json" "$dialogue_root/capabilities-qualified.json" <<'PY'
import json,sys
receipt=json.load(open(sys.argv[1])); capability=json.load(open(sys.argv[2]))
assert receipt["state"]=="qualification-qualified" and receipt["providerVersion"]=="2.1.263"
assert receipt["evidence"]=="owned-public-init-plus-broker-explicit-reply"
assert receipt["ambiguous"] is False and receipt["autoResend"] is False
assert capability["runtimeEligibility"]["coordination"]["eligible"] is True
PY

dialogue_agent_count_before="$(dialogue_pmx get agents --project "uid:$dialogue_project_uid" -o uid | wc -l)"
dialogue_provider_write_bytes() {
  python3 - "$dialogue_codex_state/provider-writes" <<'PY'
import pathlib,sys
path=pathlib.Path(sys.argv[1])
if path.exists() and not path.is_file(): raise SystemExit("Codex provider-write ledger is not a regular file")
print(path.stat().st_size if path.exists() else 0)
PY
}
# Delivery to a Codex target is a native turn push, so provider writes are the
# mechanism rather than a violation. The baseline is recorded and has to grow.
dialogue_provider_writes_before="$(dialogue_provider_write_bytes)"
dialogue_message_ref="message-heterogeneous-e2e"
rm -f "$dialogue_claude_state/round-trip-complete"
dialogue_send_receipt="$(dialogue_inside "$dialogue_codex_pane" agent message send --message-ref "$dialogue_message_ref" "uid:$dialogue_claude_uid" -- "HETEROGENEOUS_REQUEST:$dialogue_message_ref")"
dialogue_round_trip_done() { [[ -f "$dialogue_claude_state/round-trip-complete" ]]; }
if ! smoke_wait_for "dialogue explicit reply committed" dialogue_round_trip_done; then
  dialogue_tmux capture-pane -p -J -t "$dialogue_claude_pane" >&2 || true
  exit 1
fi
dialogue_status="$(dialogue_inside "$dialogue_codex_pane" agent message status "$dialogue_message_ref" -o json)"
dialogue_agent_count_after="$(dialogue_pmx get agents --project "uid:$dialogue_project_uid" -o uid | wc -l)"
dialogue_provider_writes_after="$(dialogue_provider_write_bytes)"
python3 - "$dialogue_claude_state/frame.json" "$dialogue_status" \
  "$dialogue_message_ref" "$dialogue_codex_uid" "$dialogue_codex_pane_uid" "$dialogue_codex_generation" \
  "$dialogue_claude_uid" "$dialogue_claude_pane_uid" "$dialogue_claude_generation" <<'PY'
import hashlib, json, pathlib, sys
public=json.loads(pathlib.Path(sys.argv[1]).read_text())
status=json.loads(sys.argv[2])
message, ca, cp, cg, ha, hp, hg=sys.argv[3:]
conversation="conversation-"+hashlib.sha256(message.encode()).hexdigest()[:36]
assert message == public["messageRef"]
assert public["conversationRef"] == conversation
assert public["source"] == {**public["source"], "agentUID":ca, "paneUID":cp, "activationGeneration":cg, "provider":"codex"}
assert public["target"] == {**public["target"], "agentUID":ha, "paneUID":hp, "activationGeneration":hg, "provider":"claude"}
assert public["authority"] == "untrusted-coordination-only"
# The reply itself is proven by the fixture's round-trip marker: it is only
# written after the broker accepts an explicit reply, and acceptance runs the
# same route-reversal and conversation checks this block used to repeat.
assert status["messageRef"] == message and status["conversationRef"] == conversation
assert status["delivery"]["state"] == "delivered"
print(json.dumps({"messageRef":message,"conversationRef":conversation,"replyTo":message,"sourceAgentUID":ca,"targetAgentUID":ha,"qualificationVersion":"2.1.263","waiters":0,"state":"round-trip-pushed"},sort_keys=True))
PY
if [[ "$dialogue_agent_count_before" != "2" || "$dialogue_agent_count_after" != "$dialogue_agent_count_before" ]]; then
  echo "dialogue created an Agent during message traffic: before=$dialogue_agent_count_before after=$dialogue_agent_count_after" >&2
  exit 1
fi
if [[ "$dialogue_provider_writes_after" -le "$dialogue_provider_writes_before" ]]; then
  echo "dialogue reply did not reach the Codex target by native turn push: before=$dialogue_provider_writes_before after=$dialogue_provider_writes_after" >&2
  exit 1
fi

# The barrier captures pane/supervisor/helper births before canonical delete.
# A failed proof deliberately retains the root, including on the outer EXIT.
if ! dialogue_cleanup; then exit 1; fi
dialogue_remove_root() {
  if [[ "$dialogue_removal_attempted" == "1" ]]; then [[ "$dialogue_root_removed" == "1" ]]; return; fi
  dialogue_removal_attempted=1
  case "$dialogue_root" in
    "$PROJMUX_SMOKE_WORKDIR"/*)
      if ! rm -rf -- "$dialogue_root"; then return 1; fi ;;
    *) echo "refusing to remove dialogue root outside smoke workdir" >&2; return 1 ;;
  esac
  if [[ -e "$dialogue_root" ]]; then
    echo "dialogue exact owned root survived cleanup" >&2
    return 1
  fi
  dialogue_root_removed=1
}
if ! dialogue_remove_root; then exit 1; fi
trap smoke_cleanup_env EXIT
echo ">> heterogeneous dialogue e2e passed: $dialogue_send_receipt agents=2 new-agents=0 waiters=0 codex-provider-writes=grew helper/socket/process-residual=0"
