#!/usr/bin/env python3
"""Parent-owned native source lifecycle; no coordination sends or model input relay.

The only initial input is passed to the existing public create command. This
module observes that original task and releases its one source action. Raw
provider frames, arguments and auth values are never returned as evidence.
"""
import hashlib
import json
import os
import pathlib
import runpy
import shlex
import signal
import socket
import stat
import struct
import subprocess
import sys
import time


class Refused(ValueError):
    pass


def require(value, reason):
    if not value:
        raise Refused(reason)


def exclusive(path, value):
    data = (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()
    require(len(data) <= 128 * 1024, "owned-record-bound")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def load(path):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and
            stat.S_IMODE(info.st_mode) == 0o600 and info.st_size <= 128 * 1024, "owned-record")
    return json.loads(path.read_bytes())


def image(path):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and
            info.st_mode & 0o111 and not info.st_mode & 0o022, "owned-executable")
    return dict(device=info.st_dev, inode=info.st_ino, sha256=hashlib.sha256(path.read_bytes()).hexdigest())


def birth(pid):
    require(type(pid) is int and pid > 1, "process-pid")
    directory = pathlib.Path('/proc', str(pid))
    info = directory.stat()
    fields = (directory / 'stat').read_text().rsplit(')', 1)[1].split()
    require(info.st_uid == os.getuid() and fields[0] not in ('Z', 'X'), "process-owner-or-exit")
    boot = pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    return dict(pid=pid, ownerUID=info.st_uid, start='linux:' + boot + ':' + fields[19]), int(fields[1])


def exited(identity):
    directory=pathlib.Path('/proc',str(identity['pid']))
    try:
        info=directory.stat()
        fields=(directory/'stat').read_text().rsplit(')',1)[1].split()
    except (FileNotFoundError,ProcessLookupError):
        return True
    boot=pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    require(dict(pid=identity['pid'],ownerUID=info.st_uid,start='linux:'+boot+':'+fields[19])==identity, 'cleanup-process-replaced')
    return fields[0] in ('Z','X')


def endpoint_path(root):
    path = root / 'codex-home/app-server-control/app-server-control.sock'
    require(len(os.fsencode(path)) < 100, "native-socket-path-bound")
    return path


def source_command(root):
    return shlex.join(['python3', str(root / 'bin/agent-dialogue-source-action.py'), str(root)])


def source_prompt(root):
    return ('Perform this owned provider-dialogue transport task. Execute exactly one shell command, '
            'from ' + str(root / 'work') + ', and wait for it to finish: ' + source_command(root) +
            '. It waits for the parent to verify your own runtime and releases one public qualification, '
            'your own inbox claim, one idle request and your own reply claim. Do not execute any other '
            'command, create a subagent, read auth/config/history, use external tools, resend, or clean up. '
            'After its closed JSON result returns, reply DONE. A failure is terminal; do not repair or retry.')


def private_config(root):
    # Public config keys; effective native policy remains an actual gate. No
    # ambient config/history or keyring settings are copied into this profile.
    return ('cli_auth_credentials_store = "file"\napproval_policy = "never"\n'
            'sandbox_mode = "workspace-write"\nweb_search = "disabled"\n'
            'check_for_update_on_startup = false\n'
            '[sandbox_workspace_write]\nnetwork_access = false\n'
            'exclude_slash_tmp = true\nexclude_tmpdir_env_var = true\nwritable_roots = [' +
            json.dumps(str(root)) + ']\n')


