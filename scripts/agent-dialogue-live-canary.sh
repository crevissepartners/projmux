#!/usr/bin/env bash
set -euo pipefail
umask 077

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
  real_codex="${PMX_DIALOGUE_REAL_CODEX_BIN:-}"
  credential_file="${PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE:-}"
  candidate_head="${PMX_DIALOGUE_CANDIDATE_HEAD:-}"
  [[ "$candidate_head" =~ ^[0-9a-f]{40}$ ]] || { echo "prepare requires PMX_DIALOGUE_CANDIDATE_HEAD" >&2; exit 2; }
  prepared_message_ref="${PMX_DIALOGUE_MESSAGE_REF:-message-heterogeneous-live-canary}"
  [[ "$binary" == /* && -x "$binary" && "$real_claude" == /* && -x "$real_claude" && "$real_codex" == /* && -x "$real_codex" &&
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
import json,pathlib,stat,sys
p=pathlib.Path(sys.argv[1]); s=p.stat()
print(json.dumps({"size":s.st_size,"mode":format(stat.S_IMODE(s.st_mode),"04o"),"mtimeNs":s.st_mtime_ns},sort_keys=True))
PY
  python3 - "$root" "$credential_file" "$binary" "$receipt_path" "$prepared_message_ref" "$candidate_head" "$real_claude" "$real_codex" "${BASH_SOURCE[0]}" >"$root/cleanup-plan.json" <<'PY'
import hashlib,json,pathlib,sys,time
print(json.dumps({"version":3,"ownedRoot":str(pathlib.Path(sys.argv[1]).resolve()),
                  "credentialSource":str(pathlib.Path(sys.argv[2]).resolve()),
                  "candidateBinary":str(pathlib.Path(sys.argv[3]).resolve()),
                  "claudeBinary":str(pathlib.Path(sys.argv[7]).resolve()),"codexBinary":str(pathlib.Path(sys.argv[8]).resolve()),
                  "claudeVersion":"2.1.263","codexVersion":"0.153.2",
                  "runnerFiles":{name:hashlib.sha256(pathlib.Path(sys.argv[9]).resolve().with_name(name).read_bytes()).hexdigest() for name in ("agent-dialogue-live-canary.sh","agent-dialogue-canary-setup.py","agent-dialogue-canary-evidence.py")},
                  "candidateHead":sys.argv[6],"candidateSHA256":hashlib.sha256(pathlib.Path(sys.argv[3]).read_bytes()).hexdigest(),
                  "receiptPath":str(pathlib.Path(sys.argv[4]).resolve()),"messageRef":sys.argv[5],
                  "preparedAtEpochNs":time.time_ns(),"cleanup":"delete exact Project; kill exact root-contained tmux server; remove owned credential; verify then remove owned root"},sort_keys=True))
PY
  chmod 0600 "$root/cleanup-plan.json"
  install -m 0600 "$credential_file" "$root/home/.claude/.credentials.json"
  ln -s "$binary" "$root/bin/projmux"
  ln -s "$real_claude" "$root/bin/claude"
  ln -s "$real_codex" "$root/bin/codex"
  prepare_complete=1
  trap - EXIT
  echo "prepared=$root cleanup-plan=$root/cleanup-plan.json receipt=$receipt_path"
  echo "Prepared for the canonical setup transaction; use agent-dialogue-canary-setup.py to cover actor creation failures with automatic cleanup." >&2
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

canary_env=(env -i LANG=C.UTF-8 TERM=xterm-256color SHELL=/bin/bash HOME="$root/home" CODEX_HOME="$root/codex-home" PATH="$root/bin:$PATH"
  XDG_CONFIG_HOME="$root/xdg-config" XDG_STATE_HOME="$root/xdg-state"
  XDG_RUNTIME_DIR="$root/xdg-runtime" XDG_CACHE_HOME="$root/xdg-cache"
  TMUX_TMPDIR="$root/tmux" PROJMUX_MANAGED_ROOTS="$root")
cleanup_done=0
wait_pid=""
cleanup_owned() {
  [[ "$cleanup_done" == 0 ]] || return 0
  printf '%s\n' 'attempted' >"$root/evidence/cleanup-attempted"
  local claim_pid=""
  if [[ -n "$wait_pid" ]] && jobs -p | grep -Fxq "$wait_pid"; then claim_pid="$wait_pid"; fi
  # Capture every owned writer before teardown, including pane supervisors
  # which append termination/operation receipts after their provider exits.
  python3 - "$root" "$binary" "$registry" "$socket_path" "$socket_identity" "$project_uid" "$socket_name" "$claim_pid" <<'WRITERS_PY' || return 1
import json,os,pathlib,selectors,signal,stat,subprocess,sys,time

class OwnedWriterBarrier:
    """Wait for exact captured Linux births; never signal a discovered process."""
    def __init__(self,root):
        if not hasattr(os,"pidfd_open"):
            raise RuntimeError("owned writer exit proof requires Linux pidfd")
        self.root=str(root); self.writers={}; self.excluded={os.getpid()}
        parent=os.getppid()
        while parent>1 and parent not in self.excluded:
            self.excluded.add(parent)
            item=self.observe(parent)
            if item is None: break
            parent=item[1]

    @staticmethod
    def observe(pid):
        try:
            path=pathlib.Path("/proc",str(pid)); info=path.stat()
            if info.st_uid!=os.getuid(): return None
            raw=(path/"stat").read_text(); end=raw.rfind(")"); fields=raw[end+1:].split()
            if end<0 or len(fields)<20 or fields[0] in ("Z","X"): return None
            boot=pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
            return {"pid":pid,"ownerUID":info.st_uid,"start":"linux:"+boot+":"+fields[19]},int(fields[1])
        except (FileNotFoundError,ProcessLookupError,PermissionError):
            return None

    def track(self,identity):
        pid=identity["pid"]
        if pid<=1 or pid in self.excluded: return
        if pid in self.writers:
            if self.writers[pid][0]!=identity:
                raise RuntimeError("owned writer PID replaced; root retained")
            return
        current=self.observe(pid)
        if current is None or current[0]!=identity: return
        try: fd=os.pidfd_open(pid,0)
        except ProcessLookupError: return
        current=self.observe(pid)
        if current is None or current[0]!=identity:
            os.close(fd); return
        self.writers[pid]=(identity,fd)

    def capture(self,seeds=()):
        for identity in seeds: self.track(identity)
        snapshot={}
        for path in pathlib.Path("/proc").iterdir():
            if not path.name.isdigit(): continue
            pid=int(path.name)
            if pid in self.excluded: continue
            observed=self.observe(pid)
            if observed is None: continue
            snapshot[pid]=observed
            try:
                cwd=os.readlink(path/"cwd")
                if cwd==self.root or cwd.startswith(self.root+os.sep): self.track(observed[0])
            except (FileNotFoundError,ProcessLookupError,PermissionError): pass
            try: args=(path/"cmdline").read_bytes().split(b"\0")
            except (FileNotFoundError,ProcessLookupError,PermissionError): continue
            # Exact path arguments, including --registry=<path>, rather than
            # substring process names. Values are never retained or printed.
            for raw in args:
                value=os.fsdecode(raw)
                if "=" in value: value=value.split("=",1)[1]
                if value==self.root or value.startswith(self.root+os.sep):
                    self.track(observed[0]); break
        # Retain births even when a captured supervisor later reparents. Its
        # already-running descendants are part of the same owned teardown.
        changed=True
        while changed:
            changed=False
            for pid,(identity,parent) in snapshot.items():
                if pid not in self.writers and parent in self.writers:
                    self.track(identity); changed=pid in self.writers or changed

    def wait(self,timeout,on_wait=lambda:None):
        deadline=time.monotonic()+timeout
        self.capture()
        on_wait()
        exited=set()
        while True:
            pending=[fd for _,fd in self.writers.values() if fd not in exited]
            if not pending:
                before=len(self.writers); self.capture()
                if len(self.writers)==before: return
                continue
            remaining=deadline-time.monotonic()
            if remaining<=0: raise RuntimeError("owned writer exit deadline; root retained")
            with selectors.DefaultSelector() as poller:
                for fd in pending: poller.register(fd,selectors.EVENT_READ)
                ready=poller.select(remaining)
                if not ready: raise RuntimeError("owned writer exit deadline; root retained")
                exited.update(key.fd for key,_ in ready)
            self.capture()

    def close(self):
        for _,fd in self.writers.values(): os.close(fd)


def close_owned_writers(root,teardown,seeds=(),timeout=20,on_wait=lambda:None):
    barrier=OwnedWriterBarrier(root)
    try:
        barrier.capture(seeds)  # Before delete intent or tmux teardown.
        teardown(barrier)
        barrier.wait(timeout,on_wait)
        return {"version":1,"allCapturedWriterBirthsAbsent":True,
                "writers":[identity for identity,_ in barrier.writers.values()]}
    finally:
        barrier.close()


def main():
    root=pathlib.Path(sys.argv[1]); binary,registry,socket_path,socket_identity,project_uid=sys.argv[2:7]
    clean_env=["env","-u","TMUX","-u","TMUX_PANE"]
    def control(args):
        return subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=10,check=False)
    if not socket_path:
        discovered=control(clean_env+["TMUX_TMPDIR="+str(root/"tmux"),"tmux","-L",sys.argv[7],"display-message","-p","#{socket_path}"])
        if discovered.returncode==0:
            path=pathlib.Path(discovered.stdout.decode().strip()); info=path.lstat()
            if root not in path.parents or path.is_symlink() or not stat.S_ISSOCK(info.st_mode) or info.st_uid!=os.getuid():
                raise RuntimeError("partial setup socket ownership; root retained")
            socket_path=str(path); socket_identity=f"{info.st_dev}:{info.st_ino}"
        elif any(stat.S_ISSOCK(path.lstat().st_mode) for path in (root/"tmux").rglob("*")):
            raise RuntimeError("partial setup socket cannot be identified; root retained")
    def socket_state():
        if not socket_path: return "absent"
        path=pathlib.Path(socket_path)
        try: info=path.lstat()
        except FileNotFoundError: return "absent"
        return f"{info.st_dev}:{info.st_ino}" if stat.S_ISSOCK(info.st_mode) and not path.is_symlink() else "invalid"
    seeds=[]
    current_socket=socket_state()
    if current_socket not in (socket_identity,"absent"):
        raise RuntimeError("owned tmux socket replaced; root retained")
    if current_socket==socket_identity:
        server=control(clean_env+["tmux","-S",socket_path,"display-message","-p","#{pid}"])
        panes=control(clean_env+["tmux","-S",socket_path,"list-panes","-a","-F","#{pane_pid}"])
        if server.returncode or panes.returncode:
            raise RuntimeError("owned tmux writer snapshot unavailable; root retained")
        for value in (server.stdout+panes.stdout).splitlines():
            if not value.isdigit(): raise RuntimeError("invalid owned pane process")
            observed=OwnedWriterBarrier.observe(int(value))
            if observed is not None: seeds.append(observed[0])
    # The registration helper is detached from its SessionStart parent. Its
    # exact birth is captured before unregister removes this Registry record.
    metadata=json.loads(pathlib.Path(registry).read_text()) if pathlib.Path(registry).exists() else {}
    if not project_uid:
        projects=metadata.get("projects",[])
        if len(projects)>1: raise RuntimeError("partial setup project inventory; root retained")
        if projects:
            item=projects[0]
            if pathlib.Path(item["spec"]["root"]).resolve()!=root/"work":
                raise RuntimeError("partial setup project root; root retained")
            project_uid=item["metadata"]["uid"]
    for pane in metadata.get("panes",[]):
        activation=pane.get("status",{}).get("activation") or {}
        authority=((activation.get("claude") or {}).get("registration") or {}).get("authority") or {}
        identity=authority.get("leaseProcess")
        if not isinstance(identity,dict): continue
        observed=OwnedWriterBarrier.observe(identity.get("pid",0))
        if observed is None or observed[0]!=identity: continue
        proc=pathlib.Path("/proc",str(identity["pid"]))
        if (proc/"exe").resolve()!=pathlib.Path(binary).resolve():
            raise RuntimeError("registered helper binary changed; root retained")
        if (proc/"cmdline").read_bytes().split(b"\0")[1:3]!=[b"internal",b"claude-endpoint-helper"]:
            raise RuntimeError("registered helper route changed; root retained")
        seeds.append(identity)
    claim=None
    if sys.argv[8]:
        pid=int(sys.argv[8]); observed=OwnedWriterBarrier.observe(pid)
        if observed is not None:
            if observed[1]!=os.getppid() or pathlib.Path("/proc",str(pid),"exe").resolve()!=pathlib.Path(binary).resolve():
                raise RuntimeError("claim waiter ownership changed; root retained")
            claim=observed[0]; seeds.append(claim)
    def teardown(barrier):
        if claim is not None:
            tracked=barrier.writers.get(claim["pid"])
            if tracked is not None and tracked[0]==claim:
                try: signal.pidfd_send_signal(tracked[1],signal.SIGTERM)
                except ProcessLookupError: pass
        env=["env","-i","LANG=C.UTF-8","TERM=xterm-256color","SHELL=/bin/bash","HOME="+str(root/"home"),"CODEX_HOME="+str(root/"codex-home"),
            "PATH="+str(root/"bin")+":"+os.environ.get("PATH",""),
            "XDG_CONFIG_HOME="+str(root/"xdg-config"),"XDG_STATE_HOME="+str(root/"xdg-state"),
            "XDG_RUNTIME_DIR="+str(root/"xdg-runtime"),"XDG_CACHE_HOME="+str(root/"xdg-cache"),
            "TMUX_TMPDIR="+str(root/"tmux"),"PROJMUX_MANAGED_ROOTS="+str(root)]
        if project_uid: control(env+[binary,"delete","project","uid:"+project_uid,"--socket",sys.argv[7],"--yes"])
        current=socket_state()
        if current==socket_identity:
            control(clean_env+["tmux","-S",socket_path,"kill-server"])
        elif current!="absent":
            raise RuntimeError("owned tmux socket replaced; root retained")
    try:
        proof=close_owned_writers(root,teardown,seeds)
        (root/"evidence/cleanup-writers.json").write_text(json.dumps(proof,sort_keys=True)+"\n")
    finally:
        (root/"home/.claude/.credentials.json").unlink(missing_ok=True)

if __name__=="__main__":
    try: main()
    except Exception:
        # Never print argv, environment, or raw exception values.
        print("owned writer cleanup failed; root retained",file=sys.stderr)
        raise SystemExit(1)
WRITERS_PY
  if [[ -n "$wait_pid" ]]; then wait "$wait_pid" 2>/dev/null || true; fi
  cleanup_done=1
}
finish_cleanup() {
  local status=$?
  if ! cleanup_owned; then
    rm -f -- "$root/home/.claude/.credentials.json"
    echo "canary cleanup could not prove owned writer exit; root retained" >&2
    return 1
  fi
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
  if [[ "$current_root_identity" != "$root_identity" ]]; then
    echo "canary cleanup root identity changed; root retained" >&2
    return 1
  fi
  rm -rf -- "$root" || return 1
  if [[ "$status" == 0 && -n "${canary_receipt_json:-}" ]]; then
    python3 - "$receipt_path" "$root" "$canary_receipt_json" <<'RECEIPT_PY'
import json,os,pathlib,sys
path=pathlib.Path(sys.argv[1]); root=pathlib.Path(sys.argv[2])
assert not root.exists() and not root.is_symlink()
assert path.is_absolute() and root not in path.parents and not path.exists() and not path.is_symlink()
for parent in path.parents: assert not parent.is_symlink()
receipt=json.loads(sys.argv[3]); receipt['ownedRootAbsent']=True
fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
with os.fdopen(fd,'w') as stream: stream.write(json.dumps(receipt,sort_keys=True)+'\n')
print(json.dumps(receipt,sort_keys=True))
RECEIPT_PY
  fi
  return "$status"
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
evidence_helper="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/agent-dialogue-canary-evidence.py"
inside() {
  "${canary_env[@]}" TMUX="$socket_path,$server_pid,0" TMUX_PANE="$sender_pane_id" "$binary" "$@"
}
evidence() { "${canary_env[@]}" python3 "$evidence_helper" "$1" "$root" "$input" "${@:2}"; }

# This is read-only until the one qualification command below. The current
# observer verifies public init, exact memory guard, tools=Bash and MCP/plugins0.
python3 - "$root/cleanup-plan.json" "$evidence_helper" <<'PYPIN'
import hashlib,json,pathlib,sys
plan=json.load(open(sys.argv[1])); folder=pathlib.Path(sys.argv[2]).parent
assert all(hashlib.sha256((folder/name).read_bytes()).hexdigest()==digest for name,digest in plan['runnerFiles'].items())
PYPIN
evidence initial
codex_state_snapshot() {
  python3 - "$root/codex-home" <<'PYCODEX'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1]); rows=[]
for path in sorted(root.rglob('*')):
    # Authentication values/hashes are never coordination evidence.
    if path.name in ('auth.json','.credentials.json'): continue
    if path.is_symlink(): raise SystemExit('unexpected Codex state symlink')
    if path.is_file():
        info=path.stat(); rows.append([str(path.relative_to(root)),info.st_size,info.st_mtime_ns,hashlib.sha256(path.read_bytes()).hexdigest()])
print(hashlib.sha256(json.dumps(rows,separators=(',',':')).encode()).hexdigest())
PYCODEX
}
codex_state_before="$(codex_state_snapshot)"
# An empty claim proves current source admission through the public runtime and
# live Codex composite authority checks. An unexpected inbox item aborts this run.
assert_empty_claim() {
  local output="$root/evidence/empty-claim.json" diagnostic="$root/evidence/empty-claim.stderr"
  if inside agent message wait "uid:$sender_uid" --timeout 1ms -o json >"$output" 2>"$diagnostic"; then
    echo "unexpected Codex inbox item" >&2
    return 1
  fi
  [[ ! -s "$output" ]] && grep -Fxq 'agent message wait: timed out with no compatible message' "$diagnostic"
}
assert_empty_claim

# Qualification is the first and only pre-admission push. Its actual model
# action must call the public reply leaf; no Stop, prompt or tool-output shortcut.
inside agent message qualify "uid:$receiver_uid" --confirm-isolated-provider-push -o json >"$root/evidence/qualification-receipt.json"
qualification_ref="$(python3 - "$root/evidence/qualification-receipt.json" <<'PY'
import json,re,sys
value=json.load(open(sys.argv[1])); ref=value.get('qualificationRef','')
assert value.get('state')=='qualification-qualified' and re.fullmatch(r'[A-Za-z0-9._:-]{1,160}',ref)
print(ref)
PY
)"
inside agent message wait "uid:$sender_uid" --timeout 5s -o json >"$root/evidence/qualification-reply.json"
inside agent message status "$qualification_ref" -o json >"$root/evidence/qualification-status.json"
collect_reply_proof() {
  local kind="$1" i
  # Only read-only snapshots are retried. A lost/invalid observer aborts at once;
  # late paired tool results have a bounded observation window, never a resend.
  for ((i=0; i<100; i++)); do
    evidence current || return 1
    if evidence reply "$kind" 2>"$root/evidence/reply-proof.stderr"; then return 0; fi
    sleep 0.05
  done
  echo "model tool/result/commit evidence is incomplete; no resend" >&2
  return 1
}
collect_reply_proof qualification
assert_empty_claim

# General traffic starts only after the independent qualification proof/claim.
# The model receives one harmless self-contained request and selects the ref.
inside agent message send --message-ref "$message_ref" --ttl 2m "uid:$receiver_uid" -- \
  "For this local transport acknowledgement, execute the permitted public reply command for this request with text HETEROGENEOUS_REPLY:$message_ref." >"$root/evidence/idle-send.txt"
inside agent message wait "uid:$sender_uid" --timeout 120s -o json >"$root/evidence/idle-reply.json"
inside agent message status "$message_ref" -o json >"$root/evidence/idle-status.json"
collect_reply_proof idle
assert_empty_claim
evidence current
codex_state_after="$(codex_state_snapshot)"
[[ "$codex_state_before" == "$codex_state_after" ]] || { echo "Codex provider state changed during coordination" >&2; exit 1; }

# Compare credential source/copy bytes only in this process. Neither values nor
# credential hashes are written to evidence. Snapshot metadata contains no secret.
python3 - "$root" <<'PY'
import json,pathlib,stat,sys
root=pathlib.Path(sys.argv[1]); plan=json.load(open(root/'cleanup-plan.json'))
source=pathlib.Path(plan['credentialSource']); info=source.stat()
assert source.read_bytes()==(root/'home/.claude/.credentials.json').read_bytes()
assert json.load(open(root/'evidence/auth-source-before.json'))==dict(size=info.st_size,mode=format(stat.S_IMODE(info.st_mode),'04o'),mtimeNs=info.st_mtime_ns)
(root/'evidence/auth-unchanged.json').write_text('{"unchanged":true}\n')
PY
cleanup_owned

# The same cleanup path runs on every failure. Success needs automatic writer
# exit proof, empty owned Registry/sockets/profile, and unchanged ambient settings.
canary_receipt_json="$(python3 - "$root" "$input" "$(settings_snapshot)" <<'PY'
import json,os,pathlib,stat,sys
root=pathlib.Path(sys.argv[1]); spec=json.load(open(sys.argv[2])); current=json.loads(sys.argv[3])
assert current==json.load(open(root/'evidence/global-settings-before.json'))
assert json.load(open(root/'evidence/auth-unchanged.json'))=={'unchanged':True}
writers=json.load(open(root/'evidence/cleanup-writers.json'))
assert writers['allCapturedWriterBirthsAbsent'] is True
registry=pathlib.Path(spec['registryPath'])
if registry.exists():
    reg=json.load(open(registry))
    assert all(not reg.get(key) for key in ('projects','windows','panes','agents','controlSessions','nameReservations'))
assert not (root/'home/.claude/.credentials.json').exists()
assert not any(stat.S_ISSOCK(path.lstat().st_mode) for path in root.rglob('*'))
initial=json.load(open(root/'evidence/initial.json')); final=json.load(open(root/'evidence/current.json'))
lease=pathlib.Path(initial['activationLeaseDir'])
assert not lease.exists(), 'activation lease remains after automatic cleanup'
profile=root/'xdg-state/projmux/claude-dialogue'
assert not profile.exists() or not any(profile.iterdir()), 'profile remains after automatic cleanup'
needles=(b'CLAUDE_CODE_MESSAGING_TOKEN',b'CLAUDE_CODE_MESSAGING_SOCKET',b'sk-ant-')
for path in root.rglob('*'):
    if path.is_file() and not path.is_symlink():
        assert not any(needle in path.read_bytes() for needle in needles), 'credential residue'
qualification=json.load(open(root/'evidence/qualification-proof.json')); idle=json.load(open(root/'evidence/idle-proof.json'))
assert qualification['originalRef']!=idle['originalRef'] and qualification['toolUseID']!=idle['toolUseID']
assert len(final['toolEvidence'])==2
receipt=dict(version=2,result='qualification-and-idle-pass',candidateHead=initial['candidateHead'],candidateSHA256=initial['candidateSHA256'],
    projectUID=spec['projectUID'],windowUID=spec['windowUID'],routes=initial['routes'],
    provider=dict(version=initial['profile']['claude_code_version'],sessionId=initial['authority']['sessionId'],process=initial['authority']['process']),
    helperProcess=initial['authority']['leaseProcess'],tmuxProcess=initial['tmuxProcess'],
    effectiveTools=['Bash'],mcpServers=[],plugins=[],preInboundToolUse=0,
    qualification=qualification,idle=idle,codexClaimAuthority='public-runtime-owner-and-live-composite-route',
    claimOnce=True,readOnlyEvidence=True,codexProviderStateUnchanged=True,automaticCleanup=writers,credentialResidue=0,
    globalSettingsUnchanged=True,credentialSourceUnchanged=True,ownedCredentialAbsent=True,
    activationLeaseAbsent=True,ownedProfileAbsent=True,ownedSocketAbsent=True,
    unverified=['active-tool-overlap','model-visible-human-overlap','multiple-ordinary-requests','same-UID-recovery','installed-smoke'])
print(json.dumps(receipt,sort_keys=True))
PY
)"
# EXIT verifies the original root incarnation, removes it once after writer
# closure, then publishes the external receipt. A removal failure is not PASS.
