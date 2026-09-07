"""Closed public JSON-RPC forms; no socket/provider/authentication execution."""
import copy
import itertools
import json
import pathlib
import runpy
import unittest


REPO=pathlib.Path(__file__).resolve().parents[1]


class RPCEnvelopeTests(unittest.TestCase):
    def setUp(self):
        self.observation=runpy.run_path(str(REPO/'scripts/agent-dialogue-codex-observation.py'))
        self.schemas=self.observation['Schemas'](REPO/'scripts/agent-dialogue-codex-schema')
        self.classify=self.observation['response_rejection_kind']
        self.facts=self.observation['response_envelope_facts']
        self.validate_facts=runpy.run_path(str(REPO/'scripts/agent-dialogue-canary-setup.py'))['validate_envelope_facts']
        self.forms=[
            ('JSONRPCResponse.json',dict(id=1,result={'private':'PRIVATE_RESULT'}),None),
            ('JSONRPCError.json',dict(id=1,error=dict(code=-32601,message='PRIVATE_ERROR',data={'private':'PRIVATE_DATA'})),'envelope-error'),
            ('JSONRPCNotification.json',dict(method='PRIVATE_METHOD',params={'private':'PRIVATE_PARAMS'}),'envelope-notification'),
            ('JSONRPCRequest.json',dict(id=1,method='PRIVATE_METHOD',params={'private':'PRIVATE_PARAMS'},
                trace=dict(traceparent='PRIVATE_TRACE',tracestate=None)),'envelope-request'),
        ]

    def test_pinned_public_forms_only_classify_and_never_return_private_members(self):
        for schema,value,expected in self.forms:
            self.schemas.validate(schema,value)
            self.assertEqual(self.classify(value,1),expected)
            self.assertNotIn('PRIVATE_',json.dumps(self.classify(value,1)))
            if 'params' in value:
                minimal=copy.deepcopy(value);del minimal['params']
                self.schemas.validate(schema,minimal);self.assertEqual(self.classify(minimal,1),expected)
        for trace in (None,{},dict(traceparent=None),dict(tracestate='PRIVATE_TRACE')):
            request=dict(id=1,method='PRIVATE_METHOD',trace=trace)
            self.schemas.validate('JSONRPCRequest.json',request)
            self.assertEqual(self.classify(request,1),'envelope-request')

    def test_wrong_or_foreign_request_id_never_becomes_success_or_skippable(self):
        for _,value,_ in self.forms:
            if 'id' not in value:continue
            for request_id in (0,2,'1',None,True,[],{}):
                self.assertEqual(self.classify(value|dict(id=request_id),1),'envelope-id')

    def test_unknown_keys_and_mixed_forms_remain_generic_refusals(self):
        for _,value,_ in self.forms:
            self.assertEqual(self.classify(value|dict(unknown='PRIVATE_UNKNOWN'),1),'envelope-shape')
        for value in ([],None,dict(id=1),dict(result='PRIVATE_RESULT'),
                      dict(id=1,method='PRIVATE_METHOD',result={}),
                      dict(id=1,result={},error={}),
                      dict(id=1,jsonrpc='2.0',result={}),
                      dict(method='PRIVATE_METHOD',trace={})):
            self.assertEqual(self.classify(value,1),'envelope-shape')

    def test_malformed_error_method_or_trace_is_not_labeled_a_public_form(self):
        cases=[dict(id=1,error=None),dict(id=1,error='PRIVATE_ERROR'),
               dict(id=1,error=dict(code=True,message='PRIVATE_ERROR')),
               dict(id=1,error=dict(code=2**63,message='PRIVATE_ERROR')),
               dict(id=1,error=dict(code=1,message={})),
               dict(id=1,error=dict(code=1,message='PRIVATE_ERROR',unknown='PRIVATE_UNKNOWN')),
               dict(method={}),dict(method=''),dict(id=1,method=1),
               dict(id=1,method='PRIVATE_METHOD',trace='PRIVATE_TRACE'),
               dict(id=1,method='PRIVATE_METHOD',trace=dict(traceparent=1)),
               dict(id=1,method='PRIVATE_METHOD',trace=dict(unknown='PRIVATE_UNKNOWN'))]
        for value in cases:self.assertEqual(self.classify(value,1),'envelope-shape')

    def test_all_known_field_combinations_keep_the_exact_success_allowlist(self):
        members=dict(id=1,result='PRIVATE_RESULT',error=dict(code=1,message='PRIVATE_ERROR'),
                     method='PRIVATE_METHOD',params='PRIVATE_PARAMS',trace={'traceparent':'PRIVATE_TRACE'})
        for present in itertools.product((False,True),repeat=len(members)):
            value={key:members[key] for key,keep in zip(members,present) if keep}
            expected_success=set(value)=={'id','result'}
            before=copy.deepcopy(value)
            facts=self.facts(value,1)
            self.validate_facts(facts)
            self.assertEqual(self.classify(value,1) is None,expected_success)
            self.assertEqual(value,before)
            self.assertNotIn('PRIVATE_',json.dumps(facts))
            self.assertLess(len(json.dumps(facts)),1024)
            # Unknown fields remain rejected even on an otherwise valid reply.
            self.assertEqual(self.classify(value|{'PRIVATE_KEY':'PRIVATE_VALUE'},1),'envelope-shape')
            self.assertTrue(self.facts(value|{'PRIVATE_KEY':'PRIVATE_VALUE'},1)['unknownFields'])

    def test_remaining_shape_branches_have_complete_closed_predicates(self):
        cases=[
            (None,{'topLevel':'null'}),(True,{'topLevel':'boolean'}),(1,{'topLevel':'integer'}),
            (1.5,{'topLevel':'number'}),('PRIVATE_BODY',{'topLevel':'string'}),([] ,{'topLevel':'array'}),
            ({},{'topLevel':'object','id':'absent','resultPresent':False,'method':'absent'}),
            (dict(id='PRIVATE_ID'),{'id':'other-type'}),(dict(id=True),{'id':'other-type'}),
            (dict(id=42),{'id':'other-integer'}),(dict(id=1),{'id':'expected-integer'}),
            (dict(error=None),{'error':'other-type'}),
            (dict(error={}),{'error':'object','errorCode':'absent','errorMessage':'absent'}),
            (dict(error=dict(code=False,message=1)),{'errorCode':'other-type','errorMessage':'other-type'}),
            (dict(error=dict(code=2**63,message='PRIVATE_ERROR')),{'errorCode':'integer-out-of-range','errorMessage':'string'}),
            (dict(error=dict(code=-(2**63)-1)),{'errorCode':'integer-out-of-range'}),
            (dict(error=dict(code=-(2**63),PRIVATE_KEY='PRIVATE_VALUE')),{'errorCode':'int64','errorUnknownFields':True}),
            (dict(method=1),{'method':'other-type'}),(dict(method=''),{'method':'empty-string'}),
            (dict(method='PRIVATE_METHOD'),{'method':'nonempty-string'}),
            (dict(trace=None),{'trace':'null','traceValuesValid':False}),
            (dict(trace='PRIVATE_TRACE'),{'trace':'other-type'}),
            (dict(trace={}),{'trace':'object','traceValuesValid':True}),
            (dict(trace=dict(traceparent=1)),{'traceValuesValid':False}),
            (dict(trace=dict(PRIVATE_KEY='PRIVATE_VALUE')),{'traceUnknownFields':True,'traceValuesValid':True}),
        ]
        for value,expected in cases:
            with self.subTest(expected=expected):
                facts=self.facts(value,1);self.validate_facts(facts)
                for key,want in expected.items():self.assertEqual(facts[key],want)
                self.assertIsNotNone(self.classify(value,1))
                self.assertNotIn('PRIVATE_',json.dumps(facts))
        # Simultaneous faults are visible together, without priority losing one.
        value=dict(id=1,result={},method=0,error=dict(code=True,message=0,PRIVATE_KEY=None),trace=dict(traceparent=0),PRIVATE_KEY=None)
        facts=self.facts(value,1)
        for key in ('unknownFields','resultPresent','errorUnknownFields'):self.assertTrue(facts[key])
        self.assertEqual((facts['errorCode'],facts['errorMessage'],facts['method']),('other-type',)*3)
        self.assertFalse(facts['traceValuesValid'])

    def test_private_values_and_dynamic_names_never_enter_projection(self):
        left=dict(id=1,error=dict(code=1,message='PRIVATE_ONE',data={'PRIVATE_KEY':'PRIVATE_VALUE'}),
                  method='PRIVATE_METHOD',params={'PRIVATE_KEY':'PRIVATE_BODY'},trace={'PRIVATE_KEY':'PRIVATE_TRACE'},PRIVATE_KEY='PRIVATE_SECRET')
        right=dict(id=1,error=dict(code=9,message='OTHER_MESSAGE',data=None),method='OTHER_METHOD',
                   params=None,trace={'OTHER_KEY':'OTHER_TRACE'},OTHER_KEY='OTHER_SECRET')
        self.assertEqual(self.facts(left,1),self.facts(right,1))
        self.assertEqual(self.classify(left,1),self.classify(right,1))

    def test_audit_sink_rejects_unknown_missing_nonboolean_or_nonenum_facts(self):
        valid=self.facts(dict(id=1,result={},unknown='PRIVATE_BODY'),1)
        for key,value in valid.items():
            missing=dict(valid);del missing[key]
            with self.assertRaises(ValueError):self.validate_facts(missing)
            with self.assertRaises(ValueError):self.validate_facts(valid|{key:'PRIVATE_BODY'})
            if type(value) is bool:
                with self.assertRaises(ValueError):self.validate_facts(valid|{key:1})
        with self.assertRaises(ValueError):self.validate_facts(valid|{'PRIVATE_KEY':'PRIVATE_BODY'})
