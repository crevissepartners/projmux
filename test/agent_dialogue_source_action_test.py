"""Offline public-command fixtures; no model, provider, auth or native daemon."""
import copy
import hashlib
import json
import pathlib
import runpy
import tempfile
import unittest
from unittest import mock


class SourceActionTest(unittest.TestCase):
    def setUp(self):
        repo = pathlib.Path(__file__).resolve().parents[1]
        self.action = runpy.run_path(str(repo / 'scripts/agent-dialogue-source-action.py'))
        self.evidence = runpy.run_path(str(repo / 'scripts/agent-dialogue-canary-evidence.py'))
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        (self.root / 'evidence').mkdir()
        self.spec = dict(binary='/owned/candidate', sender=dict(agentUID='codex-agent', paneID='%1'),
                         receiver=dict(agentUID='claude-agent'), messageRef='message-idle',
                         tmuxSocketPath='/owned/tmux/socket', tmuxServerPID=123)
        self.routes = dict(sender=dict(agentUID='codex-agent', paneUID='codex-pane', activationGeneration='codex-generation', provider='codex', incarnation='route-codex'),
                           receiver=dict(agentUID='claude-agent', paneUID='claude-pane', activationGeneration='claude-generation', provider='claude', incarnation='route-claude'))
        self.initial = dict(routes=self.routes, authority={'fixture': 'authority'}, tmuxProcess={'fixture': 'process'},
                            candidateSHA256='a' * 64, candidateHead='b' * 40, codexCompositeAuthority={'fixture': 'composite'})
        self.commands = []
        self.last_ref = None
        self.effects = []
        self.invalid_reply = False
        self.unknown_send = False
        self.changed_route = False
        self.evidence['snapshot'] = self.snapshot

    def snapshot(self, *_args, **_kwargs):
        value = copy.deepcopy(self.initial)
        value['toolEvidence'] = copy.deepcopy(self.effects)
        if self.changed_route:
            value['codexCompositeAuthority'] = {'fixture': 'replaced'}
        return value

    def original(self, ref):
        return dict(version=2, messageRef=ref, conversationRef='conversation-' + hashlib.sha256(ref.encode()).hexdigest()[:36],
                    source=self.routes['sender'], target=self.routes['receiver'], delivery=dict(state='delivered'))

    def reply(self, ref):
        original = self.original(ref)
        marker = ('HETEROGENEOUS_QUALIFIED:' if ref == 'message-qualification' else 'HETEROGENEOUS_REPLY:') + ref
        return dict(envelope=dict(version=2, messageRef=ref + '-reply', conversationRef=original['conversationRef'],
                                 replyTo=ref, source=self.routes['receiver'], target=self.routes['sender'],
                                 authority=dict(kind='peer', trust='untrusted', permission='coordination-only'),
                                 payload='WRONG PRIVATE BODY' if self.invalid_reply else marker),
                    delivery=dict(state='delivered', reason='target-self-claim'))

    def effect(self, ref):
        return dict(toolUseID='tool-' + ref, messageRef=ref, targetAgentUID='codex-agent', replyRef=ref + '-reply',
                    resultObserved=True, guardSelectionMatched=True, guardedCommitMatched=True)

    def invoke(self, argv, env, cwd):
        self.commands.append(argv)
        self.assertEqual(argv[:3], ['/owned/candidate', 'agent', 'message'])
        self.assertEqual(env['TMUX_PANE'], '%1')
        self.assertEqual(env['TMUX'], '/owned/tmux/socket,123,0')
        self.assertEqual(cwd, self.root / 'work')
        operation = argv[3]
        if operation == 'qualify':
            self.assertEqual(argv[4:], ['uid:claude-agent', '--confirm-isolated-provider-push', '-o', 'json'])
            self.last_ref = 'message-qualification'
            self.effects.append(self.effect(self.last_ref))
            value = dict(state='qualification-qualified', qualificationRef=self.last_ref,
                         evidence='owned-public-init-plus-broker-explicit-reply', ambiguous=False, autoResend=False)
        elif operation == 'send':
            self.assertEqual(argv[4:11], ['--message-ref', 'message-idle', '--ttl', '2m', 'uid:claude-agent', '--',
                                         'For this local transport acknowledgement, execute the permitted public reply command for this request with text HETEROGENEOUS_REPLY:message-idle.'])
            self.last_ref = 'message-idle'
            if self.unknown_send:
                return 1, b'', b'PRIVATE UNKNOWN WRITE OUTCOME'
            self.effects.append(self.effect(self.last_ref))
            return 0, b'accepted', b''
        elif operation == 'wait':
            self.assertEqual(argv[4], 'uid:codex-agent')
            if argv[6] == '1ms':
                return 1, b'', b'agent message wait: timed out with no compatible message\n'
            value = self.reply(self.last_ref)
        elif operation == 'status':
            value = self.original(argv[4])
        else:
            self.fail('unexpected public operation')
        return 0, json.dumps(value).encode(), b''

    def execute(self):
        return self.action['execute'](self.root, self.spec, self.initial, self.evidence, self.invoke)

    def test_exact_public_qualify_claim_send_claim_and_closed_result(self):
        value = self.execute()
        self.assertEqual([row[3] for row in self.commands], ['wait', 'qualify', 'wait', 'status', 'wait', 'send', 'wait', 'status', 'wait'])
        self.assertEqual(set(value), {'version', 'sourceAgentUID', 'targetAgentUID', 'qualification', 'idle'})
        self.assertNotEqual(value['qualification']['originalRef'], value['idle']['originalRef'])
        for phase in ('qualification', 'idle'):
            self.assertTrue(value[phase]['selfClaimed'] and value[phase]['guardedCommitMatched'])
        serialized = json.dumps(value)
        self.assertNotIn('payload', serialized)
        self.assertNotIn('HETEROGENEOUS_', serialized)
        self.assertNotIn('stderr', serialized)
        self.assertEqual(len(list((self.root / 'evidence').glob('*proof.json'))), 2)
        self.assertTrue(self.root.exists())  # Action never invokes fleet/root cleanup.

    def test_replaced_live_source_fence_rejects_before_any_public_dispatch(self):
        self.changed_route = True
        with self.assertRaisesRegex(ValueError, 'frozen route changed'):
            self.execute()
        self.assertEqual(self.commands, [])

    def test_wrong_peer_body_stops_before_idle_without_body_exposure(self):
        self.invalid_reply = True
        with self.assertRaises(ValueError) as caught:
            self.execute()
        self.assertNotIn('PRIVATE', str(caught.exception))
        self.assertEqual(sum(row[3] == 'qualify' for row in self.commands), 1)
        self.assertFalse(any(row[3] == 'send' for row in self.commands))
        self.assertFalse(list((self.root / 'evidence').iterdir()))

    def test_unknown_send_outcome_is_not_repeated(self):
        self.unknown_send = True
        with self.assertRaises(ValueError) as caught:
            self.execute()
        self.assertNotIn('PRIVATE', str(caught.exception))
        self.assertEqual(sum(row[3] == 'send' for row in self.commands), 1)
        self.assertEqual(sum(row[3] == 'wait' and row[6] == '120s' for row in self.commands), 1)

    def test_environment_contains_only_owned_context_and_no_inherited_secrets(self):
        with mock.patch.dict('os.environ', {'TMUX': 'ambient', 'CODEX_ACCESS_TOKEN': 'fixture-secret', 'CLAUDE_CONFIG_DIR': '/ambient'}):
            env = self.action['own_environment'](self.root, self.spec)
        self.assertNotIn('CODEX_ACCESS_TOKEN', env)
        self.assertNotIn('CLAUDE_CONFIG_DIR', env)
        self.assertEqual(env['CODEX_SQLITE_HOME'], str(self.root / 'codex-home'))


if __name__ == '__main__':
    unittest.main()
