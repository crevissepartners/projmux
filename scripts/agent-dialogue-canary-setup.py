#!/usr/bin/env python3
"""Owned setup transaction for the opt-in public qualification/idle runner."""
import hashlib
import json
import os
import pathlib
import re
import runpy
import shutil
import stat
import subprocess
import sys
import time
import uuid


STAGES = frozenset((
    'prepare', 'pins', 'claude-version', 'codex-version', 'native-launch', 'native-ready', 'policy-before-source', 'policy-before-release', 'source-freeze', 'source-release', 'source-result', 'tmux-create',
    'tmux-socket', 'socket-validation', 'tmux-server', 'tmux-project',
    'project-create', 'project-get', 'project-validation', 'reconcile', 'windows-get', 'window-validation',
    'sender-create', 'receiver-create', 'runtime-chain', 'input-write',
    'readiness', 'run', 'run-validation', 'initial-evidence', 'source-claim',
    'qualification', 'qualification-claim', 'qualification-proof',
    'idle-send', 'idle-claim', 'idle-proof', 'final-evidence',
    'cleanup', 'root-removal', 'complete'))


class Audit:
    """Bounded metadata only, outside the disposable root; never a PASS receipt."""
    def __init__(self, path, identity):
        self.path=pathlib.Path(path); self.identity=tuple(identity)

    @classmethod
    def create(cls, root, receipt):
        path=pathlib.Path(str(receipt)+'.audit.jsonl')
        if not path.is_absolute() or root==path or root in path.parents:
            raise ValueError('audit path')
        for parent in path.parents:
            if parent.is_symlink(): raise ValueError('audit parent')
        if receipt.exists() or receipt.is_symlink(): raise ValueError('receipt occupied')
        fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
        try: info=os.fstat(fd)
        finally: os.close(fd)
        return cls(path,(info.st_dev,info.st_ino))

    def descriptor(self):
        return dict(path=str(self.path),identity=list(self.identity))

    def append(self, record):
        fields={
            'stage':{'version','event','stage','exitCode','stdoutBytes','stderrBytes'},
            'cleanup':{'version','event','outcome','proofAvailable','proof','rootIdentity','rootAbsent'},
            'failure':{'version','event','reason','rootAbsent'},
            'binding':{'version','event','candidateHead','candidateSHA256','rootIdentity'},
            'terminal':{'version','event','exitCode','rootAbsent','receiptExists'},
            'source':{'version','event','phase','process','item','routes'},
            'policy':{'version','event','phase','facts'},
        }
        if record.get('event') not in fields or set(record)-fields[record['event']]: raise ValueError('audit event fields')
        data=(json.dumps(record,sort_keys=True,separators=(',',':'))+'\n').encode()
        if len(data)>128*1024: raise ValueError('audit record bound')
        fd=os.open(self.path,os.O_RDWR|os.O_APPEND|os.O_NOFOLLOW)
        try:
            info=os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_uid!=os.getuid() or stat.S_IMODE(info.st_mode)!=0o600 or info.st_nlink!=1 or (info.st_dev,info.st_ino)!=self.identity:
                raise ValueError('audit identity')
            if info.st_size+len(data)>1024*1024: raise ValueError('audit byte bound')
            existing=os.read(fd,1024*1024+1)
            if existing.count(b'\n')>=128: raise ValueError('audit record count')
            with os.fdopen(os.dup(fd),'ab') as stream:
                stream.write(data); stream.flush(); os.fsync(stream.fileno())
        finally: os.close(fd)

    def stage(self, stage, *, exit_code=None, stdout_bytes=None, stderr_bytes=None):
        if stage not in STAGES: raise ValueError('audit stage')
        row=dict(version=1,event='stage',stage=stage)
        for key,value in (('exitCode',exit_code),('stdoutBytes',stdout_bytes),('stderrBytes',stderr_bytes)):
            if value is not None:
                if type(value) is not int or not -(2**31)<=value<2**31: raise ValueError('audit numeric field')
                row[key]=value
        self.append(row)

    def cleanup(self, root, outcome):
        if outcome not in ('writers-exited','failed','root-removed','root-retained'): raise ValueError('cleanup outcome')
        row=dict(version=1,event='cleanup',outcome=outcome,proofAvailable=False)
        proof_path=root/'evidence/cleanup-writers.json'
        if proof_path.exists():
            if proof_path.is_symlink() or proof_path.stat().st_size>128*1024: raise ValueError('cleanup proof size')
            proof=json.loads(proof_path.read_text())
            if set(proof)!={'version','allCapturedWriterBirthsAbsent','writers'} or proof['version']!=1 or type(proof['allCapturedWriterBirthsAbsent']) is not bool:
                raise ValueError('cleanup proof fields')
            writers=proof['writers']
            if not isinstance(writers,list) or len(writers)>512: raise ValueError('cleanup writer bound')
            for item in writers:
                if not isinstance(item,dict) or set(item)!={'pid','ownerUID','start'} or type(item['pid']) is not int or item['pid']<=1 or type(item['ownerUID']) is not int or item['ownerUID']!=os.getuid() or not isinstance(item['start'],str) or re.fullmatch(r'linux:[a-f0-9-]{36}:[0-9]{1,20}',item['start']) is None:
                    raise ValueError('cleanup writer identity')
            row.update(proofAvailable=True,proof=proof)
        if outcome=='writers-exited' and not (row['proofAvailable'] and row['proof']['allCapturedWriterBirthsAbsent']):
            raise ValueError('cleanup proof missing')
        if root.exists():
            info=root.lstat(); row['rootIdentity']=[info.st_dev,info.st_ino]
        row['rootAbsent']=not root.exists()
        self.append(row)