def launch(root, plan, env, popen=subprocess.Popen):
    """Direct owned child, not a shared daemon/service control command."""
    executable = pathlib.Path(plan['codexBinary'])
    pinned = image(executable)
    require(pinned == plan['codexImage'], 'native-image-changed')
    path = endpoint_path(root)
    require(not path.exists() and not path.is_symlink(), 'native-socket-occupied')
    path.parent.mkdir(mode=0o700)
    argv = [str(executable), 'app-server', '--listen', 'unix://' + str(path)]
    child = popen(argv, env=env, cwd=root / 'work', stdin=subprocess.DEVNULL,
                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    # A launch handle is retained by setup even if any following capture fails.
    try:
        identity, parent = birth(child.pid)
        require(parent == os.getpid() and pathlib.Path('/proc', str(child.pid), 'exe').resolve() == executable,
                'native-launch-identity')
        record = dict(version=1, role='native-daemon', process=identity, parentPID=parent,
                      executable=str(executable), image=pinned, socketPath=str(path))
        exclusive(root / 'evidence/native-launch.json', record)
        return child, record
    except Exception:
        # Exact Popen handle only. Failure does not authorize a discovery scan.
        child.terminate()
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            raise Refused('native-launch-cleanup-unproven') from None
        raise


def current(record):
    identity, _ = birth(record['process']['pid'])
    executable = pathlib.Path(record['executable'])
    require(identity == record['process'] and image(executable) == record['image'] and
            pathlib.Path('/proc', str(identity['pid']), 'exe').resolve() == executable, 'native-process-changed')
    args = pathlib.Path('/proc', str(identity['pid']), 'cmdline').read_bytes().split(b'\0')
    require(args == [os.fsencode(executable), b'app-server', b'--listen',
                     os.fsencode('unix://' + record['socketPath']), b''], 'native-argv-changed')


def connect(record):
    current(record)
    path = pathlib.Path(record['socketPath'])
    info = path.lstat()
    require(stat.S_ISSOCK(info.st_mode) and info.st_uid == os.getuid(), 'native-socket-owner')
    identity = [info.st_dev, info.st_ino]
    if 'socketIdentity' in record:
        require(record['socketIdentity'] == identity, 'native-socket-replaced')
    connection = socket.socket(socket.AF_UNIX)
    try:
        connection.settimeout(5)
        connection.connect(str(path))
        pid, uid, _ = struct.unpack('3i', connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        require(pid == record['process']['pid'] and uid == os.getuid(), 'native-peer')
        current(record)
        after = path.lstat()
        require([after.st_dev, after.st_ino] == identity, 'native-socket-raced')
        return connection, identity
    except Exception:
        connection.close()
        raise


def ready(root, record, child, timeout=10):
    deadline = time.monotonic() + timeout
    path = pathlib.Path(record['socketPath'])
    while not path.exists():
        require(child.poll() is None and time.monotonic() < deadline, 'native-ready-deadline')
        time.sleep(.05)
    connection, identity = connect(record)
    connection.close()
    record = dict(record, socketIdentity=identity)
    exclusive(root / 'evidence/native-endpoint.json', record)
    return record


def initialize(raw, observation, root):
    # The public Unix endpoint uses WebSocket messages, not raw JSONL.
    try:
        schemas=observation['Schemas'](root/'bin/agent-dialogue-codex-schema')
        transport=runpy.run_path(str(root/'bin/agent-dialogue-websocket.py'))
        params=dict(clientInfo=dict(name='projmux-dialogue-observer',title='Owned dialogue observer',version='1'))
        schemas.validate('InitializeParams.json',params)
    except Exception:
        raise PolicyFailure('policy-schema') from None
    connection=transport['MessageConnection'](raw).upgrade()
    connection.settimeout(5)
    connection.sendall(json.dumps(dict(id=0,method='initialize',params=params),separators=(',',':')).encode()+b'\n')
    value=observation['decode'](connection.read_message(16384))
    require(isinstance(value,dict) and set(value)=={'id','result'} and type(value['id']) is int and value['id']==0,'initialize-envelope')
    try: schemas.validate('InitializeResponse.json',value['result'])
    except Exception: raise PolicyFailure('policy-schema') from None
    require(all(isinstance(v,str) and len(v)<=1024 for v in value['result'].values()),'initialize-bound')
    require(value['result']['codexHome']==str(root/'codex-home'),'initialize-owned-home')
    connection.sendall(b'{"method":"initialized","params":{}}\n')
    return connection


POLICY_FAILURE_CODES=frozenset(('policy-schema','policy-request','policy-value','policy-origin','policy-config','policy-socket'))


class PolicyFailure(Refused):
    def __init__(self,code):
        require(code in POLICY_FAILURE_CODES,'policy-failure-code')
        super().__init__(code)
        self.code=code


def read_native_policy(root,plan,endpoint):
    code='policy-schema'
    policy=None
    try:
        observation=runpy.run_path(str(root/'bin/agent-dialogue-codex-observation.py'))
        policy=runpy.run_path(str(root/'bin/agent-dialogue-native-policy.py'))
        code='policy-config'
        config=root/'codex-home/config.toml'
        info=config.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_uid==os.getuid() and stat.S_IMODE(info.st_mode)==0o600 and
                config.read_text()==private_config(root),'policy-owned-config-changed')
        code='policy-schema'
        reader=policy['PolicyReader'](root,pathlib.Path(plan['codexBinary']).parent.parent,root/'bin/agent-dialogue-config-schema',observation)
        code='policy-socket'
        connection,_=connect(endpoint)
        with connection:
            code='policy-request'
            connection=initialize(connection,observation,root)
            facts=reader.read(connection)
            code='policy-socket'
            current(endpoint)
            socket_info=pathlib.Path(endpoint['socketPath']).lstat()
            require([socket_info.st_dev,socket_info.st_ino]==endpoint['socketIdentity'],'policy-read-socket-changed')
            code='policy-config'
            require(config.lstat().st_ino==info.st_ino and config.read_text()==private_config(root),'policy-read-config-changed')
        return facts
    except PolicyFailure:
        raise
    except Exception as failure:
        if policy is not None and isinstance(failure,policy['Refused']):
            code=failure.code
        raise PolicyFailure(code) from None


def ancestry(identity, daemon):
    seen = set()
    while len(seen) < 32:
        value, parent = birth(identity['pid'])
        require(value == identity and value['pid'] not in seen, 'action-process-changed')
        if identity == daemon:
            return
        seen.add(value['pid'])
        require(parent > 1, 'action-foreign-ancestry')
        identity = birth(parent)[0]
    raise Refused('action-ancestry-bound')


def freeze_thread(root, spec):
    registry = load(pathlib.Path(spec['registryPath']))
    actor = spec['sender']
    matches = [p for p in registry['panes'] if p['metadata']['uid'] == actor['paneUID']]
    require(len(matches) == 1, 'source-pane')
    pane = matches[0]
    activation = pane['status']['activation']
    require(pane['metadata']['ownerRef'] == dict(kind='Agent', uid=actor['agentUID']) and
            activation['generation'] == actor['generation'] and activation['runtimeID'] == actor['paneID'], 'source-activation')
    native = activation['codex']
    require(native['threadId'] and native['turnId'], 'source-initial-turn')
    return native['threadId'], native['turnId']


def observe_action(root, spec, initial, stage=lambda _: None, preserve=lambda _: None):
    action = runpy.run_path(str(root / 'bin/agent-dialogue-source-action.py'))
    observation = runpy.run_path(str(root / 'bin/agent-dialogue-codex-observation.py'))
    evidence = runpy.run_path(str(root / 'bin/agent-dialogue-canary-evidence.py'))
    plan = load(root / 'cleanup-plan.json')
    endpoint = load(root / 'evidence/native-endpoint.json')
    thread_id, turn_id = freeze_thread(root, spec)
    reader = observation['ObservationReader'](observation['Schemas'](root / 'bin/agent-dialogue-codex-schema'),
        thread_id, turn_id, source_command(root), str(root / 'work'), frozenset(plan['sourceAllowedOrigins']))
    stage('source-freeze')
    ready_path = root / 'evidence/source-action-ready.json'
    deadline = time.monotonic() + 120
    while not ready_path.exists():
        current(endpoint)
        require(time.monotonic() < deadline, 'source-action-ready-deadline')
        time.sleep(.1)
    identity = load(ready_path)['process']
    ancestry(identity, endpoint['process'])
    require(pathlib.Path('/proc', str(identity['pid']), 'cmdline').read_bytes().split(b'\0')[1:] ==
            [os.fsencode(root / 'bin/agent-dialogue-source-action.py'), os.fsencode(root), b''], 'source-action-argv')

    def read(freeze=False):
        connection, _ = connect(endpoint)
        with connection:
            connection=initialize(connection, observation, root)
            peer = dict(pid=endpoint['process']['pid'], uid=os.getuid(), startTicks=endpoint['process']['start'].rsplit(':', 1)[1])
            return observation['OwnedReadConnection'](connection, peer, reader).read(freeze=freeze)

    facts = read(True)
    stage('policy-before-release')
    policy=read_native_policy(root,plan,endpoint)
    require(policy==load(root/'evidence/native-policy-before-source.json'),'policy-release-changed')
    exclusive(root/'evidence/native-policy-before-release.json',policy)
    preserve(dict(version=1,event='policy',phase='before-release',facts=policy))
    # Revalidate otherwise-current source/target immediately before first push.
    now = evidence['snapshot'](root, spec, initial=True, env=action['own_environment'](root, spec))
    require(now == initial, 'source-release-route-changed')
    ancestry(identity, endpoint['process'])
    exclusive(root / 'evidence/source-freeze.json', dict(version=1, actionProcess=identity,
              daemonProcess=endpoint['process'], sourceItem=facts, routes=initial['routes']))
    preserve(dict(version=1,event='source',phase='frozen',process=identity,item=facts,routes=initial['routes']))
    stage('source-release')
    exclusive(root / 'source-release.json', dict(version=1, actionProcess=identity, sourceItem=facts, spec=spec, initial=initial))
    stage('source-result')
    result_path = root / 'evidence/source-action-result.json'
    deadline = time.monotonic() + 300
    while not result_path.exists():
        ancestry(identity, endpoint['process'])
        require(time.monotonic() < deadline, 'source-result-deadline')
        time.sleep(.1)
    result = load(result_path)
    require(result['sourceAgentUID'] == spec['sender']['agentUID'] and result['targetAgentUID'] == spec['receiver']['agentUID'], 'source-result-route')
    expected = dict(version=1, sourceAgentUID=spec['sender']['agentUID'], targetAgentUID=spec['receiver']['agentUID'])
    for phase in ('qualification', 'idle'):
        proof = load(root / 'evidence' / (phase + '-proof.json'))
        expected[phase] = {key: proof[key] for key in ('originalRef', 'replyRef', 'conversationRef', 'guardedCommitMatched')} | {'selfClaimed': True}
    observation['validate_result'](result, expected)
    reader.set_expected_result(result)
    deadline = time.monotonic() + 30
    while True:
        facts = read()
        if facts['toolCompleted'] and facts['closedResultMatched'] and facts['turnCompleted']:
            break
        require(time.monotonic() < deadline, 'source-completion-deadline')
        time.sleep(.25)
    exclusive(root / 'evidence/source-observation.json', dict(version=1, sourceItem=facts, actionProcess=identity,
              daemonProcess=endpoint['process'], result=result, expectedOriginalThreadWrites=True))
    preserve(dict(version=1,event='source',phase='completed',process=identity,item=facts,routes=initial['routes']))
    return facts


def broker_roles(root, plan):
    """Kernel peers of this private discovery directory, never credential records."""
    directory = root / 'xdg-state/projmux/broker'
    if not directory.exists():
        return []
    require(not directory.is_symlink() and directory.stat().st_uid == os.getuid(), 'broker-directory')
    roles = []
    for path in sorted(directory.glob('cb-*.sock')):
        require(len(roles) < 4, 'broker-count')
        info = path.lstat()
        require(stat.S_ISSOCK(info.st_mode) and info.st_uid == os.getuid(), 'broker-socket')
        with socket.socket(socket.AF_UNIX) as connection:
            connection.settimeout(2)
            connection.connect(str(path))
            pid, uid, _ = struct.unpack('3i', connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            require(uid == os.getuid(), 'broker-peer')
            identity, _ = birth(pid)
            executable = pathlib.Path(plan['candidateBinary'])
            require(pathlib.Path('/proc', str(pid), 'exe').resolve() == executable and
                    image(executable)['sha256'] == plan['candidateSHA256'], 'broker-image')
            argv = pathlib.Path('/proc', str(pid), 'cmdline').read_bytes().split(b'\0')
            prefix = [os.fsencode(executable), b'internal', b'codex-broker', b'serve', b'--state-domain', os.fsencode(root / 'xdg-state/projmux')]
            require(argv[:6] == prefix and argv[-1:] == [b''], 'broker-private-domain')
            suffix = argv[6:-1]
            require(not suffix or (len(suffix) == 5 and suffix[0] == b'--endpoint-state-domain' and suffix[1] and
                    suffix[2] == b'--endpoint-generation' and suffix[3] and suffix[4] == b'--endpoint-default'), 'broker-endpoint-argv')
            require(birth(pid)[0] == identity and path.lstat().st_ino == info.st_ino, 'broker-raced')
            roles.append(dict(role='native-broker', process=identity, executable=str(executable),
                              image=image(executable), socketPath=str(path), socketIdentity=[info.st_dev, info.st_ino]))
    return roles


def cleanup_roles(root, plan, barrier):
    """Capture approved signal roles before any teardown; no root-substring kill."""
    roles = broker_roles(root, plan)
    launch_path = root / 'evidence/native-launch.json'
    if launch_path.exists():
        record = load(launch_path)
        require(record['role'] == 'native-daemon' and record['executable'] == plan['codexBinary'] and
                record['image'] == plan['codexImage'] and record['socketPath'] == str(endpoint_path(root)), 'native-cleanup-plan')
        if not exited(record['process']):
            current(record)
            roles.append(record)
        ready_path = root / 'evidence/source-action-ready.json'
        if ready_path.exists():
            identity = load(ready_path)['process']
            if not exited(identity):
                ancestry(identity, record['process'])
                require(pathlib.Path('/proc', str(identity['pid']), 'cmdline').read_bytes().split(b'\0')[1:] ==
                        [os.fsencode(root / 'bin/agent-dialogue-source-action.py'), os.fsencode(root), b''], 'cleanup-action-argv')
                roles.insert(0, dict(role='source-action', process=identity))
    for record in roles:
        barrier.track(record['process'])
    return roles


def terminate_roles(roles, barrier):
    for record in roles:
        identity = record['process']
        if exited(identity):
            continue
        tracked = barrier.writers.get(identity['pid'])
        require(tracked is not None and tracked[0] == identity, 'cleanup-role-not-captured')
        try:
            require(birth(identity['pid'])[0] == identity, 'cleanup-role-replaced')
            if record['role'] == 'native-daemon':
                current(record)
            elif record['role'] == 'native-broker':
                require(pathlib.Path('/proc', str(identity['pid']), 'exe').resolve() == pathlib.Path(record['executable']) and
                        image(pathlib.Path(record['executable'])) == record['image'], 'cleanup-broker-changed')
            signal.pidfd_send_signal(tracked[1], signal.SIGTERM)
        except (FileNotFoundError, ProcessLookupError):
            pass


def main():
    require(len(sys.argv)==4 and sys.argv[1]=='run', 'native-parent-command')
    root=pathlib.Path(sys.argv[2])
    plan=load(root/'cleanup-plan.json')
    require(plan['ownedRoot']==str(root) and plan['sourceMode']=='genuine-native-task', 'native-parent-root')
    for name,digest in plan['runnerFiles'].items():
        for folder in (pathlib.Path(__file__).parent,root/'bin'):
            path=folder/name
            require(not path.is_symlink() and hashlib.sha256(path.read_bytes()).hexdigest()==digest, 'native-parent-pin')
    setup=runpy.run_path(str(root/'bin/agent-dialogue-canary-setup.py'))
    evidence=runpy.run_path(str(root/'bin/agent-dialogue-canary-evidence.py'))
    audit=setup['audit_for_root'](root)
    spec=load(pathlib.Path(sys.argv[3]))
    initial=evidence['snapshot'](root,spec,initial=True,env=setup['isolated_environment'](root))
    exclusive(root/'evidence/initial.json',initial)
    observe_action(root,spec,initial,audit.stage,audit.append)


if __name__=='__main__':
    try:
        main()
    except Exception:
        print('owned native source observation failed; no resend',file=sys.stderr)
        raise SystemExit(1)
