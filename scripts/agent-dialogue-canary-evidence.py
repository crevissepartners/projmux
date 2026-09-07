#!/usr/bin/env python3
"""Bounded, read-only evidence for the owned public dialogue canary.

Only the existing Projmux coordination socket is read. No vendor socket,
transcript, tool command, model content, or messaging credential is collected.
"""
import datetime
import hashlib
import json
import os
import pathlib
import re
import socket
import stat
import struct
import subprocess
import sys
import time

MAX_FRAME = 64 * 1024
REF = re.compile(r"[A-Za-z0-9._:-]{1,160}\Z")


class Refused(ValueError):
    """Only a closed, content-free reason may cross the diagnostic boundary."""


def require(condition, reason):
    if not condition:
        raise Refused(reason)


def load(path, limit=4 * 1024 * 1024):
    with open(path, "rb") as stream:
        data = stream.read(limit + 1)
    require(len(data) <= limit, "evidence size")
    return json.loads(data)


def dump(path, value):
    with open(path, "w", encoding="utf-8") as stream:
        json.dump(value, stream, sort_keys=True)
        stream.write("\n")
    os.chmod(path, 0o600)


def process(pid):
    require(type(pid) is int and pid > 1, "process ID")
    root = pathlib.Path("/proc", str(pid))
    fields = (root / "stat").read_text().rsplit(")", 1)[1].split()
    require(len(fields) >= 20 and fields[0] not in ("Z", "X"), "process exited")
    owner = int(next(line.split()[1] for line in (root / "status").read_text().splitlines() if line.startswith("Uid:")))
    require(owner == os.getuid(), "process owner")
    boot = pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    return dict(pid=pid, ownerUID=owner, start="linux:" + boot + ":" + fields[19])


def exact_process(identity):
    require(isinstance(identity, dict) and set(identity) == {"pid", "ownerUID", "start"}, "process shape")
    require(process(identity["pid"]) == identity, "process birth changed")


def owned(path, root, kind):
    path = pathlib.Path(path)
    require(path.is_absolute() and root in path.parents and not path.is_symlink(), "owned path")
    info = path.lstat()
    require(info.st_uid == os.getuid() and not info.st_mode & 0o077, "owned mode")
    require((kind == "file" and stat.S_ISREG(info.st_mode)) or (kind == "socket" and stat.S_ISSOCK(info.st_mode)), "owned type")
    return info


