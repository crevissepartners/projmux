"""Offline L20 teardown: exact owned writer births precede root deletion."""
import json
import os
import pathlib
import signal
import stat
import subprocess
import sys

# Reuse the actual canary's reviewed pidfd barrier unchanged. This file runs only
# from the repository fixture; the installed application never imports it.
_source = (pathlib.Path(__file__).resolve().parents[2] /
           "scripts/agent-dialogue-live-canary.sh").read_text()
_namespace = {"__name__": "dialogue_writer_library"}
exec(compile(_source.split("<<'WRITERS_PY' || return 1\n", 1)[1].split("\nWRITERS_PY\n", 1)[0],
             "dialogue-writer-library", "exec"), _namespace)


class FixtureWriterBarrier(_namespace["OwnedWriterBarrier"]):
    def capture(self, seeds=()):
        super().capture(seeds)
        # Encoded supervisor arguments need not contain the root. The cwd is a
        # read-only ownership observation; never read credentials/environment.
        for proc in pathlib.Path("/proc").iterdir():
            if not proc.name.isdigit():
                continue
            observed = self.observe(int(proc.name))
            if observed is None:
                continue
            try:
                cwd = os.readlink(proc / "cwd")
            except (FileNotFoundError, PermissionError, ProcessLookupError):
                continue
            if cwd == self.root or cwd.startswith(self.root + os.sep):
                self.track(observed[0])
        super().capture()


# Only this fixture namespace changes; actual canary behavior stays unchanged.
_namespace["OwnedWriterBarrier"] = FixtureWriterBarrier
close_owned_writers = _namespace["close_owned_writers"]


def signal_owned(barrier, identity):
    """A stored pidfd is usable only with the exact role-validated birth."""
    tracked = barrier.writers.get(identity["pid"])
    if tracked is None and barrier.observe(identity["pid"]) is None:
        return
    if tracked is None or tracked[0] != identity:
        raise RuntimeError("owned job birth changed; root retained")
    try:
        signal.pidfd_send_signal(tracked[1], signal.SIGTERM)
    except ProcessLookupError:
        pass


def cleanup(root, binary, tmux, socket_path, socket_identity, socket_name, project_uid,
            jobs, timeout=20):
    root = pathlib.Path(root)
    root_info = root.lstat()
    if root.is_symlink() or not root.is_absolute() or root_info.st_uid != os.getuid():
        raise RuntimeError("invalid owned root")
    binary = pathlib.Path(binary).resolve(strict=True)
    registry = root / "state/projmux/metadata/registry.json"
    socket = pathlib.Path(socket_path) if socket_path else None
    clean_env = dict(os.environ)
    clean_env.pop("TMUX", None)
    clean_env.pop("TMUX_PANE", None)

    def control(argv):
        return subprocess.run(argv, env=clean_env, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, timeout=10, check=False)

    def socket_state():
        if socket is None:
            return "absent"
        if root / "tmux" not in socket.parents:
            raise RuntimeError("socket outside owned root")
        try:
            info = socket.lstat()
        except FileNotFoundError:
            return "absent"
        if socket.is_symlink() or not stat.S_ISSOCK(info.st_mode):
            return "invalid"
        return f"{info.st_dev}:{info.st_ino}"

    def process(pid):
        observed = FixtureWriterBarrier.observe(pid)
        if observed is None:
            return None
        proc = pathlib.Path("/proc", str(pid))
        try:
            argv = [os.fsdecode(x) for x in (proc / "cmdline").read_bytes().split(b"\0") if x]
            exe = (proc / "exe").resolve(strict=True)
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            return None
        return observed, exe, argv

    seeds, stoppable = [], []
    state = socket_state()
    if state not in (socket_identity, "absent"):
        raise RuntimeError("owned socket replaced; root retained")
    if state != "absent":
        for args in (["display-message", "-p", "#{pid}"], ["list-panes", "-a", "-F", "#{pane_pid}"]):
            result = control([tmux, "-S", str(socket)] + args)
            if result.returncode:
                raise RuntimeError("owned tmux writer snapshot unavailable")
            for value in result.stdout.splitlines():
                if not value.isdigit():
                    raise RuntimeError("invalid owned tmux process")
                observed = FixtureWriterBarrier.observe(int(value))
                if observed is not None:
                    seeds.append(observed[0])
    for role, pid in jobs.items():
        item = process(pid) if pid else None
        if item is None:
            continue
        (identity, parent), exe, argv = item
        valid = parent == os.getppid()
        if role == "control":
            valid = valid and exe == root / "bin/codex" and argv[1:] == ["app-server", "fixture-control"]
        elif role == "binding":
            valid = valid and exe == root / "bin/codex" and argv[1:3] == ["dialogue-bind", str(registry)]
        elif role == "waiter":
            valid = valid and exe == binary and argv[1:4] == ["agent", "message", "wait"]
        else:
            valid = False
        if not valid:
            raise RuntimeError("owned job role changed; root retained")
        seeds.append(identity)
        stoppable.append(identity)
    brokers = []
    for proc in pathlib.Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        item = process(int(proc.name))
        if item is None:
            continue
        (identity, _), exe, argv = item
        if exe == binary and argv[1:6] == ["internal", "codex-broker", "serve", "--state-domain", str(root / "state/projmux")]:
            brokers.append(identity)
    if len(brokers) > 1:
        raise RuntimeError("ambiguous owned broker; root retained")
    seeds.extend(brokers)
    stoppable.extend(brokers)

    def teardown(barrier):
        # Capture above occurs before unregister can erase helper identities or
        # initiate the delayed termination/operation receipt writers.
        if project_uid:
            control([str(binary), "delete", "project", "uid:" + project_uid,
                     "--socket", socket_name, "--yes"])
        state = socket_state()
        if state == socket_identity and state != "absent":
            control([tmux, "-S", str(socket), "kill-server"])
        elif state != "absent":
            raise RuntimeError("owned socket replaced; root retained")
        for identity in stoppable:
            signal_owned(barrier, identity)

    proof = close_owned_writers(root, teardown, seeds, timeout)
    # Directory mtime changes while writers flush; compare inode/owner only.
    current = root.lstat()
    if (current.st_dev, current.st_ino, current.st_uid) != (root_info.st_dev, root_info.st_ino, root_info.st_uid):
        raise RuntimeError("owned root replaced; root retained")
    if registry.exists():
        metadata = json.loads(registry.read_text())
        for collection in ("projects", "controlSessions", "windows", "panes", "agents", "nameReservations"):
            if metadata.get(collection):
                raise RuntimeError("owned registry resources remain; root retained")
    state = socket_state()
    if state == socket_identity and state != "absent":
        socket.unlink()
    elif state != "absent":
        raise RuntimeError("owned socket replaced; root retained")
    if any(stat.S_ISSOCK(path.lstat().st_mode) for path in root.rglob("*")):
        raise RuntimeError("owned socket remains; root retained")
    proof.update(ownedRoot=str(root), rootIdentity=f"{root_info.st_dev}:{root_info.st_ino}",
                 socketPath=str(socket) if socket else "", socketIdentity=socket_identity,
                 order=["capture-births", "canonical-delete", "exact-owned-stop", "pidfd-exit-proof"])
    return proof


if __name__ == "__main__":
    try:
        proof = cleanup(*sys.argv[1:8], jobs=dict(zip(("control", "binding", "waiter"),
                                                    (int(value or "0") for value in sys.argv[8:11]))))
        print(json.dumps(proof, sort_keys=True))
    except Exception:
        print("dialogue cleanup could not prove owned writer exit; root retained", file=sys.stderr)
        raise SystemExit(1)
