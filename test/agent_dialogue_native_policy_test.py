"""Public config/read offline fixtures; no daemon/provider/authentication."""
import copy
import json
import pathlib
import runpy
import tempfile
import unittest
from unittest import mock


REPO=pathlib.Path(__file__).resolve().parents[1]


class NativePolicyTests(unittest.TestCase):
    def setUp(self):
        self.module=runpy.run_path(str(REPO/'scripts/agent-dialogue-native-policy.py'))
        self.observation=runpy.run_path(str(REPO/'scripts/agent-dialogue-codex-observation.py'))
        self.root=pathlib.Path('/owned/root')
        self.reader=self.module['PolicyReader'](self.root,pathlib.Path('/owned/package'),REPO/'scripts/agent-dialogue-config-schema',self.observation)
        self.value=dict(config={},origins={})
        for key,value in self.reader.expected.items():
            node=self.value['config'];parts=key.split('.')
            for part in parts[:-1]:node=node.setdefault(part,{})
            node[parts[-1]]=copy.deepcopy(value)
            self.value['origins'][key]=dict(name=dict(type='user',file='/owned/root/codex-home/config.toml'),version='PRIVATE_OPAQUE_REVISION')

    def test_exact_policy_projection_drops_unrelated_private_values(self):
        self.value['config']['private_unknown_extension']={'value':'DO_NOT_PERSIST'}
        self.value['config']['model']='DO_NOT_PERSIST'
        facts=self.reader.project(self.value)
        self.assertEqual(facts['values'],self.reader.expected)
        self.assertEqual(set(facts['origins'].values()),{'owned-user-config'})
        self.assertFalse(facts['threadPolicyObserved'])
        self.assertFalse(facts['rawValuesRetained'])
        encoded=json.dumps(facts)
        self.assertNotIn('DO_NOT_PERSIST',encoded)
        self.assertNotIn('PRIVATE_OPAQUE_REVISION',encoded)
        self.assertNotIn('private_unknown_extension',encoded)

    def test_every_selected_policy_mismatch_and_missing_value_fails_closed(self):
        for key in self.reader.expected:
            for missing in (False,True):
                value=copy.deepcopy(self.value);node=value['config'];parts=key.split('.')
                for part in parts[:-1]:node=node[part]
                if missing:del node[parts[-1]]
                else:node[parts[-1]]='WRONG_PRIVATE_VALUE'
                with self.subTest(key=key,missing=missing),self.assertRaises(ValueError) as error:self.reader.project(value)
                self.assertNotIn('WRONG_PRIVATE_VALUE',str(error.exception))

    def test_boolean_integer_equivalence_is_not_accepted(self):
        self.value['config']['sandbox_workspace_write']['network_access']=0
        with self.assertRaises(ValueError):self.reader.project(self.value)

    def test_selected_origin_missing_or_parent_only_is_not_inferred(self):
        del self.value['origins']['sandbox_workspace_write.network_access']
        self.value['origins']['sandbox_workspace_write']=dict(name=dict(type='user',file='/owned/root/codex-home/config.toml'),version='v')
        with self.assertRaisesRegex(ValueError,'origin-missing'):self.reader.project(self.value)

    def test_foreign_managed_project_session_and_unknown_origins_refused(self):
        cases=[dict(type='system',file='/etc/codex/config.toml'),dict(type='project',dotCodexFolder='/foreign/.codex'),
               dict(type='sessionFlags'),dict(type='enterpriseManaged',id='private',name='private'),dict(type='invented')]
        for source in cases:
            value=copy.deepcopy(self.value);value['origins']['unrelated']=dict(name=source,version='v')
            with self.subTest(source=source['type']),self.assertRaises(ValueError):self.reader.project(value)

    def test_wrong_user_path_profile_and_unpinned_packaged_origin_refused(self):
        for source in (dict(type='user',file='/ambient/config.toml'),
                       dict(type='user',file='/owned/root/codex-home/config.toml',profile='foreign'),
                       dict(type='packagedDefaults',file='/foreign/package/config.toml')):
            value=copy.deepcopy(self.value);value['origins']['unrelated']=dict(name=source,version='v')
            with self.assertRaises(ValueError):self.reader.project(value)
        self.value['origins']['model']=dict(name=dict(type='packagedDefaults',file='/owned/package/default.toml'),version='v')
        self.assertEqual(self.reader.project(self.value)['originCounts']['packagedDefaults'],1)

    def test_layers_or_execution_inputs_are_not_retained_or_admitted(self):
        for key in ('mcp_servers','plugins','hooks','instructions','developer_instructions'):
            value=copy.deepcopy(self.value);value['config'][key]='DO_NOT_PERSIST'
            with self.subTest(key=key),self.assertRaises(ValueError) as error:self.reader.project(value)
            self.assertNotIn('DO_NOT_PERSIST',str(error.exception))
        self.value['layers']=[dict(config={'private':'DO_NOT_PERSIST'},name=dict(type='user',file='/owned/root/codex-home/config.toml'),version='v')]
        with self.assertRaises(ValueError):self.reader.project(self.value)

    def test_schema_hash_mismatch_refuses_before_any_connection(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder=pathlib.Path(temporary)
            for name in self.module['HASHES']:(folder/name).write_bytes((REPO/'scripts/agent-dialogue-config-schema'/name).read_bytes()+b' ')
            with self.assertRaisesRegex(ValueError,'schema-hash'):
                self.module['PolicyReader'](self.root,pathlib.Path('/owned/package'),folder,self.observation)

    def connection(self,raw):
        class Connection:
            def __init__(self):self.data=raw;self.sent=[]
            def settimeout(self,value):assert 0<value<=5
            def sendall(self,value):self.sent.append(json.loads(value))
            def read_message(self,size):
                if not self.data or len(self.data)>size:raise OSError('fixture message unavailable')
                value,self.data=self.data,b'';return value
        return Connection()

    def test_only_config_read_exact_cwd_no_raw_layers_or_thread_start(self):
        connection=self.connection(json.dumps(dict(id=1,result=self.value)).encode()+b'\n')
        self.reader.read(connection)
        self.assertEqual(connection.sent,[dict(id=1,method='config/read',params=dict(cwd='/owned/root/work',includeLayers=False))])

    def test_eof_unknown_notification_extra_frame_and_oversize_refused(self):
        for raw in (b'',b'x'*(1024*1024),b'{"method":"unknown","params":{}}\n',
                    json.dumps(dict(id=1,result=self.value)).encode()+b'\n{}\n'):
            with self.subTest(size=len(raw)),self.assertRaises(ValueError):self.reader.read(self.connection(raw))

    def test_origin_cardinality_and_unknown_selected_origin_fields_refused(self):
        origin=copy.deepcopy(self.value['origins']['approval_policy'])
        self.value['origins']={str(i):origin for i in range(129)}
        with self.assertRaises(ValueError):self.reader.project(self.value)
        self.value['origins']={'approval_policy':dict(origin,private='DO_NOT_PERSIST')}
        with self.assertRaises(ValueError):self.reader.project(self.value)

    def test_failure_codes_are_closed_and_do_not_contain_rejected_values(self):
        cases=[]
        bad=copy.deepcopy(self.value);bad['unexpected']='PRIVATE_BODY';cases.append(('policy-schema',lambda:self.reader.project(bad)))
        mismatch=copy.deepcopy(self.value);mismatch['config']['approval_policy']='on-request';cases.append(('policy-value',lambda:self.reader.project(mismatch)))
        origin=copy.deepcopy(self.value);origin['origins']['approval_policy']['name']['file']='/PRIVATE_BODY';cases.append(('policy-origin',lambda:self.reader.project(origin)))
        cases.append(('policy-request',lambda:self.reader.read(self.connection(b''))))
        cases.append(('policy-schema',lambda:self.reader.read(self.connection(b'PRIVATE_BODY\n'))))
        for code,action in cases:
            with self.subTest(code=code),self.assertRaises(self.module['Refused']) as failure:action()
            self.assertEqual(failure.exception.code,code)
            self.assertNotIn('PRIVATE_BODY',str(failure.exception))
        with self.assertRaises(ValueError):self.module['Refused']('ignored',code='PRIVATE_BODY')

    def test_request_io_exception_is_replaced_by_closed_code(self):
        connection=self.connection(b'')
        connection.read_message=mock.Mock(side_effect=OSError('PRIVATE_SOCKET_AND_SECRET'))
        with self.assertRaises(self.module['Refused']) as failure:self.reader.read(connection)
        self.assertEqual(failure.exception.code,'policy-request')
        self.assertEqual(str(failure.exception),'policy-refused')


if __name__=='__main__':unittest.main()
