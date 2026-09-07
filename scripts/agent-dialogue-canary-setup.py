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


def isolated_environment(root):
    # No inherited provider config, messaging credentials or routing policy.
    return dict(PATH=str(root/'bin')+os.pathsep+os.environ.get('PATH','/usr/bin:/bin'),
                HOME=str(root/'home'),CODEX_HOME=str(root/'codex-home'),
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
    namespace['close_owned_writers']=lambda root,teardown,seeds=(): close(root,teardown,seeds,timeout=timeout,on_wait=on_wait)
    arguments=sys.argv
    try:
        sys.argv=['cleanup',str(root),binary,str(root/'xdg-state/projmux/metadata/registry.json'),socket_path,socket_identity,project,socket_name,'']
        namespace['main']()
    finally:
        sys.argv=arguments


def finish_setup_failure(root, binary, socket_name, identity, **test_options):
    before=root.lstat()
    if (before.st_dev,before.st_ino)!=identity or root.is_symlink(): raise ValueError('root changed')
    try:
        cleanup_partial(root,binary,socket_name,**test_options)
    finally:
        after=root.lstat()
        if (after.st_dev,after.st_ino)!=identity or root.is_symlink(): raise ValueError('root changed')
        directory=root/'home/.claude'
        if directory.is_symlink() or (root/'home').is_symlink(): raise ValueError('credential parent changed')
        (directory/'.credentials.json').unlink(missing_ok=True)
    shutil.rmtree(root)


def setup(root, binary, socket_name, invoke):
    """Only public tmux/project/create routes; no fabricated Codex binding."""
    anchor=invoke(['tmux','-L',socket_name,'new-session','-d','-P','-F','#{pane_id}','-s','dialogue-canary','-c',str(root/'work'),'sleep','3600']).strip()
    socket_path=invoke(['tmux','-L',socket_name,'display-message','-p','-t',anchor,'#{socket_path}']).strip()
    path=pathlib.Path(socket_path)
    info=path.lstat()
    if root not in path.parents or path.is_symlink() or not stat.S_ISSOCK(info.st_mode) or info.st_uid!=os.getuid():
        raise ValueError('owned socket')
    server=int(invoke(['tmux','-S',socket_path,'display-message','-p','-t',anchor,'#{pid}']).strip())
    invoke(['tmux','-S',socket_path,'set-option','-t','dialogue-canary','-q','@projmux_project_path',str(root/'work')])
    project=invoke([binary,'create','project','--root',str(root/'work'),'--name','dialogue-canary','-o','uid']).strip()
    invoke([binary,'reconcile','resources','--socket-path',socket_path])
    windows=json.loads(invoke([binary,'get','windows','--project','uid:'+project,'-o','json']))['items']
    if len(windows)!=1 or windows[0]['metadata']['ownerRef']!=dict(kind='Project',uid=project): raise ValueError('owned Window')
    window=windows[0]['metadata']['uid']
    native={}
    for role,provider in [('sender','codex'),('receiver','claude')]:
        argv=[binary,'create','agent','--provider',provider]
        if provider=='claude': argv.append('--dialogue-reply-only')
        argv+=['--project','uid:'+project,'--window','uid:'+window,'-o','pane-id']
        native[role]=invoke(argv,pane=anchor,server=server,socket_path=socket_path,timeout=120).strip()
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
    try:
        subprocess.run(['bash',str(script),'prepare'],check=True,timeout=15)
        prepared=True
        before=root.lstat(); identity=(before.st_dev,before.st_ino)
        plan=json.loads((root/'cleanup-plan.json').read_text())
        if plan['candidateBinary']!=binary: raise ValueError('candidate changed')
        for name,digest in plan['runnerFiles'].items():
            if hashlib.sha256(script.with_name(name).read_bytes()).hexdigest()!=digest: raise ValueError('runner changed')
        def invoke(argv, *, pane=None, server=None, socket_path=None, timeout=15):
            call_env=dict(env)
            if pane is not None:
                call_env.update(TMUX=f'{socket_path},{server},0',TMUX_PANE=pane)
            result=subprocess.run(argv,env=call_env,cwd=root/'work',stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout,check=False)
            if result.returncode or len(result.stdout)>4*1024*1024:
                raise ValueError('owned setup command failed')
            return result.stdout.decode()
        for provider,expected in [('claude',plan['claudeVersion']),('codex',plan['codexVersion'])]:
            observed=re.findall(r'\b\d+\.\d+\.\d+\b',invoke([str(root/'bin'/provider),'--version']))
            if observed!=[expected]: raise ValueError('provider version changed')
        spec=setup(root,binary,socket_name,invoke)
        spec['messageRef']=plan['messageRef']
        input_path=root/'canary-input.json'
        input_path.write_text(json.dumps(spec,sort_keys=True)+'\n'); input_path.chmod(0o600)
        evidence=runpy.run_path(str(script.with_name('agent-dialogue-canary-evidence.py')))
        # Bounded read-only readiness; no provider push or model tool execution.
        deadline=time.monotonic()+120
        while True:
            try:
                evidence['snapshot'](root,spec,initial=True,env=env)
                break
            except Exception:
                if time.monotonic()>=deadline: raise ValueError('public profile/source readiness deadline') from None
                time.sleep(.1)
        run_env={key:os.environ[key] for key in ('PATH','HOME','PMX_DIALOGUE_LIVE_CANARY','PMX_DIALOGUE_CANARY_ROOT','PMX_DIALOGUE_CANARY_RECEIPT') if key in os.environ}
        run_env['PMX_DIALOGUE_CANARY_INPUT']=str(input_path)
        # run owns every cleanup outcome from this point. Never retry a retained
        # root after its barrier/removal failure.
        transferred=True
        result=subprocess.run(['bash',str(script),'run'],env=run_env,check=False)
        if result.returncode: raise ValueError('owned canary failed or retained evidence')
    except Exception as failure:
        original_error=failure
    finally:
        if prepared and root.exists() and not (transferred and (root/'evidence/cleanup-attempted').exists()):
            try:
                if identity is None: raise ValueError('root identity missing')
                finish_setup_failure(root,binary,socket_name,identity)
            except Exception:
                raise ValueError('setup cleanup could not prove writer exit; root retained') from None
    if original_error is not None: raise ValueError('owned setup or run failed') from None


if __name__=='__main__':
    try: main()
    except Exception:
        print('owned dialogue setup/run failed; inspect the bounded cleanup outcome',file=sys.stderr)
        raise SystemExit(1)
