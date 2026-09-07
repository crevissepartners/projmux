"""Offline canary evidence/cleanup checks; public-stream parsing is tested in Go."""
import copy
import hashlib
import concurrent.futures
import json
import os
import selectors
import runpy
import shlex
import shutil
import sys
import threading
import pathlib
import subprocess
import tempfile
import socket
import unittest
from unittest import mock


@unittest.skipUnless(hasattr(os, "pidfd_open"), "live canary cleanup requires Linux pidfd")
class DialogueCleanupTest(unittest.TestCase):
    def setUp(self):
        self.source = (pathlib.Path(__file__).resolve().parents[1] /
                       "scripts/agent-dialogue-live-canary.sh").read_text()
        code = self.source.split("<<'WRITERS_PY' || return 1\n", 1)[1].split("\nWRITERS_PY\n", 1)[0]
        self.code = {"__name__": "canary_cleanup_test"}
        exec(compile(code, "canary-cleanup", "exec"), self.code)
        self.temp = tempfile.TemporaryDirectory(prefix="pmx-writer-cleanup-")
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name, "owned")
        self.root.mkdir()

    def writer(self, root_argument=True):
        program = ("import pathlib,sys; print('ready',flush=True); sys.stdin.readline(); "
                   "p=pathlib.Path(sys.argv[1]); p.mkdir(parents=True,exist_ok=True); "
                   "(p/'termination-receipts.jsonl').write_text('late owned receipt\\n')")
        target = self.root / "xdg-state/projmux" if root_argument else pathlib.Path(self.temp.name, "foreign")
        child = subprocess.Popen([sys.executable, "-c", program, str(target)],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.assertEqual(child.stdout.readline().strip(), "ready")
        self.addCleanup(self.release, child)
        return child

    @staticmethod
    def release(child):
        if child.poll() is None:
            child.communicate("release\n", timeout=5)
        else:
            child.communicate(timeout=5)

    def test_run_failure_cleanup_waits_and_preserves_external_audit_after_removal(self):
        child = self.writer()
        setup_path=pathlib.Path(__file__).resolve().parents[1]/'scripts/agent-dialogue-canary-setup.py'
        setup=runpy.run_path(str(setup_path))
        receipt=pathlib.Path(self.temp.name)/'receipt'
        audit=setup['Audit'].create(self.root,receipt)
        (self.root/'evidence').mkdir()
        (self.root / "cleanup-plan.json").write_text(json.dumps({"version": 3, "ownedRoot": str(self.root),"receiptPath":str(receipt),"audit":audit.descriptor()}))
        (self.root / ".projmux-dialogue-canary-owned").write_text("projmux-dialogue-canary-owned-v3\n")
        identity = self.root.stat()
        finish = "finish_cleanup() {" + self.source.split("finish_cleanup() {", 1)[1].split("\ntrap finish_cleanup EXIT", 1)[0]
        library = pathlib.Path(self.temp.name, "cleanup.py")
        library.write_text(self.source.split("<<'WRITERS_PY' || return 1\n", 1)[1].split("\nWRITERS_PY\n", 1)[0])
        client = pathlib.Path(self.temp.name, "finish.py")
        client.write_text("import os,pathlib,runpy,sys,json; n=runpy.run_path(sys.argv[1]); "
                          "proof=n['close_owned_writers'](pathlib.Path(sys.argv[2]),lambda _:None, "
                          "on_wait=lambda:os.write(int(sys.argv[3]),b'w')); "
                          "(pathlib.Path(sys.argv[2])/'evidence/cleanup-writers.json').write_text(json.dumps(proof))")
        read_fd, write_fd = os.pipe()
        command = " ".join(map(shlex.quote, [sys.executable, str(client), str(library), str(self.root), str(write_fd)]))
        shell = ("set -euo pipefail\nroot=" + shlex.quote(str(self.root)) +
                 f"\nroot_identity={identity.st_dev}:{identity.st_ino}\n" +
                 "audit_event() { python3 " + shlex.quote(str(setup_path)) + ' audit "$root" "$@"; }\n' +
                 "cleanup_owned() { " + command + "; }\n" + finish + "\ntrap finish_cleanup EXIT\ncanary_stage=qualification\nfalse\n")
        cleanup = subprocess.Popen(["bash", "-c", shell], pass_fds=(write_fd,),
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        os.close(write_fd)
        try:
            with selectors.DefaultSelector() as poller:
                poller.register(read_fd, selectors.EVENT_READ)
                self.assertTrue(poller.select(5), "cleanup did not reach the process-exit barrier")
            self.assertEqual(os.read(read_fd, 1), b"w")
            self.assertTrue(self.root.exists())
            self.assertIsNone(cleanup.poll())
            self.assertIsNone(child.poll())
        finally:
            os.close(read_fd)
            self.release(child)
        stdout, stderr = cleanup.communicate(timeout=5)
        self.assertEqual(cleanup.returncode, 1, (stdout, stderr))  # Preserve the original canary failure.
        self.assertFalse(self.root.exists())
        self.assertEqual(child.returncode, 0)
        records=[json.loads(line) for line in audit.path.read_text().splitlines()]
        self.assertEqual(records[0],dict(version=1,event='stage',stage='qualification',exitCode=1))
        proof=next(row for row in records if row.get('outcome')=='writers-exited')
        self.assertIn(child.pid,[row['pid'] for row in proof['proof']['writers']])
        self.assertFalse(proof['rootAbsent'])
        self.assertEqual(records[-1]['stage'],'root-removal')

    def test_partial_setup_failure_waits_for_writer_and_removes_credential(self):
        root=self.root
        for relative in ('tmux','evidence','home/.claude'):
            (root/relative).mkdir(parents=True,exist_ok=True)
        (root/'home/.claude/.credentials.json').write_text('fixture only')
        child=self.writer()
        before=root.stat(); waiting=threading.Event()
        setup=runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1]/'scripts/agent-dialogue-canary-setup.py'))
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            future=executor.submit(setup['finish_setup_failure'],root,sys.executable,'pmx-partial-fixture-'+str(os.getpid()),(before.st_dev,before.st_ino),on_wait=waiting.set,timeout=5)
            try:
                self.assertTrue(waiting.wait(5))
                self.assertTrue(root.exists())
                self.assertFalse(future.done())
                self.assertIsNone(child.poll())
            finally: self.release(child)
            future.result(timeout=5)
        self.assertFalse(root.exists())
        self.assertEqual(child.returncode,0)

    def test_partial_setup_early_cleanup_error_retains_root_but_removes_credential(self):
        root=self.root
        (root/'home/.claude').mkdir(parents=True)
        credential=root/'home/.claude/.credentials.json'; credential.write_text('fixture only')
        before=root.stat()
        setup=runpy.run_path(str(pathlib.Path(__file__).resolve().parents[1]/'scripts/agent-dialogue-canary-setup.py'))
        finish=setup['finish_setup_failure']
        with mock.patch.dict(finish.__globals__,cleanup_partial=mock.Mock(side_effect=ValueError('early inventory failure'))):
            with self.assertRaises(ValueError): finish(root,sys.executable,'pmx-unstarted',(before.st_dev,before.st_ino))
        self.assertTrue(root.exists())
        self.assertFalse(credential.exists())

    def test_captured_writer_stays_owned_after_parent_exit(self):
        read_fd, write_fd = os.pipe()
        program = ("import os,pathlib,sys; pid=os.fork(); "
                   "p=pathlib.Path(sys.argv[1]); "
                   "print('parent' if pid else 'child',flush=True); "
                   "sys.stdin.readline() if pid else os.read(int(sys.argv[2]),1); "
                   "sys.exit(0) if pid else None; "
                   "p.mkdir(parents=True,exist_ok=True); (p/'receipt').write_text('late')")
        parent = subprocess.Popen([sys.executable, "-c", program, str(self.root / "state"), str(read_fd)],
                                  pass_fds=(read_fd,), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        os.close(read_fd)
        self.assertEqual({parent.stdout.readline().strip(), parent.stdout.readline().strip()}, {"parent", "child"})
        waiting = threading.Event()
        def teardown(_):
            parent.stdin.write("exit\n"); parent.stdin.flush()
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(self.code["close_owned_writers"], self.root, teardown, (), 5, waiting.set)
            try:
                self.assertTrue(waiting.wait(5))
                parent.wait(timeout=5)
                self.assertFalse(future.done())
                self.assertTrue(self.root.exists())
            finally:
                os.write(write_fd, b"x"); os.close(write_fd)
            proof = future.result(timeout=5)
        parent.communicate(timeout=5)
        self.assertGreaterEqual(len(proof["writers"]), 2)
        self.assertTrue((self.root / "state/receipt").exists())

    def test_stubborn_writer_times_out_without_removing_root_or_signalling_it(self):
        child = self.writer()
        with self.assertRaisesRegex(RuntimeError, "root retained"):
            self.code["close_owned_writers"](self.root, lambda _: None, timeout=0.05)
        self.assertTrue(self.root.exists())
        self.assertIsNone(child.poll())

    def test_foreign_or_replaced_birth_is_neither_waited_on_nor_signalled(self):
        child = self.writer(root_argument=False)
        observed = self.code["OwnedWriterBarrier"].observe(child.pid)[0]
        for replacement in (dict(observed, start=observed["start"] + "-replaced"),
                            dict(observed, ownerUID=observed["ownerUID"] + 1)):
            proof = self.code["close_owned_writers"](self.root, lambda _: None, (replacement,), timeout=0.05)
            self.assertEqual(proof["writers"], [])
            self.assertIsNone(child.poll())

    def test_real_exit_trap_retains_root_when_writer_proof_fails(self):
        root = self.root
        credentials = root / "home/.claude/.credentials.json"
        credentials.parent.mkdir(parents=True)
        credentials.write_text("fixture only")
        finish = "finish_cleanup() {" + self.source.split("finish_cleanup() {", 1)[1].split("\ntrap finish_cleanup EXIT", 1)[0]
        shell = "set -euo pipefail\nroot=" + shlex.quote(str(root)) + "\naudit_event() { return 1; }\ncleanup_owned() { return 1; }\n" + finish + "\ntrap finish_cleanup EXIT\nfalse\n"
        result = subprocess.run(["bash", "-c", shell], text=True, capture_output=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(root.exists())
        self.assertFalse(credentials.exists())
        self.assertIn("root retained", result.stderr)


@unittest.skipUnless(hasattr(os, "pidfd_open"), "offline dialogue cleanup requires Linux pidfd")
class OfflineDialogueCleanupTest(unittest.TestCase):
    def setUp(self):
        self.repo = pathlib.Path(__file__).resolve().parents[1]
        self.library = runpy.run_path(str(self.repo / "test/e2e/dialogue-cleanup.py"))
        self.temp = tempfile.TemporaryDirectory(prefix="pmx-offline-writer-")
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name, "owned")
        self.root.mkdir()
        self.source = (self.repo / "test/e2e/heterogeneous-agent-dialogue.inc.sh").read_text()

    def writer(self, root=None):
        root = root or self.root
        root.mkdir(exist_ok=True)
        program = ("import pathlib,sys;p=pathlib.Path.cwd();print('ready',flush=True);"
                   "sys.stdin.readline();p.mkdir(parents=True,exist_ok=True);"
                   "(p/'termination-receipts.jsonl').write_text('late receipt')")
        child = subprocess.Popen([sys.executable, "-c", program], cwd=root,
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.assertEqual(child.stdout.readline().strip(), "ready")
        self.assertNotIn(str(root).encode(), pathlib.Path("/proc", str(child.pid), "cmdline").read_bytes())
        self.addCleanup(DialogueCleanupTest.release, child)
        return child

    def test_real_offline_exit_waits_for_cwd_writer_and_preserves_original_failure(self):
        child = self.writer()
        fifo = pathlib.Path(self.temp.name, "delete-intent")
        os.mkfifo(fifo)
        read_fd = os.open(fifo, os.O_RDWR | os.O_NONBLOCK)
        self.addCleanup(os.close, read_fd)
        binary = pathlib.Path(self.temp.name, "delete-fixture")
        binary.write_text("#!" + sys.executable + "\nimport pathlib\npathlib.Path(" + repr(str(fifo)) + ").write_text('d')\n")
        binary.chmod(0o700)
        functions = "dialogue_cleanup() {" + self.source.split("dialogue_cleanup() {", 1)[1].split("\ntrap dialogue_finish_cleanup EXIT", 1)[0]
        values = dict(dialogue_root=str(self.root), bin=str(binary), dialogue_real_tmux="/bin/false",
                      dialogue_socket_path="", dialogue_socket_identity="", dialogue_socket="owned",
                      dialogue_project_uid="fixture", dialogue_control_pid="", dialogue_binding_pid="", dialogue_wait_pid="")
        shell = "set -euo pipefail\ndialogue_env=(env -u TMUX -u TMUX_PANE)\ndialogue_cleanup_done=0\ndialogue_cleanup_attempted=0\ndialogue_removal_attempted=0\ndialogue_root_removed=0\n"
        shell += "\n".join(name + "=" + shlex.quote(value) for name, value in values.items()) + "\n"
        shell += 'smoke_cleanup_env() { rm -rf -- "$dialogue_root"; }\n' + functions + "\ntrap dialogue_finish_cleanup EXIT\nexit 7\n"
        cleanup = subprocess.Popen(["bash", "-c", shell], cwd=self.repo,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            with selectors.DefaultSelector() as poller:
                poller.register(read_fd, selectors.EVENT_READ)
                self.assertTrue(poller.select(5), "delete intent did not follow writer capture")
            self.assertEqual(os.read(read_fd, 1), b"d")
            self.assertTrue(self.root.exists())
            self.assertIsNone(child.poll())
            self.assertIsNone(cleanup.poll())
        finally:
            DialogueCleanupTest.release(child)
        stdout, stderr = cleanup.communicate(timeout=5)
        self.assertEqual(cleanup.returncode, 7, (stdout, stderr))
        self.assertFalse(self.root.exists())

    def test_offline_failure_trap_does_not_call_outer_root_removal(self):
        finish = "dialogue_finish_cleanup() {" + self.source.split("dialogue_finish_cleanup() {", 1)[1].split("\ntrap dialogue_finish_cleanup EXIT", 1)[0]
        shell = ("set -euo pipefail\ndialogue_cleanup() { return 1; }\n"
                 "smoke_cleanup_env() { rm -rf -- " + shlex.quote(str(self.root)) + "; }\n" + finish +
                 "\ntrap dialogue_finish_cleanup EXIT\nexit 7\n")
        result = subprocess.run(["bash", "-c", shell], capture_output=True, text=True, timeout=5)
        self.assertEqual(result.returncode, 1)
        self.assertTrue(self.root.exists())
        self.assertIn("root retained", result.stderr)

    def test_failed_root_removal_is_not_retried_by_outer_exit(self):
        finish = "dialogue_finish_cleanup() {" + self.source.split("dialogue_finish_cleanup() {", 1)[1].split("\ntrap dialogue_finish_cleanup EXIT", 1)[0]
        removal = "dialogue_remove_root() {" + self.source.split("dialogue_remove_root() {", 1)[1].split("\nif ! dialogue_remove_root", 1)[0]
        attempts = pathlib.Path(self.temp.name, "attempts")
        shell = ("set -euo pipefail\ndialogue_removal_attempted=0\ndialogue_root_removed=0\n"
                 "dialogue_root=" + shlex.quote(str(self.root)) + "\nPROJMUX_SMOKE_WORKDIR=" + shlex.quote(self.temp.name) +
                 "\ndialogue_cleanup() { return 0; }\nrm() { echo attempt >> " + shlex.quote(str(attempts)) + "; return 1; }\n"
                 'smoke_cleanup_env() { rm -rf -- "$dialogue_root"; }\n' + finish + "\n" + removal +
                 "\ntrap dialogue_finish_cleanup EXIT\nif ! dialogue_remove_root; then exit 9; fi\n")
        result = subprocess.run(["bash", "-c", shell], capture_output=True, text=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(attempts.read_text(), "attempt\n")
        self.assertTrue(self.root.exists())
        self.assertIn("without retry", result.stderr)

    def test_stubborn_cwd_writer_times_out_without_signal_or_root_removal(self):
        child = self.writer()
        with self.assertRaisesRegex(RuntimeError, "root retained"):
            self.library["close_owned_writers"](self.root, lambda _: None, timeout=0.05)
        self.assertTrue(self.root.exists())
        self.assertIsNone(child.poll())

    def test_foreign_and_replaced_birth_cannot_receive_owned_role_signal(self):
        child = self.writer(pathlib.Path(self.temp.name, "foreign"))
        barrier = self.library["FixtureWriterBarrier"](self.root)
        try:
            identity = barrier.observe(child.pid)[0]
            with self.assertRaisesRegex(RuntimeError, "birth changed"):
                self.library["signal_owned"](barrier, identity)
            barrier.track(identity)
            for changed in (dict(identity, start=identity["start"] + "-replacement"),
                            dict(identity, ownerUID=identity["ownerUID"] + 1)):
                with self.assertRaisesRegex(RuntimeError, "birth changed"):
                    self.library["signal_owned"](barrier, changed)
            self.assertIsNone(child.poll())
            self.assertTrue(self.root.exists())
        finally:
            barrier.close()




class DialogueEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.repo = pathlib.Path(__file__).resolve().parents[1]
        self.code = runpy.run_path(str(self.repo / 'scripts/agent-dialogue-canary-evidence.py'))
        source = dict(agentUID='codex-agent', paneUID='codex-pane', activationGeneration='codex-generation', provider='codex', incarnation='route-codex')
        target = dict(agentUID='claude-agent', paneUID='claude-pane', activationGeneration='claude-generation', provider='claude', incarnation='route-claude')
        self.routes = dict(sender=source, receiver=target)
        conversation = 'conversation-' + hashlib.sha256(b'message-original').hexdigest()[:36]
        self.original = dict(version=2,messageRef='message-original', conversationRef=conversation, source=source, target=target, delivery=dict(state='delivered'))
        self.reply = dict(envelope=dict(version=2,messageRef='message-reply', conversationRef=conversation, replyTo='message-original', source=target, target=source,
                         authority=dict(kind='peer',trust='untrusted',permission='coordination-only'),payload='EXPECTED'), delivery=dict(state='delivered',reason='target-self-claim'))
        self.evidence = [dict(toolUseID='tool-owned',messageRef='message-original',targetAgentUID='codex-agent',replyRef='message-reply',resultObserved=True,guardSelectionMatched=True,guardedCommitMatched=True)]

    def validate(self, original=None, reply=None, evidence=None):
        return self.code['validate_reply'](original or self.original, reply or self.reply, self.evidence if evidence is None else evidence, self.routes, 'EXPECTED')

    def test_delivered_needs_separate_model_action_commit_and_exact_self_claim(self):
        proof = self.validate()
        self.assertTrue(proof['delivered'] and proof['exactCodexSelfClaim'])
        self.assertNotIn('payload', json.dumps(proof))
        for key, value in [('replyRef','wrong-reply'),('targetAgentUID','wrong-source'),('resultObserved',False),('guardSelectionMatched',False),('guardedCommitMatched',False)]:
            changed = copy.deepcopy(self.evidence); changed[0][key] = value
            with self.assertRaises(ValueError): self.validate(evidence=changed)
        for changed in ([],self.evidence+self.evidence):
            with self.assertRaises(ValueError): self.validate(evidence=changed)
        for key, value in [('version',1),('messageRef','message-original'),('authority',{}),('replyTo','wrong-original'),('conversationRef','wrong-conversation'),('payload','wrong-body'),('target',self.routes['receiver'])]:
            changed = copy.deepcopy(self.reply); changed['envelope'][key] = value
            with self.assertRaises(ValueError): self.validate(reply=changed)
        changed = copy.deepcopy(self.original); changed['delivery']['outcomeUnknown'] = True
        with self.assertRaises(ValueError): self.validate(original=changed)
        changed = copy.deepcopy(self.reply); changed['delivery']['reason'] = 'accepted'
        with self.assertRaises(ValueError): self.validate(reply=changed)

    def test_runtime_first_chain_rejects_ambiguous_replaced_or_wrong_owner(self):
        spec = dict(projectUID='project',windowUID='window',sender=dict(agentUID='codex-agent',paneUID='codex-pane',generation='codex-generation',paneID='%1'),receiver=dict(agentUID='claude-agent',paneUID='claude-pane',generation='claude-generation',paneID='%2'))
        registry = dict(projects=[dict(metadata=dict(uid='project'))],windows=[dict(metadata=dict(uid='window',ownerRef=dict(kind='Project',uid='project')))],agents=[],panes=[])
        runtime = {}
        for role, provider in [('sender','codex'),('receiver','claude')]:
            actor = spec[role]
            registry['agents'].append(dict(metadata=dict(uid=actor['agentUID'],ownerRef=dict(kind='Window',uid='window')),spec=dict(provider=provider),status=dict(phase='Running',paneRef=actor['paneUID'])))
            registry['panes'].append(dict(metadata=dict(uid=actor['paneUID'],ownerRef=dict(kind='Agent',uid=actor['agentUID'])),status=dict(activation=dict(agentUID=actor['agentUID'],generation=actor['generation'],runtimeID=actor['paneID']))))
            runtime[actor['paneID']] = actor['paneUID']
        self.code['runtime_chain'](spec,registry,runtime)
        changes = []
        duplicate = copy.deepcopy(registry); duplicate['panes'].append(duplicate['panes'][0]); changes.append(duplicate)
        replaced = copy.deepcopy(registry); replaced['panes'][0]['status']['activation']['generation'] = 'replaced'; changes.append(replaced)
        wrong = copy.deepcopy(registry); wrong['agents'][0]['status']['paneRef'] = 'claude-pane'; changes.append(wrong)
        for invalid in changes:
            with self.assertRaises(ValueError): self.code['runtime_chain'](spec,invalid,runtime)

    def test_owned_environment_discards_ambient_provider_and_routing_policy(self):
        setup=runpy.run_path(str(self.repo/'scripts/agent-dialogue-canary-setup.py'))
        env=setup['isolated_environment'](pathlib.Path('/owned/root'))
        self.assertTrue(all(key not in env for key in ('TMUX','TMUX_PANE','CLAUDE_CONFIG_DIR','CLAUDE_CODE_MESSAGING_TOKEN','CLAUDE_CODE_MESSAGING_SOCKET','PROJMUX_PROJDIR')))
        self.assertEqual(env['HOME'],'/owned/root/home')
        self.assertEqual(env['TMUX_TMPDIR'],'/owned/root/tmux')

    def test_prepare_pins_candidate_without_launching_or_private_collector(self):
        with tempfile.TemporaryDirectory(prefix='pmx-canary-prepare-') as temporary:
            parent = pathlib.Path(temporary); root = parent/'owned'; credential=parent/'auth'; credential.write_text('fixture authentication only'); credential.chmod(0o600)
            binary=parent/'candidate'; binary.write_text('#!/bin/sh\nexit 97\n'); binary.chmod(0o755)
            env=dict(os.environ,PMX_DIALOGUE_CANARY_ROOT=str(root),PMX_DIALOGUE_CANARY_RECEIPT=str(parent/'receipt'),PMX_DIALOGUE_PROJMUX_BIN=str(binary),PMX_DIALOGUE_REAL_CLAUDE_BIN=str(binary),PMX_DIALOGUE_REAL_CODEX_BIN=str(binary),PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE=str(credential),PMX_DIALOGUE_CODEX_AUTH_FILE=str(credential),PMX_DIALOGUE_CANDIDATE_HEAD='a'*40)
            result=subprocess.run(['bash',str(self.repo/'scripts/agent-dialogue-live-canary.sh'),'prepare'],env=env,capture_output=True,text=True,timeout=5)
            self.assertEqual(result.returncode,0,result.stderr)
            plan=json.loads((root/'cleanup-plan.json').read_text())
            self.assertEqual(plan['candidateSHA256'],hashlib.sha256(binary.read_bytes()).hexdigest())
            self.assertEqual(plan['candidateHead'],'a'*40)
            self.assertFalse((root/'evidence/provider.stdin').exists())
            self.assertFalse((root/'bin/collect-claude-public-jsonl').exists())
            self.assertFalse((root/'home/.claude/settings.json').exists())
            self.assertNotIn('sha256', (root/'evidence/auth-source-before.json').read_text())
            self.assertEqual((root/'home/.claude/.credentials.json').read_bytes(),credential.read_bytes())
            # Candidate exits97 if called: prepare must not launch any provider.
            self.assertEqual((root/'bin/claude').resolve(),binary)


class DialogueAuditTest(unittest.TestCase):
    def setUp(self):
        self.repo=pathlib.Path(__file__).resolve().parents[1]
        self.code=runpy.run_path(str(self.repo/'scripts/agent-dialogue-canary-setup.py'))
        self.temp=tempfile.TemporaryDirectory(prefix='pmx-audit-fixture-')
        self.addCleanup(self.temp.cleanup)
        self.parent=pathlib.Path(self.temp.name); self.root=self.parent/'owned'
        self.root.mkdir()
        for part in ('work','evidence','tmux','home/.claude'):
            (self.root/part).mkdir(parents=True,exist_ok=True)
        self.audit=self.code['Audit'].create(self.root,self.parent/'receipt')

    def records(self):
        return [json.loads(line) for line in self.audit.path.read_text().splitlines()]

    def test_each_public_setup_command_preserves_closed_stage_exit_and_byte_counts(self):
        stages=('claude-version','codex-version','tmux-create','tmux-socket','tmux-server',
                'tmux-project','project-create','project-get','reconcile','windows-get','sender-create','receiver-create')
        for stage in stages:
            with self.subTest(stage=stage), mock.patch.object(subprocess,'run',return_value=subprocess.CompletedProcess([],37,b'private output',b'private diagnostic')):
                with self.assertRaises(ValueError):
                    self.code['invoke_setup'](self.root,{},self.audit,stage,['never-executed'])
            self.assertEqual(self.records()[-1],dict(version=1,event='stage',stage=stage,exitCode=37,stdoutBytes=14,stderrBytes=18))
        text=self.audit.path.read_text()
        self.assertNotIn('private',text); self.assertNotIn('never-executed',text)
        self.assertEqual(self.audit.path.stat().st_mode&0o777,0o600)

    def test_window_parse_failure_is_identified_before_either_actor(self):
        path=self.root/'tmux/socket'
        with socket.socket(socket.AF_UNIX) as sock:
            sock.bind(str(path))
            calls=[]
            def invoke(stage,argv,**kwargs):
                self.audit.stage(stage); calls.append(stage)
                return {'tmux-create':'%0','tmux-socket':str(path),'tmux-server':'2','project-create':'project','project-get':json.dumps(dict(items=[dict(kind='Project',metadata=dict(uid='project'),spec=dict(root=str(self.root/'work')),status=dict(session=dict(name='registered-session')))])),'windows-get':'malformed'}.get(stage,'')
            with self.assertRaises(ValueError):
                self.code['setup'](self.root,'/fixture-candidate','fixture',invoke,self.audit.stage)
        self.assertEqual(self.records()[-1]['stage'],'window-validation')
        self.assertNotIn('sender-create',calls); self.assertNotIn('receiver-create',calls)

    def test_registered_project_session_projection_precedes_runtime_and_refuses_foreign(self):
        valid=dict(kind='Project',metadata=dict(uid='project'),spec=dict(root=str(self.root/'work')),status=dict(session=dict(name='canonical-owned-work')))
        class StopBeforeRuntime(Exception): pass
        calls=[]
        def invoke(stage,argv,**kwargs):
            calls.append((stage,argv))
            if stage=='project-create':return 'project\n'
            if stage=='project-get':
                self.assertEqual(argv,['/candidate','get','projects','--project','uid:project','-o','json'])
                return json.dumps(dict(items=[valid]))
            if stage=='tmux-create':raise StopBeforeRuntime()
            self.fail('unexpected stage')
        with self.assertRaises(StopBeforeRuntime):self.code['setup'](self.root,'/candidate','fixture',invoke,self.audit.stage)
        self.assertEqual([stage for stage,_ in calls],['project-create','project-get','tmux-create'])
        argv=calls[-1][1];self.assertEqual(argv[argv.index('-s')+1],'canonical-owned-work')
        for key,value in [('kind','Pane'),('metadata',dict(uid='foreign')),('spec',dict(root='/foreign')),('status',dict(session=dict(name=''))),('status',dict(session=dict(name='../foreign')))]:
            original=valid[key];valid[key]=value;calls.clear()
            with self.assertRaises(ValueError):self.code['setup'](self.root,'/candidate','fixture',invoke,self.audit.stage)
            self.assertNotIn('tmux-create',[stage for stage,_ in calls]);valid[key]=original

    @unittest.skipUnless(hasattr(os,'pidfd_open'),'Linux exact writer proof')
    def test_registered_project_failure_before_tmux_cleans_exact_uid_and_preserves_audit(self):
        for failure_stage in ('project-get','project-validation','tmux-create'):
            with self.subTest(stage=failure_stage):
                root=self.parent/failure_stage; root.mkdir()
                for part in ('work','evidence','tmux','home/.claude','xdg-state/projmux/metadata'):
                    (root/part).mkdir(parents=True,exist_ok=True)
                credential=root/'home/.claude/.credentials.json'; credential.write_text('fixture only')
                audit=self.code['Audit'].create(root,self.parent/(failure_stage+'-receipt'))
                item=dict(kind='Project',metadata=dict(uid='exact-project'),spec=dict(root=str(root/'work')),status=dict(session=dict(name='canonical-session')))
                calls=[]
                def invoke(stage,argv,**kwargs):
                    audit.stage(stage); calls.append(stage)
                    if stage==failure_stage: raise ValueError('injected setup failure')
                    if stage=='project-create':
                        (root/'xdg-state/projmux/metadata/registry.json').write_text(json.dumps(dict(projects=[item])))
                        return 'exact-project'
                    if stage=='project-get':
                        projected=copy.deepcopy(item)
                        if failure_stage=='project-validation': projected['metadata']['uid']='foreign'
                        return json.dumps(dict(items=[projected]))
                    self.fail('unexpected setup stage')
                with self.assertRaises(ValueError): self.code['setup'](root,'/candidate','fixture-owned',invoke,audit.stage)
                self.assertNotIn('sender-create',calls)
                self.assertFalse(any(root.joinpath('tmux').rglob('*')))
                identity=root.stat(); controls=[]
                def control(argv,**kwargs):
                    controls.append(argv)
                    if argv[-2:]==['-p','#{socket_path}']: return subprocess.CompletedProcess(argv,1,b'')
                    self.assertEqual(argv[-7:],['/candidate','delete','project','uid:exact-project','--socket','fixture-owned','--yes'])
                    self.assertIn('XDG_STATE_HOME='+str(root/'xdg-state'),argv)
                    return subprocess.CompletedProcess(argv,0,b'')
                with mock.patch.object(subprocess,'run',side_effect=control):
                    self.code['finish_setup_failure'](root,'/candidate','fixture-owned',(identity.st_dev,identity.st_ino),audit)
                self.assertEqual(len(controls),2)
                self.assertFalse(root.exists()); self.assertFalse(credential.exists())
                rows=[json.loads(line) for line in audit.path.read_text().splitlines()]
                proof=next(row for row in rows if row.get('outcome')=='writers-exited')
                self.assertTrue(proof['proof']['allCapturedWriterBirthsAbsent'])
                self.assertFalse(proof['rootAbsent'])
                self.assertEqual(rows[-1]['outcome'],'root-removed')

    def test_audit_rejects_occupied_symlink_replacement_and_bounds(self):
        with self.assertRaises(FileExistsError): self.code['Audit'].create(self.root,self.parent/'receipt')
        with self.assertRaises(ValueError): self.audit.stage('raw command text')
        self.audit.path.chmod(0o666)
        with self.assertRaises(ValueError): self.audit.stage('prepare')
        self.audit.path.chmod(0o600)
        for _ in range(128): self.audit.stage('prepare')
        with self.assertRaises(ValueError): self.audit.stage('prepare')
        self.audit.path.unlink(); self.audit.path.symlink_to(self.parent/'foreign')
        with self.assertRaises(OSError): self.audit.stage('prepare')
        self.assertFalse((self.parent/'foreign').exists())

    def test_early_prepare_failure_survives_without_root_or_raw_exception(self):
        shutil.rmtree(self.root)
        self.audit.path.unlink()
        env=dict(PMX_DIALOGUE_LIVE_CANARY='1',PMX_DIALOGUE_CANARY_ROOT=str(self.root),PMX_DIALOGUE_CANARY_RECEIPT=str(self.parent/'receipt'),PMX_DIALOGUE_PROJMUX_BIN='/bin/false')
        with mock.patch.dict(os.environ,env),mock.patch.object(subprocess,'run',side_effect=subprocess.CalledProcessError(73,['raw-secret-command'])):
            with self.assertRaises(ValueError): self.code['main']()
        rows=self.records()
        self.assertEqual(rows[1]['exitCode'],73)
        self.assertEqual(rows[-1]['event'],'failure')
        self.assertTrue(rows[-1]['rootAbsent']); self.assertNotIn('raw-secret',self.audit.path.read_text())

    def test_candidate_mode_is_rejected_before_credential_copy_or_actor(self):
        shutil.rmtree(self.root)
        credential=self.parent/'auth'; credential.write_text('fixture authentication only'); credential.chmod(0o600)
        candidate=self.parent/'candidate'; candidate.write_text('#!/bin/sh\nexit 97\n'); candidate.chmod(0o775)
        env=dict(os.environ,PMX_DIALOGUE_CANARY_ROOT=str(self.root),PMX_DIALOGUE_CANARY_RECEIPT=str(self.parent/'receipt'),PMX_DIALOGUE_PROJMUX_BIN=str(candidate),PMX_DIALOGUE_REAL_CLAUDE_BIN=str(candidate),PMX_DIALOGUE_REAL_CODEX_BIN=str(candidate),PMX_DIALOGUE_CLAUDE_CREDENTIAL_FILE=str(credential),PMX_DIALOGUE_CODEX_AUTH_FILE=str(credential),PMX_DIALOGUE_CANDIDATE_HEAD='a'*40)
        self.audit.path.unlink()
        env['PMX_DIALOGUE_LIVE_CANARY']='1'
        result=subprocess.run([sys.executable,str(self.repo/'scripts/agent-dialogue-canary-setup.py')],env=env,capture_output=True,text=True,timeout=5)
        self.assertNotEqual(result.returncode,0); self.assertFalse(self.root.exists())
        self.assertIn('without group/world write',result.stderr)
        self.assertEqual(credential.read_text(),'fixture authentication only')
        self.assertEqual(self.records()[0]['stage'],'prepare')
        self.assertNotEqual(self.records()[1]['exitCode'],0)
        self.assertTrue(self.records()[-1]['rootAbsent'])

    def test_run_audit_or_proof_failure_retains_root_without_success_receipt(self):
        source=(self.repo/'scripts/agent-dialogue-live-canary.sh').read_text()
        finish="finish_cleanup() {"+source.split("finish_cleanup() {",1)[1].split("\ntrap finish_cleanup EXIT",1)[0]
        identity=self.root.stat(); receipt=self.parent/'receipt'
        (self.root/'.projmux-dialogue-canary-owned').write_text('projmux-dialogue-canary-owned-v3\n')
        (self.root/'cleanup-plan.json').write_text(json.dumps(dict(version=3,ownedRoot=str(self.root),receiptPath=str(receipt),audit=self.audit.descriptor())))
        for unavailable in ('audit','proof'):
            with self.subTest(unavailable=unavailable):
                credential=self.root/'home/.claude/.credentials.json'; credential.write_text('fixture only')
                audit_command='return 1' if unavailable=='audit' else 'python3 '+shlex.quote(str(self.repo/'scripts/agent-dialogue-canary-setup.py'))+' audit "$root" "$@"'
                shell='set -euo pipefail\nroot='+shlex.quote(str(self.root))+'\nroot_identity='+str(identity.st_dev)+':'+str(identity.st_ino)+'\nreceipt_path='+shlex.quote(str(receipt))+'\ncanary_receipt_json="{}"\n'
                shell+='cleanup_owned() { rm -f -- "$root/home/.claude/.credentials.json"; }\naudit_event() { '+audit_command+'; }\n'+finish+'\ntrap finish_cleanup EXIT\ntrue\n'
                result=subprocess.run(['bash','-c',shell],capture_output=True,text=True,timeout=5)
                self.assertNotEqual(result.returncode,0)
                self.assertTrue(self.root.exists()); self.assertFalse(receipt.exists()); self.assertFalse(credential.exists())

    @unittest.skipUnless(hasattr(os,'pidfd_open'),'Linux exact writer proof')
    def test_uncertain_writer_retains_root_and_external_birth_proof(self):
        credential=self.root/'home/.claude/.credentials.json'; credential.write_text('fixture only')
        child=subprocess.Popen([sys.executable,'-c',"import sys;print('ready',flush=True);sys.stdin.readline()",str(self.root)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
        self.addCleanup(DialogueCleanupTest.release,child)
        self.assertEqual(child.stdout.readline().strip(),'ready')
        info=self.root.stat()
        with self.assertRaises(RuntimeError):
            self.code['finish_setup_failure'](self.root,'/bin/false','fixture',(info.st_dev,info.st_ino),self.audit,timeout=.05)
        self.assertTrue(self.root.exists()); self.assertIsNone(child.poll()); self.assertFalse(credential.exists())
        record=self.records()[-1]
        self.assertEqual(record['outcome'],'failed'); self.assertFalse(record['proof']['allCapturedWriterBirthsAbsent'])
        self.assertIn(child.pid,[row['pid'] for row in record['proof']['writers']])

    def test_early_cleanup_error_and_removal_error_retain_without_retry(self):
        credential=self.root/'home/.claude/.credentials.json'; credential.write_text('fixture only')
        info=self.root.stat()
        namespace=self.code['finish_setup_failure'].__globals__
        with mock.patch.dict(namespace,cleanup_partial=mock.Mock(side_effect=ValueError('private error'))):
            with self.assertRaises(ValueError): self.code['finish_setup_failure'](self.root,'/bin/false','fixture',(info.st_dev,info.st_ino),self.audit)
        self.assertTrue(self.root.exists()); self.assertFalse(credential.exists()); self.assertFalse(self.records()[-1]['proofAvailable'])
        (self.root/'evidence/cleanup-writers.json').write_text(json.dumps(dict(version=1,allCapturedWriterBirthsAbsent=True,writers=[])))
        with mock.patch.dict(namespace,cleanup_partial=mock.Mock()),mock.patch.object(shutil,'rmtree',side_effect=OSError('private remove error')) as remove:
            with self.assertRaises(OSError): self.code['finish_setup_failure'](self.root,'/bin/false','fixture',(info.st_dev,info.st_ino),self.audit)
            self.assertEqual(remove.call_count,1)
        self.assertTrue(self.root.exists()); self.assertEqual(self.records()[-1]['stage'],'root-removal')

    def test_policy_failure_audit_rejects_unknown_code_fields_and_phase(self):
        valid=dict(version=1,event='policy-failure',phase='before-source',code='policy-origin',substage='config-read-result',rejectionKind='unknown')
        for change in (dict(code='PRIVATE_BODY'),dict(detail='PRIVATE_BODY'),dict(phase='PRIVATE_BODY'),
                       dict(substage='PRIVATE_BODY'),dict(rejectionKind='PRIVATE_BODY')):
            with self.assertRaises(ValueError):self.audit.append(valid|change)
        self.assertEqual(self.audit.path.stat().st_size,0)
        self.audit.append(valid)
        self.assertEqual(self.records(),[valid])

    def policy_main_failure(self,code,*,uncertain=False,audit_write_failure=False,transient_audit_failure=False,transport_failure=False):
        # Run real main/finally and root removal with injected inert launch/read.
        # Existing synchronized writer tests exercise the underlying barrier.
        self.audit.path.unlink()
        (self.root/'codex-home').mkdir(exist_ok=True)
        for name in ('home/.claude/.credentials.json','codex-home/auth.json'):
            (self.root/name).write_text('PRIVATE_AUTH_FIXTURE')
        runpy_original=runpy.run_path
        native=runpy_original(str(self.repo/'scripts/agent-dialogue-native-source.py'))
        child=mock.Mock()
        def fail(*_):
            if audit_write_failure:self.audit.path.chmod(0o400)
            if transport_failure:
                from agent_dialogue_websocket_test import FakeServer
                observation=runpy_original(str(self.repo/'scripts/agent-dialogue-codex-observation.py'))
                transport=runpy_original(str(self.repo/'scripts/agent-dialogue-websocket.py'))
                schema=observation['Schemas'](self.repo/'scripts/agent-dialogue-codex-schema')
                observation=dict(observation,Schemas=lambda _:schema)
                raw=FakeServer(header_mutator=lambda _:b'PRIVATE_MALFORMED_HTTP\r\n\r\n')
                with mock.patch.object(runpy,'run_path',return_value=transport),raw:
                    with self.assertRaises(native['PolicyFailure']) as failure:native['initialize'](raw,observation,self.root)
                self.assertTrue(raw.closed);self.assertEqual(raw.frames,[])
                self.assertEqual((failure.exception.substage,failure.exception.kind),('upgrade','http-status'))
                raise failure.exception
            raise native['PolicyFailure'](code)
        native=dict(native,launch=mock.Mock(return_value=(child,{})),ready=mock.Mock(return_value={}),read_native_policy=fail,source_prompt=lambda _:'genuine-fixture')
        plan=dict(candidateHead='a'*40,candidateSHA256='b'*64,candidateBinary='/candidate',runnerFiles={},claudeVersion='1.0.0',codexVersion='1.0.0',sourceMode='genuine-native-task',sourcePrompt='genuine-fixture')
        (self.root/'cleanup-plan.json').write_text(json.dumps(plan))
        cleanup_calls=[]
        def cleanup(*_args,**_kwargs):
            cleanup_calls.append(True)
            rows=self.records()
            if not (audit_write_failure or transient_audit_failure):
                row=next(row for row in rows if row['event']=='policy-failure')
                self.assertEqual(row['code'],code)
                self.assertEqual((row['substage'],row['rejectionKind']),('upgrade','http-status') if transport_failure else ('unknown','unknown'))
            self.assertTrue(self.root.exists())
            (self.root/'evidence/cleanup-writers.json').write_text(json.dumps(dict(version=1,allCapturedWriterBirthsAbsent=not uncertain,writers=[])))
            if uncertain:raise ValueError('PRIVATE_UNCERTAIN_WRITER')
        setup=mock.Mock(side_effect=AssertionError('must not create actors'))
        env=dict(PMX_DIALOGUE_LIVE_CANARY='1',PMX_DIALOGUE_CANARY_ROOT=str(self.root),PMX_DIALOGUE_CANARY_RECEIPT=str(self.parent/'receipt'),PMX_DIALOGUE_PROJMUX_BIN='/candidate')
        namespace=self.code['main'].__globals__
        original_append=self.code['Audit'].append
        def append(audit,row):
            if transient_audit_failure and row['event']=='policy-failure':raise OSError('PRIVATE_TRANSIENT_AUDIT')
            return original_append(audit,row)
        with mock.patch.object(self.code['Audit'],'append',append), \
             mock.patch.dict(namespace,invoke_setup=lambda *_args,**_kwargs:'1.0.0',setup=setup,cleanup_partial=cleanup), \
             mock.patch.dict(os.environ,env),mock.patch.object(runpy,'run_path',return_value=native), \
             mock.patch.object(subprocess,'run',return_value=subprocess.CompletedProcess([],0)):
            with self.assertRaises(ValueError):self.code['main']()
        setup.assert_not_called();self.assertEqual(cleanup_calls,[True])
        self.assertFalse((self.parent/'receipt').exists())
        self.assertEqual(self.root.exists(),uncertain or audit_write_failure or transient_audit_failure)
        for name in ('home/.claude/.credentials.json','codex-home/auth.json'):
            self.assertFalse((self.root/name).exists())
        text=self.audit.path.read_text();self.assertNotIn('PRIVATE_',text)
        if not (uncertain or audit_write_failure or transient_audit_failure):
            child.wait.assert_called_once_with(timeout=.1)
            rows=self.records();self.assertEqual(rows[-2]['event'],'terminal');self.assertEqual(rows[-2]['exitCode'],1)
            code_index=next(i for i,row in enumerate(rows) if row['event']=='policy-failure')
            proof_index=next(i for i,row in enumerate(rows) if row.get('outcome')=='writers-exited')
            removal_index=next(i for i,row in enumerate(rows) if row.get('outcome')=='root-removed')
            self.assertLess(code_index,proof_index);self.assertLess(proof_index,removal_index)

    def test_policy_failure_code_survives_real_setup_finally_and_removal(self):
        self.policy_main_failure('policy-origin')

    def test_policy_failure_with_uncertain_writer_retains_root_and_code(self):
        self.policy_main_failure('policy-socket',uncertain=True)

    def test_policy_code_audit_write_failure_cannot_create_success_or_remove_root(self):
        self.policy_main_failure('policy-value',audit_write_failure=True)

    def test_websocket_upgrade_failure_closes_connection_then_parent_cleans_once(self):
        self.policy_main_failure('policy-request',transport_failure=True)

    def test_transient_policy_audit_failure_remains_retained_after_sink_recovers(self):
        self.policy_main_failure('policy-request',transient_audit_failure=True)


if __name__ == "__main__":
    unittest.main()
