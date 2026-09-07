"""Offline fake launches/connections and owned fixture PIDs; no vendor/auth."""
import copy
import json
import os
import pathlib
import runpy
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import tomllib
import unittest
from unittest import mock


REPO=pathlib.Path(__file__).resolve().parents[1]


class NativeSourceTests(unittest.TestCase):
    def setUp(self):
        self.ns=runpy.run_path(str(REPO/'scripts/agent-dialogue-native-source.py'))
        self.globals=self.ns['launch'].__globals__
        self.temp=tempfile.TemporaryDirectory(prefix='p4-n-')
        self.addCleanup(self.temp.cleanup)
        self.root=pathlib.Path(self.temp.name)
        for name in ('evidence','bin','work','codex-home'):
            (self.root/name).mkdir(mode=0o700)
        self.identity=self.ns['birth'](os.getpid())[0]
        self.executable=self.root/'inert-executable'
        self.executable.write_text('#!/bin/sh\nexit 97\n');self.executable.chmod(0o755)

    def write(self,name,value):
        self.ns['exclusive'](self.root/name,value)

    def test_private_config_and_genuine_prompt_are_explicit_without_extra_commands(self):
        config=tomllib.loads(self.ns['private_config'](self.root))
        self.assertEqual(config['cli_auth_credentials_store'],'file')
        self.assertEqual(config['approval_policy'],'never')
        self.assertEqual(config['sandbox_mode'],'workspace-write')
        self.assertFalse(config['sandbox_workspace_write']['network_access'])
        self.assertEqual(config['sandbox_workspace_write']['writable_roots'],[str(self.root)])
        self.assertEqual(config['web_search'],'disabled')
        self.assertNotIn('mcp_servers',config)
        command=self.ns['source_command'](self.root)
        self.assertEqual(shlex.split(command),['python3',str(self.root/'bin/agent-dialogue-source-action.py'),str(self.root)])
        prompt=self.ns['source_prompt'](self.root)
        self.assertEqual(prompt.count(command),1)
        self.assertIn('your own inbox claim',prompt)
        self.assertIn('Do not execute any other',prompt)

    def test_public_socket_bound_checked_before_launch(self):
        with self.assertRaisesRegex(ValueError,'socket-path-bound'):
            self.ns['endpoint_path'](pathlib.Path('/tmp')/('x'*100))

    def test_launch_uses_exact_direct_child_and_private_environment(self):
        executable=self.executable
        plan=dict(codexBinary=str(executable),codexImage=self.ns['image'](executable))
        child=mock.Mock(pid=os.getpid())
        popen=mock.Mock(return_value=child)
        with mock.patch.dict(self.globals,birth=lambda _: (self.identity,os.getpid())), mock.patch.object(pathlib.Path,'resolve',return_value=executable):
            handle,record=self.ns['launch'](self.root,plan,{'HOME':str(self.root/'home')},popen)
        self.assertIs(handle,child)
        self.assertEqual(popen.call_args.args[0],[str(executable),'app-server','--listen','unix://'+str(self.ns['endpoint_path'](self.root))])
        self.assertEqual(popen.call_args.kwargs['env'],{'HOME':str(self.root/'home')})
        self.assertEqual(popen.call_args.kwargs['stdout'],subprocess.DEVNULL)
        self.assertEqual(record['process'],self.identity)
        self.assertNotIn('argv',self.ns['load'](self.root/'evidence/native-launch.json'))
        child.terminate.assert_not_called()

    def test_changed_executable_refuses_before_popen(self):
        plan=dict(codexBinary=str(self.executable),codexImage={})
        popen=mock.Mock()
        with self.assertRaisesRegex(ValueError,'image-changed'):
            self.ns['launch'](self.root,plan,{},popen)
        popen.assert_not_called()

    def test_launch_capture_error_terminates_only_returned_owned_handle(self):
        executable=self.executable
        plan=dict(codexBinary=str(executable),codexImage=self.ns['image'](executable))
        child=mock.Mock(pid=os.getpid())
        with mock.patch.dict(self.globals,birth=lambda _: (self.identity,1)):
            with self.assertRaises(ValueError): self.ns['launch'](self.root,plan,{},mock.Mock(return_value=child))
        child.terminate.assert_called_once_with()
        child.wait.assert_called_once_with(timeout=5)

    def test_actual_owned_child_ancestry_and_foreign_birth(self):
        child=subprocess.Popen([sys.executable,'-c','import sys; sys.stdin.buffer.read()'],stdin=subprocess.PIPE,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        try:
            identity=self.ns['birth'](child.pid)[0]
            self.ns['ancestry'](identity,self.identity)
            changed=dict(identity,start=identity['start']+'0')
            with self.assertRaises(ValueError): self.ns['ancestry'](changed,self.identity)
            with self.assertRaises(ValueError): self.ns['exited'](changed)
        finally:
            child.stdin.close(); child.wait(timeout=5)
        self.assertTrue(self.ns['exited'](identity))

    def test_unreaped_zombie_is_proven_exited_without_signal(self):
        if not hasattr(os,'waitid'): self.skipTest('Linux process proof')
        child=subprocess.Popen([sys.executable,'-c','import sys; sys.stdin.buffer.read()'],stdin=subprocess.PIPE)
        identity=self.ns['birth'](child.pid)[0]
        child.stdin.close()
        os.waitid(os.P_PID,child.pid,os.WEXITED|os.WNOWAIT)
        try: self.assertTrue(self.ns['exited'](identity))
        finally: child.wait(timeout=5)

    def test_signal_requires_exact_captured_birth_and_never_falls_back_to_pid(self):
        role=dict(role='source-action',process=self.identity)
        barrier=mock.Mock(writers={os.getpid():(dict(self.identity,start='foreign'),123)})
        with mock.patch.object(signal,'pidfd_send_signal') as send:
            with self.assertRaises(ValueError):self.ns['terminate_roles']([role],barrier)
            send.assert_not_called()
            barrier.writers={os.getpid():(self.identity,123)}
            self.ns['terminate_roles']([role],barrier)
            send.assert_called_once_with(123,signal.SIGTERM)

    def test_initialization_is_bounded_and_unknown_content_is_not_exposed(self):
        class Connection:
            def __init__(self,value):self.raw=bytearray(value);self.sent=[]
            def sendall(self,value):self.sent.append(json.loads(value))
            def settimeout(self,_):pass
            def recv(self,_):
                if not self.raw:return b''
                return bytes([self.raw.pop(0)])
        observation=runpy.run_path(str(REPO/'scripts/agent-dialogue-codex-observation.py'))
        connection=Connection(b'{"id":0,"result":{"userAgent":"fixture"}}\n')
        self.ns['initialize'](connection,observation)
        self.assertEqual([row['method'] for row in connection.sent],['initialize','initialized'])
        for raw in (b'',b'x'*16385,b'{"id":0,"result":{"private":"DO_NOT_PERSIST"}}\n'):
            with self.assertRaises(ValueError) as failure:self.ns['initialize'](Connection(raw),observation)
            self.assertNotIn('DO_NOT_PERSIST',str(failure.exception))

    def test_genuine_payload_uses_existing_public_create_after_own_context(self):
        setup=runpy.run_path(str(REPO/'scripts/agent-dialogue-canary-setup.py'))
        path=self.root/'socket'
        calls=[]
        with socket.socket(socket.AF_UNIX) as sock:
            sock.bind(str(path))
            class Done(Exception):pass
            def invoke(stage,argv,**options):
                calls.append((stage,argv,options))
                if stage=='sender-create':raise Done()
                return {'project-create':'project','project-get':json.dumps(dict(items=[dict(kind='Project',metadata=dict(uid='project'),spec=dict(root=str(self.root/'work')),status=dict(session=dict(name='owned')))])),
                        'tmux-create':'%1','tmux-socket':str(path),'tmux-server':'123',
                        'windows-get':json.dumps(dict(items=[dict(metadata=dict(uid='window',ownerRef=dict(kind='Project',uid='project')))]))}.get(stage,'')
            prompt=self.ns['source_prompt'](self.root)
            with self.assertRaises(Done):setup['setup'](self.root,'/candidate','owned',invoke,source_prompt=prompt)
        stage,argv,context=calls[-1]
        self.assertEqual(argv,['/candidate','create','agent','--provider','codex','--project','uid:project','--window','uid:window','-o','pane-id','--',prompt])
        self.assertEqual(context,dict(pane='%1',server=123,socket_path=str(path),timeout=120))

    def test_release_checks_complete_before_action_and_return_before_cleanup(self):
        spec=dict(sender=dict(agentUID='source',paneUID='source-pane',generation='generation',paneID='%1'),receiver=dict(agentUID='target'))
        initial=dict(routes={'fixture':'routes'})
        self.write('cleanup-plan.json',dict(sourceAllowedOrigins=['agent']))
        self.write('evidence/native-endpoint.json',dict(process=self.identity))
        self.write('evidence/source-action-ready.json',dict(process=self.identity))
        facts=dict(threadId='thread',turnId='turn',itemId='item',source='agent',status='inProgress',commandMatched=True,cwdMatched=True,sourceDefaultApplied=False)
        result=dict(version=1,sourceAgentUID='source',targetAgentUID='target')
        for phase in ('qualification','idle'):
            result[phase]=dict(originalRef=phase,replyRef=phase+'-reply',conversationRef=phase+'-conversation',selfClaimed=True,guardedCommitMatched=True)
            self.write('evidence/'+phase+'-proof.json',{k:v for k,v in result[phase].items() if k!='selfClaimed'})
        sequence=[]
        reader=mock.Mock(frozen=None)
        def read(*,freeze=False):
            if freeze:
                self.assertFalse((self.root/'source-release.json').exists());sequence.append('read-started');return facts
            sequence.append('read-completed');reader.set_expected_result.assert_called_once_with(result)
            return facts|dict(toolCompleted=True,closedResultMatched=True,turnCompleted=True)
        connection=mock.MagicMock()
        wire=mock.Mock();wire.read.side_effect=read
        real_observation=runpy.run_path(str(REPO/'scripts/agent-dialogue-codex-observation.py'))
        observation=dict(Schemas=mock.Mock(),ObservationReader=mock.Mock(return_value=reader),OwnedReadConnection=mock.Mock(return_value=wire),validate_result=real_observation['validate_result'])
        def stage(name):
            sequence.append(name)
            if name=='source-result':
                self.assertTrue((self.root/'source-release.json').exists())
                self.write('evidence/source-action-result.json',result)
        original_read=pathlib.Path.read_bytes
        def read_bytes(path):
            if path==pathlib.Path('/proc',str(os.getpid()),'cmdline'):
                return b'python3\0'+os.fsencode(self.root/'bin/agent-dialogue-source-action.py')+b'\0'+os.fsencode(self.root)+b'\0'
            return original_read(path)
        modules={'agent-dialogue-source-action.py':{'own_environment':lambda *_:{}},
                 'agent-dialogue-codex-observation.py':observation,'agent-dialogue-canary-evidence.py':{'snapshot':lambda *_a,**_k:copy.deepcopy(initial)}}
        with mock.patch.dict(self.globals,freeze_thread=lambda *_:('thread','turn'),ancestry=lambda *_:None,
                             connect=lambda *_:(connection,[]),initialize=lambda *_:None), \
             mock.patch.object(runpy,'run_path',side_effect=lambda path:modules[pathlib.Path(path).name]), \
             mock.patch.object(pathlib.Path,'read_bytes',read_bytes):
            self.ns['observe_action'](self.root,spec,initial,stage)
        self.assertLess(sequence.index('read-started'),sequence.index('source-release'))
        self.assertLess(sequence.index('source-result'),sequence.index('read-completed'))
        self.assertTrue(self.root.exists())
        self.assertTrue((self.root/'evidence/source-observation.json').exists())


if __name__=='__main__':unittest.main()
