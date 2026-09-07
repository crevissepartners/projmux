"""Closed public JSON-RPC forms; no socket/provider/authentication execution."""
import copy
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