def audit_for_root(root):
    plan=json.loads((root/'cleanup-plan.json').read_text())
    descriptor=plan['audit']
    expected=pathlib.Path(plan['receiptPath']+'.audit.jsonl')
    if pathlib.Path(descriptor['path'])!=expected or root in expected.parents: raise ValueError('audit plan')
    return Audit(expected,descriptor['identity'])


def isolated_environment(root):
    # No inherited provider config, messaging credentials or routing policy.
    return dict(PATH=str(root/'bin')+os.pathsep+os.environ.get('PATH','/usr/bin:/bin'),
                HOME=str(root/'home'),CODEX_HOME=str(root/'codex-home'),CODEX_SQLITE_HOME=str(root/'codex-home'),
                XDG_CONFIG_HOME=str(root/'xdg-config'),XDG_STATE_HOME=str(root/'xdg-state'),
                XDG_RUNTIME_DIR=str(root/'xdg-runtime'),XDG_CACHE_HOME=str(root/'xdg-cache'),
                TMUX_TMPDIR=str(root/'tmux'),PROJMUX_MANAGED_ROOTS=str(root),
                LANG='C.UTF-8',TERM='xterm-256color',SHELL='/bin/bash')


def cleanup_partial(root, binary, socket_name, socket_path='', socket_identity='', project='', *, on_wait=lambda: None, timeout=20):
    """The same pidfd writer barrier is used before and after run handoff."""
    source=(pathlib.Path(__file__).with_name('agent-dialogue-live-canary.sh')).read_text()
    body=source.split("<<'WRITERS_PY' || return 1\n",1)[1].split('\nWRITERS_PY\n',1)[0]
    namespace={'__name__':'owned_setup_cleanup'}
    exec(compile(body,'owned-canary-cleanup','exec'),namespace)
    close=namespace['close_owned_writers']
    namespace['close_owned_writers']=lambda root,teardown,seeds=(),**kwargs: close(root,teardown,seeds,timeout=timeout,on_wait=on_wait,**kwargs)
    arguments=sys.argv
    try:
        sys.argv=['cleanup',str(root),binary,str(root/'xdg-state/projmux/metadata/registry.json'),socket_path,socket_identity,project,socket_name,'']
        namespace['main']()
    finally:
        sys.argv=arguments


