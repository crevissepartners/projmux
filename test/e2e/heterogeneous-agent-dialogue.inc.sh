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
dialogue_cleanup_done=0
dialogue_stop_owned_broker() {
  python3 - "$bin" "$dialogue_root/state/projmux" <<'PY'
import os, pathlib, signal, sys
binary=pathlib.Path(sys.argv[1]).resolve(strict=True); domain=str(pathlib.Path(sys.argv[2]).resolve())
matches=[]
for proc in pathlib.Path("/proc").iterdir():
    if not proc.name.isdigit(): continue
    try:
        argv=proc.joinpath("cmdline").read_bytes().split(b"\0")
        argv=[x.decode() for x in argv if x]
        exe=proc.joinpath("exe").resolve(strict=True)
    except (FileNotFoundError,PermissionError,ProcessLookupError,UnicodeDecodeError):
        continue
    if exe != binary or len(argv)<6 or argv[1:5] != ["internal","codex-broker","serve","--state-domain"] or argv[5] != domain:
        continue
    matches.append(int(proc.name))
if len(matches)>1: raise SystemExit("more than one exact owned Codex broker matched")
if matches:
    os.kill(matches[0],signal.SIGTERM)
    print(matches[0])
PY
}
dialogue_cleanup() {
  if [[ "$dialogue_cleanup_done" == "1" ]]; then return; fi
  dialogue_cleanup_done=1
  if [[ -n "$dialogue_wait_pid" ]]; then
    if jobs -pr | grep -Fxq "$dialogue_wait_pid"; then
      kill -TERM "$dialogue_wait_pid" 2>/dev/null || true
    fi
    wait "$dialogue_wait_pid" 2>/dev/null || true
    dialogue_wait_pid=""
  fi
  if [[ -n "$dialogue_socket_path" ]]; then
    case "$dialogue_socket_path" in
      "$dialogue_root"/tmux/*) "${dialogue_env[@]}" "$dialogue_real_tmux" -S "$dialogue_socket_path" kill-server >/dev/null 2>&1 || true ;;
      *) echo "refusing dialogue cleanup outside smoke root: $dialogue_socket_path" >&2; return 1 ;;
    esac
  fi
  if [[ -n "$dialogue_control_pid" ]] && kill -0 "$dialogue_control_pid" 2>/dev/null; then
    kill -TERM "$dialogue_control_pid"
    wait "$dialogue_control_pid" 2>/dev/null || true
  fi
  if [[ -n "$dialogue_binding_pid" ]]; then
    if jobs -pr | grep -Fxq "$dialogue_binding_pid"; then kill -TERM "$dialogue_binding_pid"; fi
    wait "$dialogue_binding_pid" || true
    dialogue_binding_pid=""
  fi
  dialogue_stop_owned_broker >/dev/null
}
trap 'dialogue_cleanup; smoke_cleanup_env' EXIT

"${dialogue_env[@]}" "$dialogue_shim/codex" app-server fixture-control >"$dialogue_root/control.out" 2>"$dialogue_root/control.err" &
dialogue_control_pid=$!
dialogue_control_ready() {
  kill -0 "$dialogue_control_pid" 2>/dev/null && [[ -S "$dialogue_codex_home/app-server-control/app-server-control.sock" ]]
}
smoke_wait_for "dialogue Codex control endpoint" dialogue_control_ready

dialogue_anchor="$(dialogue_tmux new-session -d -P -F '#{pane_id}' -s "$dialogue_session" -c "$dialogue_project" sleep 600)"
dialogue_socket_path="$(dialogue_tmux display-message -p -t "$dialogue_anchor" '#{socket_path}')"
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
dialogue_claude_pane="$(dialogue_inside "$dialogue_anchor" create agent --provider claude --project "uid:$dialogue_project_uid" --window "uid:$dialogue_window_uid" -o pane-id)"
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
assert actions["message.send"]["available"] is True and actions["message.wait"]["available"] is True
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

# L20's deterministic collector synthesizes the exact sanitized public-init
# facts for the offline fixture only. The product still validates every exact
# route/process/helper field and requires the unique Stop marker before opening
# helper-memory eligibility. No provider socket/token is persisted here.
dialogue_qualification_evidence="$dialogue_root/qualification-evidence.json"
python3 - "$dialogue_root/state/projmux/metadata/registry.json" "$dialogue_capabilities_before" \
  "$dialogue_claude_uid" "$dialogue_claude_pane_uid" "$dialogue_claude_generation" "$dialogue_qualification_evidence" <<'PY'
import datetime,json,os,pathlib,sys
registry=json.load(open(sys.argv[1])); capability=json.load(open(sys.argv[2])); agent_uid,pane_uid,generation,out=sys.argv[3:]
pane=next(x for x in registry["panes"] if x["metadata"]["uid"]==pane_uid)
authority=pane["status"]["activation"]["claude"]["registration"]["authority"]
evidence={"version":1,"claude_code_version":"2.1.263","sessionId":authority["sessionId"],
 "agentUID":agent_uid,"paneUID":pane_uid,"activationGeneration":generation,
 "routeIncarnation":capability["runtimeEligibility"]["routeIncarnation"],
 "providerProcess":authority["process"],"registrationGeneration":authority["registrationGeneration"],
 "helperProcess":authority["leaseProcess"],"tools":[],"mcp_servers":[],"plugins":[],
 "pluginInitCount":0,"preMarkerToolUse":0,"preMarkerStderr":0,"inboundPolicy":"accept",
 "publicInitObserved":True,"streamFrozen":True,
 "observedAt":datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00","Z")}
temporary=out+".tmp"
with open(temporary,"x") as f: json.dump(evidence,f,separators=(",",":")); f.write("\n")
os.chmod(temporary,0o600); os.replace(temporary,out)
PY
dialogue_pmx agent message qualify "uid:$dialogue_claude_uid" --evidence "$dialogue_qualification_evidence" \
  --confirm-isolated-provider-push -o json >"$dialogue_root/qualification-receipt.json"
dialogue_pmx agent capabilities "uid:$dialogue_claude_uid" -o json >"$dialogue_root/capabilities-qualified.json"
python3 - "$dialogue_root/qualification-receipt.json" "$dialogue_root/capabilities-qualified.json" <<'PY'
import json,sys
receipt=json.load(open(sys.argv[1])); capability=json.load(open(sys.argv[2]))
assert receipt["state"]=="qualification-qualified" and receipt["providerVersion"]=="2.1.263"
assert receipt["evidence"]=="owned-public-init-plus-exact-stop-marker"
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
dialogue_provider_writes_before="$(dialogue_provider_write_bytes)"
if [[ "$dialogue_provider_writes_before" != "0" ]]; then
  echo "dialogue fixture observed a Codex provider write before message traffic" >&2
  exit 1
fi
dialogue_message_ref="message-heterogeneous-e2e"
dialogue_inside "$dialogue_codex_pane" agent message wait "uid:$dialogue_codex_uid" --timeout 30s -o json >"$dialogue_root/reply.json" &
dialogue_wait_pid=$!
dialogue_send_receipt="$(dialogue_inside "$dialogue_codex_pane" agent message send --message-ref "$dialogue_message_ref" "uid:$dialogue_claude_uid" -- "HETEROGENEOUS_REQUEST:$dialogue_message_ref")"
wait "$dialogue_wait_pid"
dialogue_wait_pid=""
dialogue_status="$(dialogue_inside "$dialogue_codex_pane" agent message status "$dialogue_message_ref" -o json)"
dialogue_agent_count_after="$(dialogue_pmx get agents --project "uid:$dialogue_project_uid" -o uid | wc -l)"
dialogue_provider_writes_after="$(dialogue_provider_write_bytes)"
python3 - "$dialogue_claude_state/frame.json" "$dialogue_root/reply.json" "$dialogue_status" \
  "$dialogue_message_ref" "$dialogue_codex_uid" "$dialogue_codex_pane_uid" "$dialogue_codex_generation" \
  "$dialogue_claude_uid" "$dialogue_claude_pane_uid" "$dialogue_claude_generation" <<'PY'
import hashlib, json, pathlib, sys
public=json.loads(pathlib.Path(sys.argv[1]).read_text())
reply=json.loads(pathlib.Path(sys.argv[2]).read_text())
status=json.loads(sys.argv[3])
message, ca, cp, cg, ha, hp, hg=sys.argv[4:]
conversation="conversation-"+hashlib.sha256(message.encode()).hexdigest()[:36]
assert message == public["messageRef"]
assert public["conversationRef"] == conversation
assert public["source"] == {**public["source"], "agentUID":ca, "paneUID":cp, "activationGeneration":cg, "provider":"codex"}
assert public["target"] == {**public["target"], "agentUID":ha, "paneUID":hp, "activationGeneration":hg, "provider":"claude"}
assert public["authority"] == "untrusted-coordination-only"
envelope=reply["envelope"]
assert envelope["replyTo"] == message and envelope["conversationRef"] == conversation
assert envelope["source"] == public["target"] and envelope["target"] == public["source"]
assert envelope["authority"] == {"kind":"peer","trust":"untrusted","permission":"coordination-only"}
assert envelope["payload"] == "HETEROGENEOUS_REPLY:"+message
assert reply["delivery"]["state"] == "delivered" and reply["delivery"]["reason"] == "target-self-claim"
assert status["messageRef"] == message and status["conversationRef"] == conversation
assert status["delivery"]["state"] == "delivered"
print(json.dumps({"messageRef":message,"conversationRef":conversation,"replyTo":message,"sourceAgentUID":ca,"targetAgentUID":ha,"qualificationVersion":"2.1.263","waiters":0,"state":"round-trip-self-claimed"},sort_keys=True))
PY
if [[ "$dialogue_agent_count_before" != "2" || "$dialogue_agent_count_after" != "$dialogue_agent_count_before" ]]; then
  echo "dialogue created an Agent during message traffic: before=$dialogue_agent_count_before after=$dialogue_agent_count_after" >&2
  exit 1
fi
if [[ "$dialogue_provider_writes_before" != "0" || "$dialogue_provider_writes_after" != "0" ]]; then
  echo "dialogue used a Codex app-server/user-turn/model-history write" >&2
  exit 1
fi

dialogue_pmx delete project "uid:$dialogue_project_uid" --socket "$dialogue_socket" --yes >"$dialogue_root/delete.out"
if [[ -n "$(dialogue_pmx get projects -o uid)" || -n "$(dialogue_pmx get agents --all-projects -o uid)" ]]; then
  echo "dialogue canonical cleanup left owned Registry resources" >&2
  exit 1
fi
python3 - "$dialogue_root/state/projmux/metadata/registry.json" <<'PY'
import json, pathlib, sys
p=pathlib.Path(sys.argv[1])
if p.exists():
    registry=json.loads(p.read_text())
    for collection in ("projects","controlSessions","windows","panes","agents","nameReservations"):
        assert not registry.get(collection), "owned Registry residue in "+collection
PY
dialogue_cleanup
dialogue_processes_gone() {
  # shellcheck disable=SC2009 # Match the literal owned root, not a regex.
  ! ps -eo args= | grep -F "$dialogue_root" | grep -v grep >/dev/null
}
if ! smoke_wait_for "dialogue owned process cleanup" dialogue_processes_gone; then
  echo "dialogue exact owned process residue:" >&2
  # shellcheck disable=SC2009 # Diagnostic preserves PID/state for this literal root.
  ps -eo pid=,ppid=,state=,args= | grep -F "$dialogue_root" | grep -v grep >&2 || true
  exit 1
fi
case "$dialogue_socket_path" in
  "$dialogue_root"/tmux/*)
    if [[ -e "$dialogue_socket_path" && ! -S "$dialogue_socket_path" ]]; then
      echo "dialogue tmux socket path was replaced by a non-socket" >&2
      exit 1
    fi
    if [[ -S "$dialogue_socket_path" ]]; then rm -f -- "$dialogue_socket_path"; fi
    ;;
  *) echo "refusing stale dialogue tmux socket cleanup outside owned root: $dialogue_socket_path" >&2; exit 1 ;;
esac
if dialogue_tmux list-sessions >/dev/null 2>&1; then
  echo "dialogue cleanup left an owned tmux server/session" >&2
  exit 1
fi
if find "$dialogue_root" -type s -print -quit 2>/dev/null | grep -q .; then
  echo "dialogue cleanup left an owned socket" >&2
  find "$dialogue_root" -type s -print >&2 || true
  exit 1
fi
case "$dialogue_root" in
  "$PROJMUX_SMOKE_WORKDIR"/*) rm -rf -- "$dialogue_root" ;;
  *) echo "refusing to remove dialogue root outside smoke workdir: $dialogue_root" >&2; exit 1 ;;
esac
if [[ -e "$dialogue_root" ]]; then
  echo "dialogue exact owned root survived cleanup" >&2
  exit 1
fi
trap smoke_cleanup_env EXIT
echo ">> heterogeneous dialogue e2e passed: $dialogue_send_receipt agents=2 new-agents=0 waiters=0 codex-provider-writes=0 helper/socket/process-residual=0"