def command(argv, *, timeout=5, env=None):
    env = dict(os.environ if env is None else env)
    env.pop("TMUX", None)
    env.pop("TMUX_PANE", None)
    result = subprocess.run(argv, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
    require(result.returncode == 0 and len(result.stdout) <= 4 * 1024 * 1024 and not result.stderr, "read-only command failed")
    return result.stdout


def runtime_chain(spec, registry, pane_runtime):
    projects = {row["metadata"]["uid"]: row for row in registry.get("projects", [])}
    windows = {row["metadata"]["uid"]: row for row in registry.get("windows", [])}
    agents = {row["metadata"]["uid"]: row for row in registry.get("agents", [])}
    panes = registry.get("panes", [])
    require(len(projects) == 1 and spec["projectUID"] in projects and len(agents) == 2, "private inventory")
    require(windows[spec["windowUID"]]["metadata"]["ownerRef"] == dict(kind="Project", uid=spec["projectUID"]), "Window owner")
    result = {}
    for role, provider in (("sender", "codex"), ("receiver", "claude")):
        expected = spec[role]
        require(all(isinstance(expected.get(key), str) and REF.fullmatch(expected[key]) for key in ("agentUID", "paneUID", "generation")), "actor identifiers")
        require(re.fullmatch(r"%[0-9]+", expected["paneID"]) is not None, "pane runtime")
        # Resolve runtime first, then follow the owner chain in this exact scope.
        matches = [p for p in panes if (p.get("status", {}).get("activation") or {}).get("runtimeID") == expected["paneID"]]
        require(len(matches) == 1, "ambiguous runtime")
        pane = matches[0]
        require(pane["metadata"]["uid"] == expected["paneUID"] and pane["metadata"]["ownerRef"] == dict(kind="Agent", uid=expected["agentUID"]), "Pane owner")
        agent = agents[expected["agentUID"]]
        activation = pane["status"]["activation"]
        require(agent["metadata"]["ownerRef"] == dict(kind="Window", uid=spec["windowUID"]), "Agent scope")
        require(agent["status"]["paneRef"] == expected["paneUID"] and agent["status"]["phase"] == "Running" and agent["spec"]["provider"] == provider, "Agent activation")
        require(activation["agentUID"] == expected["agentUID"] and activation["generation"] == expected["generation"], "activation generation")
        require(pane_runtime.get(expected["paneID"]) == expected["paneUID"], "tmux binding")
        result[role] = activation
    return result



def target_authority(authority):
    require(isinstance(authority, dict) and set(authority) == {"sessionId", "process", "registrationGeneration", "leaseProcess"}, "authority fields")
    exact_process(authority["process"])
    exact_process(authority["leaseProcess"])
    require(all(isinstance(authority[key], str) and REF.fullmatch(authority[key]) for key in ("sessionId", "registrationGeneration")), "authority identifiers")
    return authority

def coordination_target(spec, authority):
    # Preserve the repository's struct field order when deriving its private UDS.
    def ordered_process(value):
        return {key: value[key] for key in ("pid", "ownerUID", "start")}
    require(all(isinstance(authority.get(key), str) and REF.fullmatch(authority[key]) for key in ("sessionId", "registrationGeneration")), "authority identifiers")
    ordered = dict(sessionId=authority["sessionId"], process=ordered_process(authority["process"]),
                   registrationGeneration=authority["registrationGeneration"], leaseProcess=ordered_process(authority["leaseProcess"]))
    receiver = spec["receiver"]
    return dict(agentUID=receiver["agentUID"], paneUID=receiver["paneUID"], generation=receiver["generation"], provider="claude", authority=ordered)


def profile_response(spec, target):
    encoded = json.dumps(target, separators=(",", ":"), ensure_ascii=False).encode()
    receiver = spec["receiver"]
    seed = spec["registryPath"] + "\0" + receiver["paneUID"] + "\0" + receiver["generation"]
    directory = pathlib.Path("/tmp", "pmx-ce-" + hashlib.sha256(seed.encode()).hexdigest()[:32])
    info = directory.lstat()
    require(not directory.is_symlink() and stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700, "coord directory")
    path = directory / ("coord-" + hashlib.sha256(encoded).hexdigest()[:24] + ".sock")
    before = path.lstat()
    require(not path.is_symlink() and stat.S_ISSOCK(before.st_mode) and before.st_uid == os.getuid() and stat.S_IMODE(before.st_mode) == 0o600, "coord socket")
    helper = target["authority"]["leaseProcess"]
    exact_process(helper)
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        connection.settimeout(5)
        connection.connect(str(path))
        peer_pid, peer_uid, _ = struct.unpack("3i", connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        require(peer_pid == helper["pid"] and peer_uid == helper["ownerUID"], "coord peer")
        exact_process(helper)
        require(path.stat().st_ino == before.st_ino and path.stat().st_dev == before.st_dev, "coord incarnation")
        connection.sendall(json.dumps(dict(version=5, operation="profile-evidence", target=target), separators=(",", ":")).encode())
        connection.shutdown(socket.SHUT_WR)
        data = b""
        while len(data) <= MAX_FRAME:
            chunk = connection.recv(min(4096, MAX_FRAME + 1 - len(data)))
            if not chunk:
                break
            data += chunk
        require(len(data) <= MAX_FRAME, "coord frame bound")
    exact_process(helper)
    response = json.loads(data)
    require(response.get("version") == 5 and response.get("kind") == "profile-evidence" and response.get("autoResend") is False and not response.get("ambiguous", False), "current observer unavailable")
    return response, str(directory)


def validate_profile(response, spec, authority, incarnation, *, initial):
    require(not set(response) - {"version", "kind", "profileEvidence", "toolEvidence", "autoResend", "ambiguous"}, "profile response fields")
    evidence = response["profileEvidence"]
    allowed = {"version", "claude_code_version", "sessionId", "agentUID", "paneUID", "activationGeneration", "routeIncarnation", "providerProcess", "registrationGeneration", "helperProcess", "replyExecutionGate", "tools", "mcp_servers", "plugins", "pluginInitCount", "preMarkerToolUse", "preMarkerStderr", "inboundPolicy", "publicInitObserved", "streamFrozen", "observedAt"}
    require(isinstance(evidence, dict) and set(evidence) == allowed, "profile evidence fields")
    receiver = spec["receiver"]
    require(evidence.get("version") == 1 and evidence.get("claude_code_version") == "2.1.263", "current version")
    for key, value in dict(sessionId=authority["sessionId"], agentUID=receiver["agentUID"], paneUID=receiver["paneUID"], activationGeneration=receiver["generation"], routeIncarnation=incarnation,
                           providerProcess=authority["process"], helperProcess=authority["leaseProcess"], registrationGeneration=authority["registrationGeneration"]).items():
        require(evidence.get(key) == value, "profile route")
    require(evidence.get("replyExecutionGate") is True and evidence.get("tools") == ["Bash"] and evidence.get("mcp_servers") == [] and evidence.get("plugins") == [], "effective isolation")
    require(all(type(evidence.get(key)) is int and evidence[key] == 0 for key in ("pluginInitCount", "preMarkerToolUse", "preMarkerStderr")), "pre-inbound effects")
    require(evidence.get("publicInitObserved") is True and evidence.get("streamFrozen") is True and evidence.get("inboundPolicy") == "accept", "observed init")
    age = time.time() - datetime.datetime.fromisoformat(evidence["observedAt"].replace("Z", "+00:00")).timestamp()
    require(-5 <= age <= 300, "evidence age")
    actions = response.get("toolEvidence", [])
    require(isinstance(actions, list) and len(actions) <= 32 and (not initial or not actions), "tool evidence cardinality")
    for action in actions:
        allowed = {"toolUseID", "messageRef", "targetAgentUID", "resultObserved", "replyRef", "guardSelectionMatched", "guardedCommitMatched", "executionProcess", "executingAtRead"}
        require(isinstance(action, dict) and not set(action) - allowed, "tool evidence fields")
        require(all(isinstance(action.get(key), str) and REF.fullmatch(action[key]) for key in ("toolUseID", "messageRef", "targetAgentUID")), "tool evidence identifiers")
        require(all(type(action.get(key)) is bool for key in ("resultObserved", "guardSelectionMatched", "guardedCommitMatched", "executingAtRead")), "tool evidence booleans")
        execution = action.get("executionProcess")
        require(isinstance(execution, dict) and set(execution) == {"pid", "ownerUID", "start"} and type(execution["pid"]) is int and type(execution["ownerUID"]) is int and isinstance(execution["start"], str) and len(execution["start"]) <= 160, "execution process fields")
        require("replyRef" not in action or (isinstance(action["replyRef"], str) and REF.fullmatch(action["replyRef"])), "tool result ref")
    return evidence, actions


def snapshot(root, spec, *, initial, env=None):
    plan = load(root / "cleanup-plan.json")
    require(spec.get("version") == 2 and spec["binary"] == plan["candidateBinary"] and spec["messageRef"] == plan["messageRef"], "prepared candidate input")
    require(hashlib.sha256(pathlib.Path(spec["binary"]).read_bytes()).hexdigest() == plan["candidateSHA256"], "candidate changed")
    owned(spec["registryPath"], root, "file")
    sock = pathlib.Path(spec["tmuxSocketPath"])
    require(root in sock.parents and not sock.is_symlink() and stat.S_ISSOCK(sock.lstat().st_mode) and sock.stat().st_uid == os.getuid(), "owned tmux socket")
    server = command(["tmux", "-S", str(sock), "display-message", "-p", "#{pid}\t#{socket_path}"],env=env).decode().strip().split("\t")
    require(server == [str(spec["tmuxServerPID"]), str(sock)], "tmux server identity")
    rows = command(["tmux", "-S", str(sock), "list-panes", "-a", "-F", "#{pane_id}\t#{@projmux_pane_uid}"],env=env).decode().splitlines()
    runtime = dict(row.split("\t") for row in rows)
    activation = runtime_chain(spec, load(spec["registryPath"]), runtime)
    capabilities = {role: json.loads(command([spec["binary"], "agent", "capabilities", "uid:" + spec[role]["agentUID"], "-o", "json"],env=env)) for role in ("sender", "receiver")}
    routes = {}
    for role, provider in (("sender", "codex"), ("receiver", "claude")):
        cap = capabilities[role]
        require(isinstance(cap, dict) and not set(cap) - {"provider", "agent", "runtimeEligibility", "capabilities"}, "capability response fields")
        current = cap["runtimeEligibility"]
        require(isinstance(current, dict) and not set(current) - {"registryReady", "evidence", "liveVerified", "reason", "paneUID", "paneRuntimeID", "activationGeneration", "routeIncarnation", "stateDomainID", "endpointGenerationID", "brokerRuntimeID", "connectionEpoch", "bindingEpoch", "coordination"}, "capability runtime fields")
        actor = spec[role]
        require(cap["agent"]["uid"] == actor["agentUID"] and current["registryReady"] is True and current["activationGeneration"] == actor["generation"] and current["paneUID"] == actor["paneUID"], "capability route")
        require(REF.fullmatch(current["routeIncarnation"]) is not None, "route incarnation")
        routes[role] = dict(agentUID=actor["agentUID"], paneUID=actor["paneUID"], activationGeneration=actor["generation"], provider=provider, incarnation=current["routeIncarnation"])
    source = capabilities["sender"]["runtimeEligibility"]
    require(all(source.get(key) for key in ("stateDomainID", "endpointGenerationID", "brokerRuntimeID", "connectionEpoch", "bindingEpoch")), "Codex composite authority")
    authority = activation["receiver"]["claude"]["registration"]["authority"]
    require(activation["receiver"]["claude"]["registration"]["ready"] is True, "registration not ready")
    exact_process(authority["process"])
    exact_process(authority["leaseProcess"])
    helper = pathlib.Path("/proc", str(authority["leaseProcess"]["pid"]))
    require((helper / "exe").resolve() == pathlib.Path(spec["binary"]).resolve(), "helper candidate")
    require((helper / "cmdline").read_bytes().split(b"\0")[1:] == [b"internal", b"claude-endpoint-helper", b""], "helper fixed argv")
    keys = {entry.split(b"=", 1)[0] for entry in (helper / "environ").read_bytes().split(b"\0")}
    require(not keys.intersection({b"CLAUDE_CODE_MESSAGING_SOCKET", b"CLAUDE_CODE_MESSAGING_TOKEN"}), "helper credential environment")
    response, lease_dir = profile_response(spec, coordination_target(spec, authority))
    evidence, actions = validate_profile(response, spec, authority, routes["receiver"]["incarnation"], initial=initial)
    coordination = capabilities["receiver"]["runtimeEligibility"]["coordination"]
    require(coordination["eligible"] is (not initial), "qualification admission")
    composite = {key: source[key] for key in ("stateDomainID", "endpointGenerationID", "brokerRuntimeID", "connectionEpoch", "bindingEpoch")}
    require(all(isinstance(composite[key], str) and REF.fullmatch(composite[key]) for key in ("stateDomainID", "endpointGenerationID", "brokerRuntimeID")), "composite identity fields")
    require(all(type(composite[key]) is int and 0 < composite[key] < 2**64 for key in ("connectionEpoch", "bindingEpoch")), "composite epochs")
    return dict(version=2, routes=routes, authority=target_authority(authority), profile=evidence, toolEvidence=actions,
                codexCompositeAuthority=composite, tmuxProcess=process(spec["tmuxServerPID"]), activationLeaseDir=lease_dir,
                candidateSHA256=plan["candidateSHA256"], candidateHead=plan["candidateHead"])


def validate_reply(original, reply, evidence, routes, payload):
    envelope = reply["envelope"]
    require(original.get("version") == 2 and envelope.get("version") == 2, "public envelope version")
    require(isinstance(envelope.get("messageRef"), str) and REF.fullmatch(envelope["messageRef"]) and envelope["messageRef"] != original["messageRef"], "distinct reply ref")
    require(envelope.get("authority") == dict(kind="peer", trust="untrusted", permission="coordination-only"), "reply authority")
    require(original["source"] == routes["sender"] and original["target"] == routes["receiver"], "original routes")
    require(original["delivery"]["state"] == "delivered" and original["delivery"].get("outcomeUnknown", False) is False and original.get("autoResend", False) is False, "full frame delivery")
    require(envelope["source"] == routes["receiver"] and envelope["target"] == routes["sender"], "reply route reversal")
    require(original["conversationRef"] == "conversation-" + hashlib.sha256(original["messageRef"].encode()).hexdigest()[:36] and envelope["replyTo"] == original["messageRef"] and envelope["conversationRef"] == original["conversationRef"] and envelope["payload"] == payload, "semantic correlated reply")
    require(reply["delivery"]["state"] == "delivered" and reply["delivery"]["reason"] == "target-self-claim" and not reply["delivery"].get("outcomeUnknown", False), "Codex self claim")
    matches = [action for action in evidence if action.get("messageRef") == original["messageRef"]]
    require(len(matches) == 1, "exact model selection count")
    action = matches[0]
    require(action.get("targetAgentUID") == routes["sender"]["agentUID"] and action.get("replyRef") == envelope["messageRef"] and action.get("resultObserved") is True and action.get("guardSelectionMatched") is True and action.get("guardedCommitMatched") is True, "model action and durable commit")
    require(isinstance(action.get("toolUseID"), str) and REF.fullmatch(action["toolUseID"]) is not None, "model tool ID")
    return dict(originalRef=original["messageRef"], replyRef=envelope["messageRef"], conversationRef=envelope["conversationRef"],
                toolUseID=action["toolUseID"], selectionMatched=True, guardedCommitMatched=True, pairedResultObserved=True,
                delivered=True, semanticReplyMatched=True, exactCodexSelfClaim=True)


def main():
    operation, root_arg, input_arg = sys.argv[1:4]
    root = pathlib.Path(root_arg).resolve(strict=True)
    spec = load(input_arg)
    if operation in ("initial", "current"):
        result = snapshot(root, spec, initial=operation == "initial")
        if operation == "current":
            before = load(root / "evidence/initial.json")
            require(all(result[key] == before[key] for key in ("routes", "authority", "tmuxProcess", "candidateSHA256")), "frozen route changed")
        dump(root / "evidence" / (operation + ".json"), result)
    elif operation == "reply":
        kind = sys.argv[4]
        require(kind in ("qualification", "idle"), "reply case")
        current = load(root / "evidence/current.json")
        original = load(root / "evidence" / (kind + "-status.json"))
        reply = load(root / "evidence" / (kind + "-reply.json"))
        marker = ("HETEROGENEOUS_QUALIFIED:" if kind == "qualification" else "HETEROGENEOUS_REPLY:") + original["messageRef"]
        result = validate_reply(original, reply, current["toolEvidence"], current["routes"], marker)
        if kind == "qualification":
            receipt = load(root / "evidence/qualification-receipt.json")
            require(receipt["state"] == "qualification-qualified" and receipt["qualificationRef"] == original["messageRef"] and receipt["evidence"] == "owned-public-init-plus-broker-explicit-reply" and not receipt["ambiguous"] and not receipt["autoResend"], "qualification receipt")
        dump(root / "evidence" / (kind + "-proof.json"), result)
    else:
        raise ValueError("unknown evidence operation")


if __name__ == "__main__":
    try:
        main()
    except Refused as failure:
        print("dialogue canary evidence rejected: " + str(failure), file=sys.stderr)
        raise SystemExit(1)
    except Exception:
        # No raw provider outputs, credential values, command text or exceptions.
        print("dialogue canary evidence rejected", file=sys.stderr)
        raise SystemExit(1)