def finish_setup_failure(root, binary, socket_name, identity, audit=None, **test_options):
    before=root.lstat()
    if (before.st_dev,before.st_ino)!=identity or root.is_symlink(): raise ValueError('root changed')
    try:
        cleanup_partial(root,binary,socket_name,**test_options)
        if audit is not None: audit.cleanup(root,'writers-exited')
    except Exception:
        if audit is not None: audit.cleanup(root,'failed')
        raise
    finally:
        after=root.lstat()
        if (after.st_dev,after.st_ino)!=identity or root.is_symlink(): raise ValueError('root changed')
        directory=root/'home/.claude'
        if directory.is_symlink() or (root/'home').is_symlink(): raise ValueError('credential parent changed')
        (directory/'.credentials.json').unlink(missing_ok=True)
        if (root/'codex-home').is_symlink(): raise ValueError('Codex credential parent changed')
        (root/'codex-home/auth.json').unlink(missing_ok=True)
    if audit is not None: audit.stage('root-removal')
    shutil.rmtree(root)
    if audit is not None: audit.cleanup(root,'root-removed')


def setup(root, binary, socket_name, invoke, stage=lambda _: None, *, source_prompt=None):
    """Only public tmux/project/create routes; no fabricated Codex binding."""
    project=invoke('project-create',[binary,'create','project','--root',str(root/'work'),'--name','dialogue-canary','-o','uid']).strip()
    project_json=invoke('project-get',[binary,'get','projects','--project','uid:'+project,'-o','json'])
    stage('project-validation')
    projects=json.loads(project_json)['items']
    if len(projects)!=1: raise ValueError('unique owned Project')
    item=projects[0]
    if item.get('kind')!='Project' or item['metadata']['uid']!=project or item['spec']['root']!=str(root/'work'):
        raise ValueError('owned Project projection')
    session_name=item['status']['session']['name']
    if not isinstance(session_name,str) or not session_name.strip() or len(session_name.encode())>256 or session_name in ('.','..') or '/' in session_name or '\\' in session_name or any(ord(char)<32 for char in session_name):
        raise ValueError('Project session projection')
    # Registration already selected this physical projection. Creating a
    # differently named session would create a conflicting Project root claim.
    anchor=invoke('tmux-create',['tmux','-L',socket_name,'new-session','-d','-P','-F','#{pane_id}','-s',session_name,'-c',str(root/'work'),'sleep','3600']).strip()
    socket_path=invoke('tmux-socket',['tmux','-L',socket_name,'display-message','-p','-t',anchor,'#{socket_path}']).strip()
    stage('socket-validation')
    path=pathlib.Path(socket_path)
    info=path.lstat()
    if root not in path.parents or path.is_symlink() or not stat.S_ISSOCK(info.st_mode) or info.st_uid!=os.getuid():
        raise ValueError('owned socket')
    server=int(invoke('tmux-server',['tmux','-S',socket_path,'display-message','-p','-t',anchor,'#{pid}']).strip())
    invoke('tmux-project',['tmux','-S',socket_path,'set-option','-t',session_name,'-q','@projmux_project_path',str(root/'work')])
    invoke('reconcile',[binary,'reconcile','resources','--socket-path',socket_path])
    window_json=invoke('windows-get',[binary,'get','windows','--project','uid:'+project,'-o','json'])
    stage('window-validation')
    windows=json.loads(window_json)['items']
    if len(windows)!=1 or windows[0]['metadata']['ownerRef']!=dict(kind='Project',uid=project): raise ValueError('owned Window')
    window=windows[0]['metadata']['uid']
    native={}
    for role,provider in [('sender','codex'),('receiver','claude')]:
        argv=[binary,'create','agent','--provider',provider]
        if provider=='claude': argv.append('--dialogue-reply-only')
        argv+=['--project','uid:'+project,'--window','uid:'+window,'-o','pane-id']
        if provider=='codex' and source_prompt is not None:
            argv+=['--',source_prompt]
        native[role]=invoke(role+'-create',argv,pane=anchor,server=server,socket_path=socket_path,timeout=120).strip()
    stage('runtime-chain')
    registry=root/'xdg-state/projmux/metadata/registry.json'
    data=json.loads(registry.read_text())
    actors={}
    for role,runtime in native.items():
        panes=[p for p in data['panes'] if (p.get('status',{}).get('activation') or {}).get('runtimeID')==runtime]
        if len(panes)!=1: raise ValueError('unique runtime')
        pane=panes[0]; activation=pane['status']['activation']
        actors[role]=dict(agentUID=pane['metadata']['ownerRef']['uid'],paneUID=pane['metadata']['uid'],generation=activation['generation'],paneID=runtime)
    return dict(version=2,binary=binary,registryPath=str(registry),tmuxSocketPath=socket_path,tmuxSocketName=socket_name,
                tmuxServerPID=server,projectUID=project,windowUID=window,**actors)


