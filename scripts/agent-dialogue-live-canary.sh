#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: PMX_DIALOGUE_CANARY_ROOT=/absolute/disposable/root $0 prepare|run" >&2
  exit 2
}

mode="${1:-}"
root="${PMX_DIALOGUE_CANARY_ROOT:-}"
[[ "$mode" == "prepare" || "$mode" == "run" ]] || usage
[[ "$root" == /* && "$root" != / && "$root" != "$HOME" && "$root" != "$HOME"/* ]] || {
  echo "PMX_DIALOGUE_CANARY_ROOT must be an absolute disposable path outside HOME" >&2
  exit 2
}
receipt_path="${PMX_DIALOGUE_CANARY_RECEIPT:-${root}.receipt.json}"
[[ "$receipt_path" == /* && "$receipt_path" != "$root" && "$receipt_path" != "$root"/* ]] || {
  echo "PMX_DIALOGUE_CANARY_RECEIPT must be an absolute path outside the disposable root" >&2
  exit 2
}
python3 - "$root" "$receipt_path" "$HOME" "$mode" <<'PY'
import os,pathlib,platform,stat,sys
root=pathlib.Path(sys.argv[1]); receipt=pathlib.Path(sys.argv[2]); home=pathlib.Path(sys.argv[3]).resolve(strict=True)
if str(root)!=os.path.normpath(str(root)) or str(receipt)!=os.path.normpath(str(receipt)):
    raise SystemExit("canary root and receipt paths must be clean absolute paths")
def private_parent_chain(path,label):
    parent=path.parent
    if not parent.exists() or not parent.is_dir(): raise SystemExit(label+" parent must already exist")
    for component in (parent,*parent.parents):
        info=component.lstat()
        if stat.S_ISLNK(info.st_mode):
            trusted=platform.system()=="Darwin" and str(component) in ("/tmp","/var") and component.resolve(strict=True)==pathlib.Path("/private"+str(component))
            if not trusted: raise SystemExit(label+" parent chain contains a symlink")
    return parent.resolve(strict=True)/path.name
resolved_root=private_parent_chain(root,"canary root")
if root.exists() or root.is_symlink():
    info=root.lstat()
    if stat.S_ISLNK(info.st_mode): raise SystemExit("canary root is a symlink")
    resolved_root=root.resolve(strict=True)
if resolved_root==home or home in resolved_root.parents:
    raise SystemExit("canary root resolves inside HOME")
if sys.argv[4]=="prepare":
    resolved_receipt=private_parent_chain(receipt,"canary receipt")
    if receipt.exists() or receipt.is_symlink():
        raise SystemExit("canary receipt must be a fresh non-symlink path")
    if resolved_receipt==resolved_root or resolved_root in resolved_receipt.parents:
        raise SystemExit("canary receipt resolves inside the disposable root")
PY

settings_snapshot() {
  python3 - "$HOME/.claude/settings.json" <<'PY'
import hashlib, json, os, pathlib, stat, sys
p=pathlib.Path(sys.argv[1])
if not p.exists():
    print(json.dumps({"exists":False},sort_keys=True)); raise SystemExit
b=p.read_bytes(); s=p.stat()
print(json.dumps({"exists":True,"sha256":hashlib.sha256(b).hexdigest(),"size":len(b),
                  "mode":format(stat.S_IMODE(s.st_mode),"04o"),"mtimeNs":s.st_mtime_ns},sort_keys=True))
PY
}

if [[ "$mode" == "prepare" ]]; then
  binary="${PMX_DIALOGUE_PROJMUX_BIN:-}"
  real_claude="${PMX_DIALOGUE_REAL_CLAUDE_BIN:-}"
  credential_file="${PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE:-}"
  prepared_message_ref="${PMX_DIALOGUE_MESSAGE_REF:-message-heterogeneous-live-canary}"
  [[ "$binary" == /* && -x "$binary" && "$real_claude" == /* && -x "$real_claude" &&
    "$credential_file" == /* && -f "$credential_file" && "$prepared_message_ref" =~ ^[A-Za-z0-9._:-]{1,160}$ ]] || {
    echo "prepare requires absolute executable PMX_DIALOGUE_PROJMUX_BIN/PMX_DIALOGUE_REAL_CLAUDE_BIN, PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE, and a bounded PMX_DIALOGUE_MESSAGE_REF" >&2
    exit 2
  }
  [[ ! -e "$root" ]] || { echo "canary root already exists" >&2; exit 2; }
  mkdir -p "$root"/{xdg-config,xdg-state,xdg-runtime,xdg-cache,tmux,home/.claude,codex-home,evidence,bin,work}
  chmod 0700 "$root" "$root"/{xdg-config,xdg-state,xdg-runtime,xdg-cache,tmux,home,home/.claude,codex-home,evidence,bin,work}
  printf '%s\n' 'projmux-dialogue-canary-owned-v3' >"$root/.projmux-dialogue-canary-owned"
  chmod 0600 "$root/.projmux-dialogue-canary-owned"
  prepare_complete=0
  # shellcheck disable=SC2317 # Invoked by the EXIT trap.
  prepare_cleanup() {
    if [[ "$prepare_complete" == 0 && -d "$root" ]]; then rm -rf -- "$root"; fi
  }
  trap prepare_cleanup EXIT
  settings_snapshot >"$root/evidence/global-settings-before.json"
  python3 - "$credential_file" >"$root/evidence/auth-source-before.json" <<'PY'
import hashlib,json,pathlib,stat,sys
p=pathlib.Path(sys.argv[1]); b=p.read_bytes(); s=p.stat()
print(json.dumps({"sha256":hashlib.sha256(b).hexdigest(),"size":len(b),"mode":format(stat.S_IMODE(s.st_mode),"04o"),"mtimeNs":s.st_mtime_ns},sort_keys=True))
PY
  python3 - "$root" "$credential_file" "$binary" "$receipt_path" "$prepared_message_ref" >"$root/cleanup-plan.json" <<'PY'
import json, pathlib, sys, time
print(json.dumps({"version":3,"ownedRoot":str(pathlib.Path(sys.argv[1]).resolve()),
                  "credentialSource":str(pathlib.Path(sys.argv[2]).resolve()),
                  "candidateBinary":str(pathlib.Path(sys.argv[3]).resolve()),
                  "receiptPath":str(pathlib.Path(sys.argv[4]).resolve()),"messageRef":sys.argv[5],
                  "preparedAtEpochNs":time.time_ns(),"cleanup":"delete exact Project; kill exact root-contained tmux server; remove owned credential; verify then remove owned root"},sort_keys=True))
PY
  chmod 0600 "$root/cleanup-plan.json"
  install -m 0600 "$credential_file" "$root/home/.claude/.credentials.json"
  ln -s "$binary" "$root/bin/projmux"
  printf '%s\n' '{"mcpServers":{}}' >"$root/evidence/empty-mcp.json"
  printf '%s\n' '{"connectorWrites":0,"externalWrites":0,"preInboundToolUse":0}' >"$root/evidence/external-effects.json"
  mkfifo "$root/evidence/provider.stdin"
  chmod 0600 "$root/evidence/provider.stdin"
  prepare_env=(env -u TMUX -u TMUX_PANE HOME="$root/home" CODEX_HOME="$root/codex-home" PATH="$root/bin:$PATH"
    XDG_CONFIG_HOME="$root/xdg-config" XDG_STATE_HOME="$root/xdg-state"
    XDG_RUNTIME_DIR="$root/xdg-runtime" XDG_CACHE_HOME="$root/xdg-cache"
    TMUX_TMPDIR="$root/tmux" PROJMUX_MANAGED_ROOTS="$root")
  "${prepare_env[@]}" "$binary" agent integrate claude --dry-run >"$root/evidence/integrate-dry-run.txt"
  "${prepare_env[@]}" "$binary" agent integrate claude >"$root/evidence/integrate.txt"
  python3 - >"$root/bin/collect-claude-public-jsonl" <<'PY'
print(r'''#!/usr/bin/env python3
import json,sys
init_allowed={"type","subtype","cwd","session_id","tools","mcp_servers","model","permissionMode",
              "slash_commands","apiKeySource","claude_code_version","output_style","agents","skills",
              "plugins","uuid","fast_mode_state","prompt_suggestion_enabled","messaging_socket_path"}
assistant_allowed={"type","message","parent_tool_use_id","session_id","uuid"}
assistant_message_allowed={"id","type","role","model","content","stop_reason","stop_sequence","usage"}
assistant_text_allowed={"type","text"}
result_allowed={"type","subtype","is_error","duration_ms","duration_api_ms","num_turns","result",
                "session_id","total_cost_usd","usage","modelUsage","permission_denials","uuid","errors",
                "structured_output"}
for raw in sys.stdin:
    try:
        event=json.loads(raw)
        if not isinstance(event,dict): raise ValueError("non-object")
        if "CLAUDE_CODE_MESSAGING_TOKEN" in event or "CLAUDE_CODE_MESSAGING_SOCKET" in event:
            raise ValueError("credential key")
        kind=event.get("type")
        if kind=="system" and event.get("subtype")=="init":
            if set(event)-init_allowed: raise ValueError("unknown init field")
        elif kind=="assistant":
            if set(event)-assistant_allowed or not isinstance(event.get("message"),dict): raise ValueError("assistant shape")
            message=event["message"]
            if set(message)-assistant_message_allowed or message.get("role")!="assistant": raise ValueError("assistant message shape")
            if not isinstance(message.get("content"),list) or not message["content"]: raise ValueError("assistant content")
            for block in message["content"]:
                if not isinstance(block,dict) or set(block)-assistant_text_allowed or block.get("type")!="text": raise ValueError("assistant block")
        elif kind=="result":
            if set(event)-result_allowed or event.get("subtype")!="success" or event.get("is_error") is not False: raise ValueError("result shape")
        else:
            raise ValueError("event type")
        if "messaging_socket_path" in event:
            locator=event.pop("messaging_socket_path")
            if not isinstance(locator,str) or not locator: raise ValueError("invalid locator")
            event["messaging_socket_present"]=True
        sys.stdout.write(json.dumps(event,separators=(",",":"),ensure_ascii=True)+"\n")
        sys.stdout.flush()
    except Exception:
        sys.stderr.write("public provider stream rejected by the frozen sanitizer\n")
        raise SystemExit(1)
''')
PY
  chmod 0700 "$root/bin/collect-claude-public-jsonl"
  python3 - "$root" <<'PYCODE'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1]); source=root/"bin/collect-claude-public-jsonl"
(root/"evidence/collector-source-integrity.json").write_text(json.dumps({"sha256":hashlib.sha256(source.read_bytes()).hexdigest()})+"\n")
PYCODE
  python3 - "$root" "$real_claude" "$prepared_message_ref" >"$root/bin/claude" <<'PY'
import json, pathlib, shlex, sys
root=pathlib.Path(sys.argv[1]); claude=shlex.quote(sys.argv[2]); ref=sys.argv[3]; q=shlex.quote
args=["--print","--verbose","--input-format","stream-json","--output-format","stream-json",
      "--restricted","--strict-mcp-config","--mcp-config",str(root/"evidence/empty-mcp.json"),
      "--setting-sources","","--tools","","--no-session-persistence","--permission-mode","dontAsk",
      "--no-chrome","--disable-slash-commands","--prompt-suggestions","false",
      "--settings",str(root/"home/.claude/settings.json")]
print("#!/bin/bash")
print("set -eu")
print("export HOME="+q(str(root/"home")))
print("export CLAUDE_CONFIG_DIR="+q(str(root/"home/.claude")))
start_code="import pathlib,time; pathlib.Path("+repr(str(root/"evidence/provider-started-at"))+").write_text(str(time.time_ns()))"
print("python3 -c "+q(start_code))
instruction="When a Projmux coordination message contains payload HETEROGENEOUS_REQUEST:"+ref+", answer with exactly HETEROGENEOUS_REPLY:"+ref+" and nothing else. Do not use tools."
line=json.dumps({"type":"user","message":{"role":"user","content":instruction}},separators=(",",":"))
print("exec 9<>"+q(str(root/"evidence/provider.stdin")))
print("printf '%s\\n' "+q(line)+" >&9")
collector=q(str(root/"bin/collect-claude-public-jsonl"))
print("exec "+claude+" "+" ".join(q(x) for x in args)+" <&9 > >("+collector+" >>"+q(str(root/"evidence/provider.jsonl"))+" 2>"+q(str(root/"evidence/provider-collector.stderr"))+") 2>"+q(str(root/"evidence/provider.stderr")))
PY
  chmod 0700 "$root/bin/claude"
  bash -n "$root/bin/claude"
  python3 - "$root/bin/claude" "$prepared_message_ref" <<'PY'
import json,shlex,sys
lines=[line for line in open(sys.argv[1]).read().splitlines() if line.startswith("printf ")]
assert len(lines)==1
tokens=shlex.split(lines[0]); assert tokens[0]=="printf" and tokens[1]=="%s\\n" and tokens[-1]==">&9"
frame=json.loads(tokens[2]); assert frame["type"]=="user" and sys.argv[2] in frame["message"]["content"]
PY
  prepare_complete=1
  trap - EXIT
  echo "prepared=$root cleanup-plan=$root/cleanup-plan.json receipt=$receipt_path"
  echo "owned-settings=$root/home/.claude/settings.json claude-wrapper=$root/bin/claude" >&2
  echo "Create the disposable Project and exact existing Codex/Claude Agents only now; the FIFO wrapper enqueues the fixed initial instruction. Freeze public evidence, then run the gated command." >&2
  exit 0
fi

[[ "${PMX_DIALOGUE_LIVE_CANARY:-}" == "1" ]] || {
  echo "refusing live provider traffic without PMX_DIALOGUE_LIVE_CANARY=1" >&2
  exit 2
}
input="${PMX_DIALOGUE_CANARY_INPUT:-$root/canary-input.json}"
[[ -f "$root/cleanup-plan.json" && -f "$input" && -f "$root/evidence/global-settings-before.json" ]] || {
  echo "run requires the prepare receipt and canary-input.json" >&2
  exit 2
}

# Prove the deletion root was created by prepare before arming any cleanup that
# removes it. This check performs no mutation and rejects symlinked or
# group/world-accessible roots and critical descendants.
root_identity="$(python3 - "$root" "$root/cleanup-plan.json" "$root/.projmux-dialogue-canary-owned" <<'PY'
import json,os,pathlib,stat,sys
root=pathlib.Path(sys.argv[1]); plan_path=pathlib.Path(sys.argv[2]); sentinel=pathlib.Path(sys.argv[3])
directories=(root,root/"xdg-config",root/"xdg-state",root/"xdg-runtime",root/"xdg-cache",root/"tmux",
             root/"home",root/"home/.claude",root/"codex-home",root/"evidence",root/"bin",root/"work")
for path,kind,mode in tuple((p,"dir",0o700) for p in directories)+((plan_path,"file",0o600),(sentinel,"file",0o600)):
    info=path.lstat()
    assert not path.is_symlink() and info.st_uid==os.getuid() and stat.S_IMODE(info.st_mode)==mode
    assert (kind=="dir" and stat.S_ISDIR(info.st_mode)) or (kind=="file" and stat.S_ISREG(info.st_mode))
resolved=root.resolve(strict=True); plan=json.load(open(plan_path))
assert plan.get("version")==3 and pathlib.Path(plan.get("ownedRoot","")).resolve(strict=True)==resolved
assert pathlib.Path(plan.get("receiptPath","")).is_absolute()
assert sentinel.read_text()=="projmux-dialogue-canary-owned-v3\n"
candidate_link=root/"bin/projmux"
assert candidate_link.is_symlink() and candidate_link.resolve(strict=True)==pathlib.Path(plan["candidateBinary"])
info=root.stat()
print(f"{info.st_dev}:{info.st_ino}")
PY
)"

field() {
  python3 - "$input" "$1" <<'PY'
import json, sys
v=json.load(open(sys.argv[1]))
for key in sys.argv[2].split('.'):
    v=v[key]
if isinstance(v,(dict,list)):
    raise SystemExit("field must be scalar: "+sys.argv[2])
print(v)
PY
}

binary="$(field binary)"
registry="$(field registryPath)"
socket_path="$(field tmuxSocketPath)"
socket_name="$(field tmuxSocketName)"
project_uid="$(field projectUID)"

# Prove the minimal cleanup authority before arming the trap. The remaining
# input, Registry contents, and provider evidence are intentionally parsed only
# after cleanup is live so any later fail-closed gate still removes this exact
# prepared Project/tmux root.
socket_identity="$(python3 - "$root" "$root/cleanup-plan.json" "$binary" "$registry" "$socket_path" "$socket_name" "$project_uid" <<'PY'
import json,os,pathlib,re,socket,stat,sys
root=pathlib.Path(sys.argv[1]).resolve(strict=True); plan=json.load(open(sys.argv[2]))
binary=pathlib.Path(sys.argv[3]); registry=pathlib.Path(sys.argv[4]); sock=pathlib.Path(sys.argv[5])
if not binary.is_absolute() or not binary.exists() or not os.access(binary,os.X_OK):
    raise SystemExit("binary is not an absolute executable")
if binary.resolve(strict=True)!=pathlib.Path(plan["candidateBinary"]):
    raise SystemExit("binary differs from the prepared exact candidate")
for path,label,kind in ((registry,"Registry","file"),(sock,"tmux socket","socket")):
    if not path.is_absolute(): raise SystemExit(label+" is not absolute")
    resolved=path.resolve(strict=True)
    if root not in resolved.parents: raise SystemExit(label+" escaped the owned root")
    info=path.lstat()
    if path.is_symlink() or info.st_uid!=os.getuid(): raise SystemExit(label+" is not exact-owned")
    if kind=="file" and not stat.S_ISREG(info.st_mode): raise SystemExit(label+" is not regular")
    if kind=="socket" and not stat.S_ISSOCK(info.st_mode): raise SystemExit(label+" is not a socket")
for value,label in ((sys.argv[6],"socket name"),(sys.argv[7],"Project UID")):
    if not re.fullmatch(r"[A-Za-z0-9._:-]{1,160}",value): raise SystemExit("invalid "+label)
info=sock.lstat(); print(f"{info.st_dev}:{info.st_ino}")
PY
)"

canary_env=(env -u TMUX -u TMUX_PANE HOME="$root/home" CODEX_HOME="$root/codex-home" PATH="$root/bin:$PATH"
  XDG_CONFIG_HOME="$root/xdg-config" XDG_STATE_HOME="$root/xdg-state"
  XDG_RUNTIME_DIR="$root/xdg-runtime" XDG_CACHE_HOME="$root/xdg-cache"
  TMUX_TMPDIR="$root/tmux" PROJMUX_MANAGED_ROOTS="$root")
cleanup_done=0
wait_pid=""
claim_identity_path="$root/evidence/claim-wait-process.json"
cleanup_owned() {
  [[ "$cleanup_done" == 0 ]] || return 0
  cleanup_done=1
  if [[ -n "$wait_pid" ]]; then
    if [[ -f "$claim_identity_path" ]]; then
      python3 - "$claim_identity_path" <<'PY'
import json,os,pathlib,signal,sys
identity=json.load(open(sys.argv[1])); pid=identity["pid"]
try:
    proc=pathlib.Path("/proc",str(pid)); raw=(proc/"stat").read_text(); end=raw.rfind(")"); fields=raw[end+1:].split()
    boot=pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    actual={"pid":pid,"ownerUID":(proc/"stat").stat().st_uid,"start":"linux:"+boot+":"+fields[19]}
    if end>0 and len(fields)>=20 and fields[0] not in ("Z","X") and actual==identity:
        os.kill(pid,signal.SIGTERM)
except (FileNotFoundError,ProcessLookupError): pass
PY
    elif jobs -pr | grep -Fxq "$wait_pid"; then
      kill "$wait_pid" 2>/dev/null || true
    fi
    wait "$wait_pid" 2>/dev/null || true
  fi
  if [[ "$binary" == /* && -x "$binary" && "$project_uid" =~ ^[A-Za-z0-9._:-]{1,160}$ ]]; then
    "${canary_env[@]}" "$binary" delete project "uid:$project_uid" --socket "$socket_name" --yes >/dev/null 2>&1 || true
  fi
  current_socket_identity="$(python3 - "$socket_path" <<'PY'
import pathlib,platform,stat,sys
path=pathlib.Path(sys.argv[1])
try: info=path.lstat()
except FileNotFoundError: print("absent")
else: print(f"{info.st_dev}:{info.st_ino}" if stat.S_ISSOCK(info.st_mode) and not path.is_symlink() else "invalid")
PY
)"
  if [[ "$current_socket_identity" == "$socket_identity" ]]; then
    tmux -S "$socket_path" kill-server >/dev/null 2>&1 || true
  fi
  rm -f -- "$root/home/.claude/.credentials.json"
}
finish_cleanup() {
  cleanup_owned
  current_root_identity="$(python3 - "$root" "$root/cleanup-plan.json" "$root/.projmux-dialogue-canary-owned" <<'PY'
import json,pathlib,stat,sys
root=pathlib.Path(sys.argv[1])
try:
    info=root.lstat(); plan=json.load(open(sys.argv[2])); sentinel=pathlib.Path(sys.argv[3]).read_text()
except (FileNotFoundError,ValueError): print("invalid")
else:
    valid=stat.S_ISDIR(info.st_mode) and not root.is_symlink() and plan.get("version")==3 and pathlib.Path(plan.get("ownedRoot",""))==root.resolve() and sentinel=="projmux-dialogue-canary-owned-v3\n"
    print(f"{info.st_dev}:{info.st_ino}" if valid else "invalid")
PY
)"
  if [[ "$current_root_identity" == "$root_identity" ]]; then rm -rf -- "$root"; fi
}
trap finish_cleanup EXIT

# The exact cleanup trap is now live. Every path/evidence/gate validation below
# therefore removes already-launched owned resources on failure.
python3 - "$root" "$receipt_path" "$root/cleanup-plan.json" <<'PY'
import json,pathlib,platform,stat,sys
root=pathlib.Path(sys.argv[1]).resolve(strict=True); receipt=pathlib.Path(sys.argv[2]); parent=receipt.parent
if not parent.exists() or not parent.is_dir(): raise SystemExit("canary receipt parent must already exist")
for component in (parent,*parent.parents):
    if stat.S_ISLNK(component.lstat().st_mode):
        trusted=platform.system()=="Darwin" and str(component) in ("/tmp","/var") and component.resolve(strict=True)==pathlib.Path("/private"+str(component))
        if not trusted: raise SystemExit("canary receipt parent chain contains a symlink")
resolved=parent.resolve(strict=True)/receipt.name
if receipt.exists() or receipt.is_symlink(): raise SystemExit("canary receipt must be a fresh non-symlink path")
if resolved==root or root in resolved.parents: raise SystemExit("canary receipt resolves inside the disposable root")
plan=json.load(open(sys.argv[3]))
if resolved!=pathlib.Path(plan.get("receiptPath","")): raise SystemExit("canary receipt differs from the prepared cleanup plan")
PY

server_pid="$(field tmuxServerPID)"
sender_uid="$(field sender.agentUID)"
sender_pane_id="$(field sender.paneID)"
receiver_uid="$(field receiver.agentUID)"
message_ref="$(field messageRef)"
live_jsonl="$(field evidence.providerLiveJSONL)"
init_jsonl="$root/evidence/init.jsonl"
events_jsonl="$root/evidence/pre-inbound.jsonl"
provider_stderr="$(field evidence.providerStderr)"
effects_json="$(field evidence.externalEffectsJSON)"
owned_settings="$(field evidence.ownedSettingsJSON)"
provider_started_at="$(field evidence.providerStartedAt)"

python3 - "$root" "$binary" "$registry" "$socket_path" "$live_jsonl" "$provider_stderr" "$effects_json" "$owned_settings" "$provider_started_at" <<'PY'
import os, pathlib, sys
root=pathlib.Path(sys.argv[1]).resolve()
for raw in sys.argv[2:]:
    p=pathlib.Path(raw)
    if not p.is_absolute(): raise SystemExit("all binary/evidence paths must be absolute")
    resolved=p.resolve(strict=True)
    if raw != sys.argv[2] and root not in resolved.parents:
        raise SystemExit("evidence/Registry/tmux/settings path escaped owned root: "+raw)
if not os.access(sys.argv[2],os.X_OK): raise SystemExit("binary is not executable")
PY

"${canary_env[@]}" "$binary" agent capabilities "uid:$receiver_uid" -o json >"$root/evidence/capability-before.json"
observed_server_pid="$(env -u TMUX -u TMUX_PANE tmux -S "$socket_path" display-message -p '#{pid}')"
[[ "$observed_server_pid" == "$server_pid" ]] || { echo "exact tmux server PID changed before traffic" >&2; exit 1; }

# This is the traffic gate. It validates the exact Registry route, the cleanup
# plan predating provider launch, the same long-lived public init session, and
# zero tool/MCP/plugin/stderr/external effects. No message command precedes it.
python3 - "$root/cleanup-plan.json" "$input" "$registry" "$live_jsonl" "$provider_stderr" "$effects_json" "$owned_settings" "$provider_started_at" "$binary" \
  "$root/evidence/capability-before.json" "$init_jsonl" "$events_jsonl" <<'PY'
import hashlib, json, os, pathlib, stat, sys
plan=json.load(open(sys.argv[1])); spec=json.load(open(sys.argv[2])); reg=json.load(open(sys.argv[3]))
assert plan["preparedAtEpochNs"] < spec["provider"]["startedAtEpochNs"], "cleanup was not registered before provider launch"
assert plan["messageRef"] == spec["messageRef"], "prepared semantic message reference changed"
projects={x["metadata"]["uid"]:x for x in reg.get("projects",[])}; windows={x["metadata"]["uid"]:x for x in reg.get("windows",[])}
agents={x["metadata"]["uid"]:x for x in reg.get("agents",[])}; panes={x["metadata"]["uid"]:x for x in reg.get("panes",[])}
assert len(projects)==1 and spec["projectUID"] in projects, "canary must own exactly one Project"
assert spec["windowUID"] in windows and windows[spec["windowUID"]]["metadata"]["ownerRef"]=={"kind":"Project","uid":spec["projectUID"]}
assert len(agents)==2, "canary must use exactly two already-created Agents"
for key, provider in (("sender","codex"),("receiver","claude")):
    item=spec[key]; a=agents[item["agentUID"]]; p=panes[item["paneUID"]]
    assert a["spec"]["provider"]==provider and a["status"]["phase"]=="Running"
    assert a["metadata"]["ownerRef"]=={"kind":"Window","uid":spec["windowUID"]}
    assert a["status"]["paneRef"]==item["paneUID"] and p["metadata"]["ownerRef"]=={"kind":"Agent","uid":item["agentUID"]}
    assert p["status"]["activation"]["generation"]==item["generation"]
    assert p["status"]["activation"]["runtimeID"]==item["paneID"]
cp=panes[spec["receiver"]["paneUID"]]["status"]["activation"]["claude"]
auth=cp["registration"]["authority"]; process=spec["provider"]
assert cp["registration"]["ready"] is True
assert auth["sessionId"]==process["sessionID"]
assert auth["process"]=={"pid":process["pid"],"ownerUID":process["ownerUID"],"start":process["start"]}

def exact_linux_process(expected):
    assert set(expected)=={"pid","ownerUID","start"} and expected["pid"]>1
    assert expected["start"].startswith("linux:"), "live canary process proof currently requires Linux procfs"
    proc=pathlib.Path("/proc",str(expected["pid"])); raw=(proc/"stat").read_text()
    end=raw.rfind(")"); fields=raw[end+1:].split()
    assert end>0 and len(fields)>=20 and fields[0] not in ("Z","X")
    boot=pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    actual={"pid":expected["pid"],"ownerUID":(proc/"stat").stat().st_uid,"start":"linux:"+boot+":"+fields[19]}
    assert actual==expected, "process birth identity changed"
    return proc

def observe_linux_process(pid):
    proc=pathlib.Path("/proc",str(pid)); raw=(proc/"stat").read_text()
    end=raw.rfind(")"); fields=raw[end+1:].split()
    assert pid>1 and end>0 and len(fields)>=20 and fields[0] not in ("Z","X")
    boot=pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    return {"pid":pid,"ownerUID":(proc/"stat").stat().st_uid,"start":"linux:"+boot+":"+fields[19]}

provider_proc=exact_linux_process(auth["process"])
helper_proc=exact_linux_process(auth["leaseProcess"])
tmux_process=observe_linux_process(spec["tmuxServerPID"])
assert tmux_process["ownerUID"]==os.getuid(), "tmux server is not exact-owned"
candidate=pathlib.Path(sys.argv[9])
assert pathlib.Path(os.readlink(helper_proc/"exe")).resolve(strict=True)==candidate.resolve(strict=True), "helper binary differs from candidate"
argv=[part.decode() for part in (helper_proc/"cmdline").read_bytes().split(b"\0") if part]
assert len(argv)==3 and argv[1:]==["internal","claude-endpoint-helper"], "helper argv is not the fixed hidden route"
helper_env_keys=set()
for entry in (helper_proc/"environ").read_bytes().split(b"\0"):
    if entry:
        helper_env_keys.add(entry.partition(b"=")[0].decode("utf-8","strict"))
assert not {"CLAUDE_CODE_MESSAGING_SOCKET","CLAUDE_CODE_MESSAGING_TOKEN"}&helper_env_keys, "messaging credential key survived helper scrub"
lease_digest=hashlib.sha256((sys.argv[3]+"\0"+spec["receiver"]["paneUID"]+"\0"+spec["receiver"]["generation"]).encode()).hexdigest()[:32]
lease_dir=pathlib.Path("/tmp","pmx-ce-"+lease_digest); lease_info=lease_dir.lstat()
assert stat.S_ISDIR(lease_info.st_mode) and stat.S_IMODE(lease_info.st_mode)==0o700 and lease_info.st_uid==os.getuid()
lease_entries=list(lease_dir.iterdir())
assert lease_entries and all(not p.is_symlink() for p in lease_entries), "activation lease is empty or contains a symlink"
assert all(stat.S_ISSOCK(p.lstat().st_mode) or (p.is_file() and p.name.endswith(".sock.json")) for p in lease_entries)

live_path=pathlib.Path(sys.argv[4]); live_bytes=live_path.read_bytes()
def jsonl_bytes(data):
    return [json.loads(line) for line in data.decode().splitlines() if line.strip()]
init=jsonl_bytes(live_bytes)
assert init and all(isinstance(x,dict) for x in init), "public stream contains a non-object event"
init_allowed={"type","subtype","cwd","session_id","tools","mcp_servers","model","permissionMode",
              "slash_commands","apiKeySource","claude_code_version","output_style","agents","skills",
              "plugins","uuid","fast_mode_state","prompt_suggestion_enabled","messaging_socket_present"}
assistant_allowed={"type","message","parent_tool_use_id","session_id","uuid"}
assistant_message_allowed={"id","type","role","model","content","stop_reason","stop_sequence","usage"}
assistant_text_allowed={"type","text"}
result_allowed={"type","subtype","is_error","duration_ms","duration_api_ms","num_turns","result",
                "session_id","total_cost_usd","usage","modelUsage","permission_denials","uuid","errors",
                "structured_output"}
for event in init:
    kind=event.get("type")
    if kind=="system" and event.get("subtype")=="init":
        assert not set(event)-init_allowed, "unknown current-version init field"
    elif kind=="assistant":
        assert not set(event)-assistant_allowed and isinstance(event.get("message"),dict), "unknown assistant event shape"
        message=event["message"]
        assert not set(message)-assistant_message_allowed and message.get("role")=="assistant"
        assert isinstance(message.get("content"),list) and message["content"]
        for block in message["content"]:
            assert isinstance(block,dict) and not set(block)-assistant_text_allowed and block.get("type")=="text"
    elif kind=="result":
        assert not set(event)-result_allowed and event.get("subtype")=="success" and event.get("is_error") is False
    else:
        raise AssertionError("unexpected public stream event type")
matches=[x for x in init if x.get("type")=="system" and x.get("subtype")=="init"]
assert len(matches)==1 and matches[0].get("session_id")==process["sessionID"]
assert init[0] is matches[0] and sum(x.get("type")=="result" for x in init)==1 and init[-1].get("type")=="result"
provider_version=matches[0].get("claude_code_version",matches[0].get("version",""))
assert provider_version=="2.1.263", "frozen frame is not qualified for the observed Claude version"
assert matches[0].get("tools")==[] and matches[0].get("mcp_servers")==[] and matches[0].get("plugins")==[]
assert matches[0].get("messaging_socket_present") is True, "sanitized messaging endpoint presence is missing"
events=init
def contains_tool(value):
    if isinstance(value,dict): return value.get("type")=="tool_use" or any(contains_tool(v) for v in value.values())
    if isinstance(value,list): return any(contains_tool(v) for v in value)
    return False
assert not any(contains_tool(x) for x in init+events), "pre-inbound tool use is nonzero"
assert pathlib.Path(sys.argv[5]).read_bytes()==b"", "unexpected provider stderr"
assert pathlib.Path(plan["ownedRoot"],"evidence/provider-collector.stderr").read_bytes()==b"", "public stream sanitizer rejected provider output"
effects=json.load(open(sys.argv[6])); assert effects=={"connectorWrites":0,"externalWrites":0,"preInboundToolUse":0}
settings=json.load(open(sys.argv[7])); hooks=settings.get("hooks",{})
assert int(pathlib.Path(sys.argv[8]).read_text()) == spec["provider"]["startedAtEpochNs"], "provider launch receipt mismatch"
owned_binary=pathlib.Path(plan["ownedRoot"],"bin/projmux")
assert owned_binary.is_symlink() and owned_binary.resolve(strict=True)==candidate.resolve(strict=True)
candidate_hash=hashlib.sha256(candidate.read_bytes()).hexdigest()
capability=json.load(open(sys.argv[10])); runtime=capability["runtimeEligibility"]; coordination=runtime["coordination"]
assert capability["provider"]=="claude" and capability["agent"]["uid"]==spec["receiver"]["agentUID"]
assert runtime["registryReady"] is True and runtime["paneUID"]==spec["receiver"]["paneUID"]
assert runtime["activationGeneration"]==spec["receiver"]["generation"]
assert runtime["routeIncarnation"].startswith("route-")
assert coordination["eligible"] is False and coordination["reason"]=="Claude coordination is unqualified for the exact running provider version"
commands=[]
for event, entries in hooks.items():
    for matcher in entries:
        for hook in matcher.get("hooks",[]): commands.append((event,hook))
reply="exec projmux internal claude-message-reply >/dev/null 2>&1 # projmux-managed:claude-coordination:v3"
boundary="exec projmux internal claude-message-boundary >/dev/null 2>&1 # projmux-managed:claude-coordination:v3"
rows=[h for e,h in commands if e=="Stop" and h.get("command")==reply]
assert len(rows)==1 and rows[0].get("type")=="command" and rows[0].get("timeout")==5
assert not rows[0].get("async",False) and not rows[0].get("asyncRewake",False)
rows=[h for e,h in commands if e=="UserPromptSubmit" and h.get("command")==boundary]
assert len(rows)==1 and rows[0].get("type")=="command" and rows[0].get("timeout")==5
assert not rows[0].get("async",False) and not rows[0].get("asyncRewake",False)
assert not any("claude-message-wait" in h.get("command","") or h.get("asyncRewake",False) for _,h in commands), "obsolete ingress waiter remains"
assert live_path.read_bytes()==live_bytes, "public provider stream changed while the traffic gate was evaluated"
pathlib.Path(sys.argv[11]).write_bytes(live_bytes)
pathlib.Path(sys.argv[12]).write_bytes(live_bytes)
live_hash=hashlib.sha256(live_bytes).hexdigest()
evidence={"version":1,"claude_code_version":provider_version,"sessionId":auth["sessionId"],
 "agentUID":spec["receiver"]["agentUID"],"paneUID":spec["receiver"]["paneUID"],
 "activationGeneration":spec["receiver"]["generation"],"routeIncarnation":runtime["routeIncarnation"],
 "providerProcess":auth["process"],"registrationGeneration":auth["registrationGeneration"],
 "helperProcess":auth["leaseProcess"],"tools":[],"mcp_servers":[],"plugins":[],"pluginInitCount":0,
 "preMarkerToolUse":0,"preMarkerStderr":0,"inboundPolicy":"accept","publicInitObserved":True,
 "streamFrozen":True,"observedAt":__import__("datetime").datetime.now(__import__("datetime").timezone.utc).isoformat().replace("+00:00","Z")}
evidence_path=pathlib.Path(plan["ownedRoot"],"evidence","qualification-evidence.json")
temporary=evidence_path.with_suffix(".tmp")
with open(temporary,"x") as f: json.dump(evidence,f,separators=(",",":")); f.write("\n")
temporary.chmod(0o600); temporary.replace(evidence_path)
pathlib.Path(plan["ownedRoot"],"evidence","traffic-gate.json").write_text(json.dumps({
  "version":1,"result":"pass","sessionID":process["sessionID"],"providerPID":process["pid"],
  "providerOwnerUID":process["ownerUID"],"providerStart":process["start"],
  "providerVersion":provider_version,"initCount":1,
  "tools":[],"mcpServers":[],"plugins":[],"preInboundToolUse":0,"stderrBytes":0,
  "preInboundEventCount":len(events),"connectorWrites":0,"externalWrites":0,
  "candidateSHA256":candidate_hash,"sanitizedProviderStreamSHA256":live_hash,"waiters":0,
  "helperProcess":auth["leaseProcess"],"helperArgv":"projmux internal claude-endpoint-helper",
  "tmuxProcess":tmux_process,
  "activationLeaseDir":str(lease_dir),"credentialEnvPresent":False,
  "capabilityBeforeQualification":{"eligible":False,"evidence":coordination["evidence"],"reason":coordination["reason"]}},sort_keys=True)+"\n")
PY
[[ -f "$root/evidence/traffic-gate.json" ]] || { echo "traffic gate did not publish its receipt" >&2; exit 1; }
python3 - "$live_jsonl" "$root/evidence/traffic-gate.json" <<'PY'
import hashlib,json,pathlib,sys
assert hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest()==json.load(open(sys.argv[2]))["sanitizedProviderStreamSHA256"], "provider stream changed before broker traffic"
PY

# This is the first provider push. It is a dedicated opt-in qualification, not
# an ordinary send and not a caller-supplied version assertion. The helper opens
# eligibility only after the exact marker returns through the official Stop
# hook. If the injected message triggers UserPromptSubmit, the boundary closes
# the attempt and this command fails rather than creating an exception.
"${canary_env[@]}" "$binary" agent message qualify "uid:$receiver_uid" \
  --evidence "$root/evidence/qualification-evidence.json" \
  --confirm-isolated-provider-push -o json >"$root/evidence/qualification-receipt.json"
"${canary_env[@]}" "$binary" agent capabilities "uid:$receiver_uid" -o json >"$root/evidence/capability-qualified.json"
python3 - "$root/evidence/qualification-receipt.json" "$root/evidence/capability-qualified.json" <<'PY'
import json,sys
receipt=json.load(open(sys.argv[1])); capability=json.load(open(sys.argv[2]))
assert receipt["state"]=="qualification-qualified" and receipt["providerVersion"]=="2.1.263"
assert receipt["evidence"]=="owned-public-init-plus-exact-stop-marker"
assert receipt["ambiguous"] is False and receipt["autoResend"] is False
coordination=capability["runtimeEligibility"]["coordination"]
assert coordination["eligible"] is True and coordination["evidence"]=="helper-memory-exact-version-qualification"
PY

tree_snapshot() {
  python3 - "$root/codex-home" <<'PY'
import hashlib,json,pathlib,stat,sys
root=pathlib.Path(sys.argv[1]); rows=[]
for p in sorted(root.rglob("*")):
    if p.is_symlink(): rows.append([str(p.relative_to(root)),"symlink",str(p.readlink())]); continue
    if p.is_file():
        b=p.read_bytes(); s=p.stat()
        rows.append([str(p.relative_to(root)),"file",format(stat.S_IMODE(s.st_mode),"04o"),len(b),s.st_mtime_ns,hashlib.sha256(b).hexdigest()])
print(hashlib.sha256(json.dumps(rows,separators=(",",":"),ensure_ascii=True).encode()).hexdigest())
PY
}
agent_count_before="$("${canary_env[@]}" "$binary" get agents --project "uid:$project_uid" -o uid | wc -l)"
codex_state_before="$(tree_snapshot)"
"${canary_env[@]}" TMUX="$socket_path,$server_pid,0" TMUX_PANE="$sender_pane_id" \
  "$binary" agent message wait "uid:$sender_uid" --timeout 120s -o json >"$root/evidence/reply.json" &
wait_pid=$!
python3 - "$wait_pid" "$binary" "$sender_uid" "$claim_identity_path" <<'PY'
import json,os,pathlib,sys,time
pid=int(sys.argv[1]); candidate=pathlib.Path(sys.argv[2]).resolve(strict=True); expected_ref="uid:"+sys.argv[3]
deadline=time.monotonic()+2; identity=None
while time.monotonic()<deadline:
    try:
        proc=pathlib.Path("/proc",str(pid)); raw=(proc/"stat").read_text(); end=raw.rfind(")"); fields=raw[end+1:].split()
        boot=pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
        argv=[part.decode() for part in (proc/"cmdline").read_bytes().split(b"\0") if part]
        executable=pathlib.Path(os.readlink(proc/"exe")).resolve(strict=True)
        if end>0 and len(fields)>=20 and fields[0] not in ("Z","X") and executable==candidate and argv[1:]==["agent","message","wait",expected_ref,"--timeout","120s","-o","json"]:
            identity={"pid":pid,"ownerUID":(proc/"stat").stat().st_uid,"start":"linux:"+boot+":"+fields[19]}
            break
    except (FileNotFoundError,ProcessLookupError): pass
    time.sleep(.02)
assert identity is not None and identity["ownerUID"]==os.getuid(), "exact Codex self-claim child was not observed"
path=pathlib.Path(sys.argv[4]); path.write_text(json.dumps(identity,sort_keys=True)+"\n"); path.chmod(0o600)
PY
"${canary_env[@]}" TMUX="$socket_path,$server_pid,0" TMUX_PANE="$sender_pane_id" \
  "$binary" agent message send --message-ref "$message_ref" "uid:$receiver_uid" -- "HETEROGENEOUS_REQUEST:$message_ref" \
  >"$root/evidence/send.txt"
wait "$wait_pid"
"${canary_env[@]}" TMUX="$socket_path,$server_pid,0" TMUX_PANE="$sender_pane_id" \
  "$binary" agent message status "$message_ref" -o json >"$root/evidence/status.json"
agent_count_after="$("${canary_env[@]}" "$binary" get agents --project "uid:$project_uid" -o uid | wc -l)"
codex_state_after="$(tree_snapshot)"
[[ "$codex_state_before" == "$codex_state_after" ]] || {
  echo "Codex provider state changed during coordination traffic" >&2
  exit 1
}

python3 - "$input" "$root/evidence/reply.json" "$root/evidence/status.json" "$agent_count_before" "$agent_count_after" <<'PY'
import hashlib, json, sys
s=json.load(open(sys.argv[1])); reply=json.load(open(sys.argv[2])); status=json.load(open(sys.argv[3])); ref=s["messageRef"]
conversation="conversation-"+hashlib.sha256(ref.encode()).hexdigest()[:36]; env=reply["envelope"]
assert sys.argv[4]==sys.argv[5]=="2", "message traffic created or removed an Agent"
assert env["messageRef"] != ref and env["replyTo"]==ref and env["conversationRef"]==conversation
assert env["source"]["agentUID"]==s["receiver"]["agentUID"] and env["source"]["paneUID"]==s["receiver"]["paneUID"]
assert env["source"]["activationGeneration"]==s["receiver"]["generation"]
assert env["target"]["agentUID"]==s["sender"]["agentUID"] and env["target"]["paneUID"]==s["sender"]["paneUID"]
assert env["target"]["activationGeneration"]==s["sender"]["generation"]
assert env["payload"]=="HETEROGENEOUS_REPLY:"+ref, "model-visible semantic reply marker mismatch"
assert reply["delivery"]["state"]=="delivered" and reply["delivery"]["reason"]=="target-self-claim"
assert status["messageRef"]==ref and status["conversationRef"]==conversation and status["delivery"]["state"]=="delivered"
PY

cleanup_owned
python3 - "$root/evidence/traffic-gate.json" "$input" "$registry" "$claim_identity_path" "$root/evidence/cleanup-verification.json" <<'PY'
import hashlib,json,os,pathlib,re,signal,stat,sys,time
gate=json.load(open(sys.argv[1])); spec=json.load(open(sys.argv[2])); registry=sys.argv[3]
provider={"pid":gate["providerPID"],"ownerUID":gate["providerOwnerUID"],"start":gate["providerStart"]}
helper=gate["helperProcess"]
tmux_process=gate["tmuxProcess"]
claim=json.load(open(sys.argv[4]))

def current(identity):
    try:
        proc=pathlib.Path("/proc",str(identity["pid"])); raw=(proc/"stat").read_text()
        end=raw.rfind(")"); fields=raw[end+1:].split()
        if end<=0 or len(fields)<20 or fields[0] in ("Z","X"): return False
        boot=pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
        actual={"pid":identity["pid"],"ownerUID":(proc/"stat").stat().st_uid,"start":"linux:"+boot+":"+fields[19]}
        return actual==identity
    except (FileNotFoundError,ProcessLookupError):
        return False

identities=(provider,helper,tmux_process,claim)
deadline=time.monotonic()+10
while any(current(identity) for identity in identities) and time.monotonic()<deadline:
    time.sleep(.05)
for identity in identities:
    if current(identity): os.kill(identity["pid"],signal.SIGTERM)
deadline=time.monotonic()+10
while any(current(identity) for identity in identities) and time.monotonic()<deadline:
    time.sleep(.05)
assert not any(current(identity) for identity in identities), "exact owned provider/helper process survived cleanup"

digest=hashlib.sha256((registry+"\0"+spec["receiver"]["paneUID"]+"\0"+spec["receiver"]["generation"]).encode()).hexdigest()[:32]
lease_dir=pathlib.Path("/tmp","pmx-ce-"+digest)
assert gate["activationLeaseDir"]==str(lease_dir), "activation lease derivation changed"
if lease_dir.exists():
    info=lease_dir.lstat()
    assert stat.S_ISDIR(info.st_mode) and stat.S_IMODE(info.st_mode)==0o700 and info.st_uid==os.getuid() and not lease_dir.is_symlink()
    for path in lease_dir.iterdir():
        item=path.lstat(); name=path.name
        valid_socket=(stat.S_ISSOCK(item.st_mode) and (re.fullmatch(r"[0-9a-f]{32}\.sock",name) or re.fullmatch(r"coord-[0-9a-f]{24}\.sock",name)))
        valid_receipt=stat.S_ISREG(item.st_mode) and re.fullmatch(r"[0-9a-f]{32}\.sock\.json",name)
        assert item.st_uid==os.getuid() and not path.is_symlink() and (valid_socket or valid_receipt), "unexpected activation lease residue"
        path.unlink()
    lease_dir.rmdir()
assert not lease_dir.exists(), "activation lease directory survived cleanup"
pathlib.Path(sys.argv[5]).write_text(json.dumps({"version":1,"exactProviderBirthAbsent":True,
  "exactHelperBirthAbsent":True,"exactTmuxBirthAbsent":True,"activationLeaseDirAbsent":True,
  "exactClaimBirthAbsent":True,"credentialEnvPresent":False},sort_keys=True)+"\n")
PY
for _ in $(seq 1 200); do
  # shellcheck disable=SC2009 # Match the literal owned root, not a regex.
  if ! ps -eo args= | grep -F "$root" | grep -v grep >/dev/null &&
    ! find "$root" -type s -print -quit | grep -q .; then break; fi
  sleep 0.1
done
# shellcheck disable=SC2009 # Match the literal owned root, not a regex.
if ps -eo args= | grep -F "$root" | grep -v grep >/dev/null; then
  echo "owned process residue remains after cleanup" >&2
  exit 1
fi
if find "$root" -type s -print -quit | grep -q .; then
  echo "owned tmux/helper socket residue remains after cleanup" >&2
  exit 1
fi
if env -u TMUX -u TMUX_PANE tmux -S "$socket_path" list-sessions >/dev/null 2>&1; then
  echo "owned tmux server remained reachable after cleanup" >&2
  exit 1
fi
python3 - "$registry" "$effects_json" "$root/evidence/global-settings-before.json" "$(settings_snapshot)" "$root" "$receipt_path" \
  "$input" "$root/evidence/traffic-gate.json" "$root/evidence/reply.json" "$root/evidence/status.json" \
  "$root/cleanup-plan.json" "$root/evidence/auth-source-before.json" "$codex_state_before" "$codex_state_after" \
  "$root/evidence/qualification-receipt.json" "$root/evidence/cleanup-verification.json" "$claim_identity_path" <<'PY'
import hashlib,json,os,pathlib,platform,stat,sys
registry,effects,before_path,current,root,out,input_path,gate_path,reply_path,status_path,plan_path,auth_before_path,codex_before,codex_after,qualification_path,cleanup_path,claim_path=sys.argv[1:]
if pathlib.Path(registry).exists():
    reg=json.load(open(registry))
    for collection in ("projects","controlSessions","windows","panes","agents","nameReservations"):
        assert not reg.get(collection), "Registry residue in "+collection
assert json.load(open(effects))=={"connectorWrites":0,"externalWrites":0,"preInboundToolUse":0}
before=json.load(open(before_path)); after=json.loads(current)
assert before==after, "global Claude settings changed"
assert not any(pathlib.Path(root).rglob("*.sock")), "owned socket residue"
assert not pathlib.Path(root,"home/.claude/.credentials.json").exists(), "owned credential copy survived cleanup"
plan=json.load(open(plan_path)); source=pathlib.Path(plan["credentialSource"]); b=source.read_bytes(); s=source.stat()
auth_before=json.load(open(auth_before_path)); auth_after={"sha256":hashlib.sha256(b).hexdigest(),"size":len(b),
 "mode":format(stat.S_IMODE(s.st_mode),"04o"),"mtimeNs":s.st_mtime_ns}
assert auth_before==auth_after, "credential source changed"
needles=(b"CLAUDE_CODE_MESSAGING_TOKEN",b"CLAUDE_CODE_MESSAGING_SOCKET",b"sk-ant-")
collector=pathlib.Path(root,"bin/collect-claude-public-jsonl")
source_integrity=json.load(open(pathlib.Path(root,"evidence/collector-source-integrity.json")))
assert not collector.is_symlink() and collector.is_file(), "collector source changed"
assert hashlib.sha256(collector.read_bytes()).hexdigest()==source_integrity["sha256"], "collector source changed"
for p in pathlib.Path(root).rglob("*"):
    # Only this exact unchanged generated source contains the denylist itself.
    # Runtime evidence, state, and every other regular file remain scanned.
    if p==collector: continue
    if p.is_file() and not p.is_symlink() and any(n in p.read_bytes() for n in needles): raise SystemExit("credential residue: "+str(p))
spec=json.load(open(input_path)); gate=json.load(open(gate_path)); reply=json.load(open(reply_path)); status=json.load(open(status_path)); qualification=json.load(open(qualification_path)); cleanup=json.load(open(cleanup_path))
claim=json.load(open(claim_path))
assert cleanup=={"version":1,"exactProviderBirthAbsent":True,"exactHelperBirthAbsent":True,"exactTmuxBirthAbsent":True,"activationLeaseDirAbsent":True,"exactClaimBirthAbsent":True,"credentialEnvPresent":False}
assert gate["credentialEnvPresent"] is False and gate["helperArgv"]=="projmux internal claude-endpoint-helper"
ref=spec["messageRef"]; env=reply["envelope"]
receipt={"version":1,"result":"pass","messageRef":ref,"replyMessageRef":env["messageRef"],
 "projectUID":spec["projectUID"],"windowUID":spec["windowUID"],
 "conversationRef":env["conversationRef"],"replyTo":env["replyTo"],"replyPayloadMarker":env["payload"],
 "sender":{"agentUID":spec["sender"]["agentUID"],"paneUID":spec["sender"]["paneUID"],"paneID":spec["sender"]["paneID"],
           "generation":spec["sender"]["generation"],"incarnation":status["source"]["incarnation"]},
 "receiver":{"agentUID":spec["receiver"]["agentUID"],"paneUID":spec["receiver"]["paneUID"],"paneID":spec["receiver"]["paneID"],
             "generation":spec["receiver"]["generation"],"incarnation":status["target"]["incarnation"]},
 "provider":{"pid":gate["providerPID"],"ownerUID":gate["providerOwnerUID"],"start":gate["providerStart"],
             "sessionID":gate["sessionID"],"version":gate["providerVersion"],"initCount":gate["initCount"],
             "tools":gate["tools"],"mcpServers":gate["mcpServers"],"plugins":gate["plugins"],
             "preInboundEventCount":gate["preInboundEventCount"],"preInboundToolUse":gate["preInboundToolUse"],
             "stderrBytes":gate["stderrBytes"],"sanitizedPreInboundStreamSHA256":gate["sanitizedProviderStreamSHA256"],
             "inboundPolicy":"accept"},
 "helper":{"pid":gate["helperProcess"]["pid"],"ownerUID":gate["helperProcess"]["ownerUID"],
           "start":gate["helperProcess"]["start"],"argv":gate["helperArgv"],"credentialEnvPresent":False},
 "codexSelfClaimProcess":{"pid":claim["pid"],"ownerUID":claim["ownerUID"],"start":claim["start"],"absentAfterCleanup":True},
 "capabilityBeforeQualification":gate["capabilityBeforeQualification"],
 "qualification":{"state":qualification["state"],"qualificationRef":qualification["qualificationRef"],
                  "providerVersion":qualification["providerVersion"],"evidence":qualification["evidence"],
                  "ambiguous":qualification["ambiguous"],"autoResend":qualification["autoResend"]},
 "candidateSHA256":gate["candidateSHA256"],
 "codexProviderState":{"beforeSHA256":codex_before,"afterSHA256":codex_after,"writes":0},
 "delivery":{"original":status["delivery"]["state"],"reply":reply["delivery"]["state"],"replyReason":reply["delivery"]["reason"]},
 "globalSettings":{"before":before,"after":after,"unchanged":True},
 "credentialSource":{"present":True,"size":auth_after["size"],"mode":auth_after["mode"],
                     "mtimeNs":auth_after["mtimeNs"],"unchanged":True},"ownedCredentialPresent":False,
 "agentsBefore":2,"agentsAfter":2,"providerToolUse":0,"connectorWrites":0,"externalWrites":0,"waiterProcesses":0,
 "exactProviderBirthAbsent":True,"exactHelperBirthAbsent":True,"exactTmuxBirthAbsent":True,
 "exactClaimBirthAbsent":True,"activationLeaseDirAbsent":True,
 "credentialResidue":0,"processResidue":0,"socketResidue":0,"registryResidue":0,"tmuxResidue":0,
 "globalSettingsUnchanged":True}
out_path=pathlib.Path(out); parent=out_path.parent
for component in (parent,*parent.parents):
    if stat.S_ISLNK(component.lstat().st_mode):
        trusted=platform.system()=="Darwin" and str(component) in ("/tmp","/var") and component.resolve(strict=True)==pathlib.Path("/private"+str(component))
        assert trusted, "canary receipt parent chain changed"
resolved=parent.resolve(strict=True)/out_path.name
assert resolved==pathlib.Path(plan["receiptPath"]) and not out_path.exists() and not out_path.is_symlink()
flags=os.O_WRONLY|os.O_CREAT|os.O_EXCL
if hasattr(os,"O_NOFOLLOW"): flags|=os.O_NOFOLLOW
fd=os.open(out_path,flags,0o600)
with os.fdopen(fd,"w") as stream: stream.write(json.dumps(receipt,sort_keys=True)+"\n")
print(json.dumps(receipt,sort_keys=True))
PY
