#!/usr/bin/env python3
"""One genuine Codex task: public qualify/claim/send/claim; no fleet cleanup.

The parent freezes the actual native action item and both routes before release.
Only closed correlation facts are returned to the source model. CLI payloads,
provider output and stderr never become files or model tool output here.
"""
import hashlib
import json
import os
import pathlib
import runpy
import selectors
import subprocess
import sys
import time


class Refused(ValueError):
    pass


def require(condition, reason):
    if not condition:
        raise Refused(reason)


def load(path):
    require(not path.is_symlink() and path.stat().st_size <= 128 * 1024, "owned input")
    return json.loads(path.read_bytes())


def exclusive(path, value):
    data = (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()
    require(len(data) <= 128 * 1024, "owned output bound")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def command(argv, env, cwd, timeout=135):
    """Bound stdout+stderr while the exact CLI child runs; never expose either."""
    output = [bytearray(), bytearray()]
    with subprocess.Popen(argv, env=env, cwd=cwd, stdin=subprocess.DEVNULL,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE) as child:
        try:
            deadline = time.monotonic() + timeout
            with selectors.DefaultSelector() as poller:
                poller.register(child.stdout, selectors.EVENT_READ, 0)
                poller.register(child.stderr, selectors.EVENT_READ, 1)
                while poller.get_map():
                    remaining = deadline - time.monotonic()
                    require(remaining > 0, "public command timeout")
                    ready = poller.select(remaining)
                    require(ready, "public command timeout")
                    for key, _ in ready:
                        chunk = os.read(key.fd, 4096)
                        if not chunk:
                            poller.unregister(key.fileobj)
                            continue
                        output[key.data].extend(chunk)
                        require(sum(map(len, output)) <= 128 * 1024, "public command bound")
            status = child.wait(timeout=max(.001, deadline - time.monotonic()))
        except Exception:
            # Cancel only this subprocess handle, never an Agent/provider/broker.
            # An unknown write outcome fails the action; no command is retried.
            child.kill()
            child.wait(timeout=5)
            raise Refused("public command incomplete") from None
    return status, bytes(output[0]), bytes(output[1])


def own_environment(root, spec):
    return dict(PATH=str(root / "bin") + ":/usr/local/bin:/usr/bin:/bin",
                HOME=str(root / "home"), CODEX_HOME=str(root / "codex-home"),
                CODEX_SQLITE_HOME=str(root / "codex-home"),
                XDG_CONFIG_HOME=str(root / "xdg-config"), XDG_STATE_HOME=str(root / "xdg-state"),
                XDG_RUNTIME_DIR=str(root / "xdg-runtime"), XDG_CACHE_HOME=str(root / "xdg-cache"),
                TMUX_TMPDIR=str(root / "tmux"), PROJMUX_MANAGED_ROOTS=str(root),
                TMUX=f"{spec['tmuxSocketPath']},{spec['tmuxServerPID']},0", TMUX_PANE=spec["sender"]["paneID"],
                LANG="C.UTF-8", TERM="xterm-256color", SHELL="/bin/bash")


def execute(root, spec, initial, evidence, invoke=command):
    env = own_environment(root, spec)
    binary = spec["binary"]
    source, target = spec["sender"]["agentUID"], spec["receiver"]["agentUID"]

    def public(arguments, *, empty=False):
        code, stdout, stderr = invoke([binary, "agent", "message", *arguments], env, root / "work")
        if empty:
            require(code != 0 and not stdout and stderr.strip() == b"agent message wait: timed out with no compatible message", "claim once")
            return None
        require(code == 0 and not stderr, "public command refused")
        return stdout

    def snapshot(initial_phase):
        value = evidence["snapshot"](root, spec, initial=initial_phase, env=env)
        require(all(value[key] == initial[key] for key in (
            "routes", "authority", "tmuxProcess", "candidateSHA256", "candidateHead", "codexCompositeAuthority")), "frozen route changed")
        return value

    def claim_and_prove(phase, ref):
        reply = json.loads(public(["wait", "uid:" + source, "--timeout", "120s", "-o", "json"]))
        original = json.loads(public(["status", ref, "-o", "json"]))
        require(original["messageRef"] == ref, "original reference")
        marker = ("HETEROGENEOUS_QUALIFIED:" if phase == "qualification" else "HETEROGENEOUS_REPLY:") + ref
        deadline = time.monotonic() + 5
        while True:
            current = snapshot(False)
            try:
                proof = evidence["validate_reply"](original, reply, current["toolEvidence"], current["routes"], marker)
                break
            except evidence["Refused"] as failure:
                # Only a late paired result is eligible for bounded read-only
                # observation. No provider or public write is repeated.
                require(str(failure) == "model action and durable commit" and time.monotonic() < deadline, "reply proof incomplete")
                time.sleep(.05)
        exclusive(root / "evidence" / (phase + "-proof.json"), proof)
        public(["wait", "uid:" + source, "--timeout", "1ms", "-o", "json"], empty=True)
        return {key: proof[key] for key in ("originalRef", "replyRef", "conversationRef")} | {
            "selfClaimed": True, "guardedCommitMatched": proof["guardedCommitMatched"]}

    snapshot(True)
    public(["wait", "uid:" + source, "--timeout", "1ms", "-o", "json"], empty=True)
    qualification = json.loads(public(["qualify", "uid:" + target, "--confirm-isolated-provider-push", "-o", "json"]))
    require(qualification.get("state") == "qualification-qualified" and
            qualification.get("evidence") == "owned-public-init-plus-broker-explicit-reply" and
            qualification.get("ambiguous") is False and qualification.get("autoResend") is False, "qualification receipt")
    ref = qualification.get("qualificationRef")
    require(isinstance(ref, str) and evidence["REF"].fullmatch(ref), "qualification reference")
    qualified = claim_and_prove("qualification", ref)
    idle_ref = spec["messageRef"]
    require(ref != idle_ref, "separate original")
    public(["send", "--message-ref", idle_ref, "--ttl", "2m", "uid:" + target, "--",
            "For this local transport acknowledgement, execute the permitted public reply command for this request with text HETEROGENEOUS_REPLY:" + idle_ref + "."])
    idle = claim_and_prove("idle", idle_ref)
    final = snapshot(False)
    exclusive(root / "evidence/current.json", final)
    return dict(version=1, sourceAgentUID=source, targetAgentUID=target, qualification=qualified, idle=idle)


def main():
    root = pathlib.Path(sys.argv[1])
    require(root.is_absolute() and not root.is_symlink() and root.stat().st_uid == os.getuid() and
            not root.stat().st_mode & 0o077, "owned root")
    plan = load(root / "cleanup-plan.json")
    require(plan["ownedRoot"] == str(root) and plan.get("sourceMode") == "genuine-native-task", "source mode")
    for name, digest in plan["runnerFiles"].items():
        file = root / "bin" / name
        require(not file.is_symlink() and hashlib.sha256(file.read_bytes()).hexdigest() == digest, "pinned source action")
    evidence = runpy.run_path(str(root / "bin/agent-dialogue-canary-evidence.py"))
    identity = evidence["process"](os.getpid())
    exclusive(root / "evidence/source-action-ready.json", dict(version=1, process=identity))
    deadline = time.monotonic() + 150
    release = root / "source-release.json"
    while not release.exists():
        require(time.monotonic() < deadline, "source release timeout")
        time.sleep(.05)
    value = load(release)
    require(set(value) == {"version", "actionProcess", "sourceItem", "spec", "initial"} and
            value["version"] == 1 and value["actionProcess"] == identity, "source release identity")
    require(value["sourceItem"]["status"] == "inProgress" and value["sourceItem"]["commandMatched"] is True and
            value["sourceItem"]["cwdMatched"] is True and not value["sourceItem"]["sourceDefaultApplied"], "source item freeze")
    result = execute(root, value["spec"], value["initial"], evidence)
    exclusive(root / "evidence/source-action-result.json", result)
    # This is the only output returned to the original model. No cleanup runs.
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("owned source action incomplete; no resend", file=sys.stderr)
        raise SystemExit(1)