def invoke_setup(root, env, audit, stage, argv, *, pane=None, server=None, socket_path=None, timeout=15):
    audit.stage(stage)
    call_env=dict(env)
    if pane is not None:
        call_env.update(TMUX=f'{socket_path},{server},0',TMUX_PANE=pane)
    result=subprocess.run(argv,env=call_env,cwd=root/'work',stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout,check=False)
    audit.stage(stage,exit_code=result.returncode,stdout_bytes=len(result.stdout),stderr_bytes=len(result.stderr))
    if result.returncode or len(result.stdout)>4*1024*1024:
        raise ValueError('owned setup command failed')
    return result.stdout.decode()


def main():
    if os.environ.get('PMX_DIALOGUE_LIVE_CANARY')!='1': raise ValueError('explicit live opt-in required')
    root=pathlib.Path(os.environ['PMX_DIALOGUE_CANARY_ROOT'])
    script=pathlib.Path(__file__).with_name('agent-dialogue-live-canary.sh')
    binary=str(pathlib.Path(os.environ['PMX_DIALOGUE_PROJMUX_BIN']).resolve())
    socket_name='pmx-p4-'+uuid.uuid4().hex[:20]
    env=isolated_environment(root)
    prepared=False
    transferred=False
    identity=None
    original_error=None
    native_child=None
    audit=Audit.create(root,pathlib.Path(os.environ['PMX_DIALOGUE_CANARY_RECEIPT']))
    try:
        audit.stage('prepare')
        result=subprocess.run(['bash',str(script),'prepare'],check=True,timeout=15)
        audit.stage('prepare',exit_code=result.returncode)
        prepared=True
        before=root.lstat(); identity=(before.st_dev,before.st_ino)
        audit.stage('pins')
        plan_path=root/'cleanup-plan.json'
        plan=json.loads(plan_path.read_text())
        plan['audit']=audit.descriptor()
        plan_path.write_text(json.dumps(plan,sort_keys=True)+'\n')
        if re.fullmatch(r'[a-f0-9]{40}',plan['candidateHead']) is None or re.fullmatch(r'[a-f0-9]{64}',plan['candidateSHA256']) is None: raise ValueError('candidate digest')
        audit.append(dict(version=1,event='binding',candidateHead=plan['candidateHead'],candidateSHA256=plan['candidateSHA256'],rootIdentity=list(identity)))
        if plan['candidateBinary']!=binary: raise ValueError('candidate changed')
        for name,digest in plan['runnerFiles'].items():
            if hashlib.sha256((script.parent/name).read_bytes()).hexdigest()!=digest: raise ValueError('runner changed')
        def invoke(stage, argv, **options):
            return invoke_setup(root,env,audit,stage,argv,**options)
        for provider,expected in [('claude',plan['claudeVersion']),('codex',plan['codexVersion'])]:
            observed=re.findall(r'\b\d+\.\d+\.\d+\b',invoke(provider+'-version',[str(root/'bin'/provider),'--version']))
            if observed!=[expected]: raise ValueError('provider version changed')
        native=runpy.run_path(str(script.with_name('agent-dialogue-native-source.py')))
        if plan.get('sourceMode')!='genuine-native-task' or plan['sourcePrompt']!=native['source_prompt'](root):
            raise ValueError('genuine source plan')
        audit.stage('native-launch')
        native_child,launch=native['launch'](root,plan,env)
        audit.stage('native-ready')
        endpoint=native['ready'](root,launch,native_child)
        audit.stage('policy-before-source')
        policy=native['read_native_policy'](root,plan,endpoint)
        native['exclusive'](root/'evidence/native-policy-before-source.json',policy)
        audit.append(dict(version=1,event='policy',phase='before-source',facts=policy))
        spec=setup(root,binary,socket_name,invoke,audit.stage,source_prompt=plan['sourcePrompt'])
        spec['messageRef']=plan['messageRef']
        audit.stage('input-write')
        input_path=root/'canary-input.json'
        input_path.write_text(json.dumps(spec,sort_keys=True)+'\n'); input_path.chmod(0o600)
        evidence=runpy.run_path(str(script.with_name('agent-dialogue-canary-evidence.py')))
        # Bounded read-only readiness; no provider push or model tool execution.
        audit.stage('readiness')
        deadline=time.monotonic()+120
        while True:
            try:
                evidence['snapshot'](root,spec,initial=True,env=env)
                break
            except evidence['Refused'] as failure:
                if str(failure) not in ('capability route','Codex composite authority','registration not ready'):
                    raise ValueError('readiness validation failed') from None
                if time.monotonic()>=deadline: raise ValueError('public profile/source readiness deadline') from None
                time.sleep(.1)
        run_env={key:os.environ[key] for key in ('PATH','HOME','PMX_DIALOGUE_LIVE_CANARY','PMX_DIALOGUE_CANARY_ROOT','PMX_DIALOGUE_CANARY_RECEIPT') if key in os.environ}
        run_env['PMX_DIALOGUE_CANARY_INPUT']=str(input_path)
        # run owns every cleanup outcome from this point. Never retry a retained
        # root after its barrier/removal failure.
        transferred=True
        audit.stage('run')
        result=subprocess.run(['bash',str(script),'run'],env=run_env,check=False)
        audit.stage('run',exit_code=result.returncode)
        if result.returncode: raise ValueError('owned canary failed or retained evidence')
        audit.stage('complete',exit_code=0)
    except Exception as failure:
        original_error=failure
        if isinstance(failure,subprocess.CalledProcessError): audit.stage('prepare',exit_code=failure.returncode)
    finally:
        if prepared and root.exists() and not (transferred and (root/'evidence/cleanup-attempted').exists()):
            try:
                if identity is None: raise ValueError('root identity missing')
                audit.stage('cleanup')
                finish_setup_failure(root,binary,socket_name,identity,audit)
            except Exception:
                audit.cleanup(root,'root-retained')
                raise ValueError('setup cleanup could not prove writer exit; root retained') from None
        if native_child is not None:
            # Once-cleanup has already proved exit (or retained failure). Reap
            # only this launch handle; never wait unbounded or retry teardown.
            try: native_child.wait(timeout=.1)
            except subprocess.TimeoutExpired:
                raise ValueError('native launch handle still running; root retained') from None
    audit.append(dict(version=1,event='terminal',exitCode=1 if original_error is not None else 0,rootAbsent=not root.exists(),receiptExists=pathlib.Path(os.environ['PMX_DIALOGUE_CANARY_RECEIPT']).exists()))
    if original_error is not None:
        audit.append(dict(version=1,event='failure',reason='timeout' if isinstance(original_error,subprocess.TimeoutExpired) else 'validation-or-command',rootAbsent=not root.exists()))
        raise ValueError('owned setup or run failed') from None


if __name__=='__main__':
    try:
        if len(sys.argv)>1 and sys.argv[1]=='audit':
            root=pathlib.Path(sys.argv[2]); audit=audit_for_root(root)
            if sys.argv[3]=='stage': audit.stage(sys.argv[4],exit_code=int(sys.argv[5]) if len(sys.argv)>5 else None)
            elif sys.argv[3]=='cleanup': audit.cleanup(root,sys.argv[4])
            else: raise ValueError('audit operation')
        else: main()
    except Exception:
        print('owned dialogue setup/run failed; inspect the bounded cleanup outcome',file=sys.stderr)
        raise SystemExit(1)
