"""RFC6455 memory peers only: no socket/provider/authentication execution."""
import base64
import hashlib
import json
import os
import pathlib
import runpy
import shutil
import socket
import struct
import tempfile
import unittest
from unittest import mock

REPO=pathlib.Path(__file__).resolve().parents[1]
WS=runpy.run_path(str(REPO/'scripts/agent-dialogue-websocket.py'))


def frame(payload=b'{}',opcode=1,fin=True):
    if isinstance(payload,str):payload=payload.encode()
    length=len(payload)
    header=bytes(((0x80 if fin else 0)|opcode,length)) if length<126 else bytes(((0x80 if fin else 0)|opcode,126))+struct.pack('!H',length) if length<=65535 else bytes(((0x80 if fin else 0)|opcode,127))+struct.pack('!Q',length)
    return header+payload


class FakeServer:
    family=socket.AF_UNIX
    def __init__(self,handler=lambda _:None,header_mutator=lambda value:value):
        self.handler=handler;self.header_mutator=header_mutator;self.incoming=bytearray();self.frames=[];self.upgraded=False;self.closed=False
    def settimeout(self,value):assert value>0
    def getsockopt(self,*_):return struct.pack('3i',4242,os.getuid(),os.getgid())
    def close(self):self.closed=True
    def __enter__(self):return self
    def __exit__(self,*_):self.close()
    def recv(self,size):
        chunk=bytes(self.incoming[:min(size,17)]);del self.incoming[:len(chunk)];return chunk
    def sendall(self,data):
        if not self.upgraded:
            if not data.startswith(b'GET / HTTP/1.1\r\n'):raise OSError('fixture requires HTTP upgrade')
            headers=dict(line.split(b': ',1) for line in data.split(b'\r\n')[1:] if b': ' in line)
            key=headers[b'Sec-WebSocket-Key'];assert len(base64.b64decode(key))==16
            accept=base64.b64encode(hashlib.sha1(key+WS['GUID']).digest())
            response=b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: '+accept+b'\r\n\r\n'
            self.incoming.extend(self.header_mutator(response));self.upgraded=True;return
        assert data[0]&0x80 and data[1]&0x80
        size=data[1]&127;offset=2
        if size==126:size=struct.unpack('!H',data[2:4])[0];offset=4
        elif size==127:size=struct.unpack('!Q',data[2:10])[0];offset=10
        mask=data[offset:offset+4];payload=bytes(value^mask[i%4] for i,value in enumerate(data[offset+4:]))
        assert len(payload)==size
        opcode=data[0]&15;self.frames.append((opcode,payload))
        if opcode==1:
            response=self.handler(json.loads(payload))
            if response is not None:self.incoming.extend(frame(json.dumps(response)))


class WebSocketTests(unittest.TestCase):
    def connected(self,**kwargs):
        raw=FakeServer(**kwargs);return raw,WS['MessageConnection'](raw).upgrade()

    def test_upgrade_masked_write_and_message_boundaries(self):
        raw,connection=self.connected()
        for size in (4,126,65536):
            connection.sendall(json.dumps('x'*size).encode()+b'\n')
            self.assertEqual(json.loads(raw.frames[-1][1]),'x'*size)
        raw.incoming.extend(frame(b'{\n"first":1\n}')+frame(b'{"second":2}'))
        self.assertEqual(json.loads(connection.read_message()),{'first':1})
        self.assertEqual(json.loads(connection.read_message()),{'second':2})
        self.assertEqual(connection.getsockopt(0,0),raw.getsockopt())

    def test_fragmented_unicode_with_interleaved_ping_preserves_text(self):
        raw,connection=self.connected()
        payload='{"value":"한글"}'.encode();cut=payload.index('한'.encode())+1
        raw.incoming.extend(frame(payload[:cut],fin=False)+frame(b'ping',opcode=9)+frame(payload[cut:],opcode=0))
        self.assertEqual(connection.read_message(),payload)
        self.assertEqual(raw.frames,[(10,b'ping')])

    def test_upgrade_refuses_status_accept_extensions_duplicates_and_bounds_privately(self):
        for mutate in (lambda h:h.replace(b'101',b'403',1),lambda h:h.replace(b'Sec-WebSocket-Accept:',b'Other:'),
                       lambda h:h.replace(b'\r\n\r\n',b'\r\nSec-WebSocket-Extensions: PRIVATE\r\n\r\n'),
                       lambda h:h.replace(b'Upgrade: websocket',b'Upgrade: websocket\r\nUpgrade: websocket'),
                       lambda _h:b'X'*16385,lambda _h:b''):
            raw=FakeServer(header_mutator=mutate)
            with self.assertRaises(WS['Refused']) as failure:WS['MessageConnection'](raw).upgrade()
            self.assertEqual(str(failure.exception),'ws-upgrade-refused')
            self.assertEqual(raw.frames,[])

    def test_malformed_frames_eof_and_declared_size_refuse_before_body_read(self):
        cases=[b'',b'\x81\x80',b'\xc1\x00',frame(b'',opcode=2),frame(b'',opcode=0),frame(b'',opcode=3),
               frame(b'\xff'),frame(b'first',fin=False)+frame(b'second'),frame(b'ping',opcode=9,fin=False),
               frame(b'x'*126,opcode=9),b'\x81\x7e\x00\x01x',b'\x81\x7f'+struct.pack('!Q',2**63),
               b'\x81\x7f'+struct.pack('!Q',WS['MAX_MESSAGE']+1),frame(b'',opcode=8)]
        for payload in cases:
            raw,connection=self.connected();raw.incoming.extend(payload)
            with self.subTest(payloadSize=len(payload)),self.assertRaises(WS['Refused']) as failure:connection.read_message()
            self.assertEqual(str(failure.exception),'ws-read-refused');self.assertTrue(connection.invalid)
            before=len(raw.frames)
            with self.assertRaises(WS['Refused']):connection.sendall(b'{}\n')
            self.assertEqual(len(raw.frames),before)

    def test_frame_wire_deadline_and_caller_limits_are_cumulative(self):
        for kind in ('frames','wire','deadline','message'):
            raw,connection=self.connected();raw.incoming.extend(frame(b'1234'))
            if kind=='frames':connection.frames=WS['MAX_FRAMES']
            if kind=='wire':connection.wire=WS['MAX_WIRE']
            if kind=='deadline':connection.deadline=0
            with self.assertRaises(WS['Refused']):connection.read_message(3 if kind=='message' else WS['MAX_MESSAGE'])
        raw,connection=self.connected();raw.incoming.extend(frame(b'',opcode=10)*129)
        with self.assertRaises(WS['Refused']):connection.read_message()

    def test_raw_jsonl_is_not_a_websocket_handshake(self):
        raw=FakeServer()
        with self.assertRaises(OSError):raw.sendall(b'{"id":0,"method":"initialize"}\n')
        self.assertFalse(raw.upgraded)


class InitializedTransportTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(prefix='p4-ws-fixture-');self.addCleanup(self.temp.cleanup)
        self.root=pathlib.Path(self.temp.name)
        shutil.copytree(REPO/'scripts/agent-dialogue-codex-schema',self.root/'bin/agent-dialogue-codex-schema')
        shutil.copyfile(REPO/'scripts/agent-dialogue-websocket.py',self.root/'bin/agent-dialogue-websocket.py')
        self.native=runpy.run_path(str(REPO/'scripts/agent-dialogue-native-source.py'))
        self.observation=runpy.run_path(str(REPO/'scripts/agent-dialogue-codex-observation.py'))
    def initial(self):return dict(codexHome=str(self.root/'codex-home'),platformFamily='unix',platformOs='linux',userAgent='PRIVATE_METADATA')
    def test_full_public_init_then_config_and_thread_read_use_framed_messages(self):
        import agent_dialogue_codex_observation_test as fixtures
        policy=runpy.run_path(str(REPO/'scripts/agent-dialogue-native-policy.py'))
        reader=policy['PolicyReader'](self.root,pathlib.Path('/owned/package'),REPO/'scripts/agent-dialogue-config-schema',self.observation)
        config={};origins={}
        for key,value in reader.expected.items():
            node=config;parts=key.split('.')
            for part in parts[:-1]:node=node.setdefault(part,{})
            node[parts[-1]]=value;origins[key]=dict(name=dict(type='user',file=str(self.root/'codex-home/config.toml')),version='PRIVATE_ORIGIN')
        config['unrelated']='PRIVATE_CONFIG'
        def respond(request):
            if request['method']=='initialize':return dict(id=0,result=self.initial())
            if request['method']=='config/read':return dict(id=request['id'],result=dict(config=config,origins=origins))
            if request['method']=='thread/read':return fixtures.response()
            self.assertEqual(request['method'],'initialized')
        raw=FakeServer(respond)
        connection=self.native['initialize'](raw,self.observation,self.root)
        facts=reader.read(connection);self.assertNotIn('PRIVATE_',json.dumps(facts))
        item_reader=fixtures.ObservationTests().reader()
        peer=dict(pid=4242,uid=os.getuid(),startTicks='42')
        with mock.patch.dict(self.observation['OwnedReadConnection'].__init__.__globals__,process_birth=lambda _:peer):
            item=self.observation['OwnedReadConnection'](connection,peer,item_reader).read(freeze=True)
        self.assertEqual(item['itemId'],'item-action')
        self.assertNotIn('PRIVATE_',json.dumps(item))
        self.assertEqual([json.loads(value)['method'] for opcode,value in raw.frames if opcode==1],['initialize','initialized','config/read','thread/read'])

    def test_missing_foreign_extra_or_malformed_init_refuses_before_initialized_and_closes(self):
        for change in ('missing','foreign','unknown','malformed','oversize'):
            value=self.initial()
            if change=='missing':del value['codexHome']
            if change=='foreign':value['codexHome']='/PRIVATE_FOREIGN'
            if change=='unknown':value['secret']='PRIVATE_SECRET'
            if change=='malformed':value['codexHome']=0
            if change=='oversize':value['userAgent']='x'*17000
            raw=FakeServer(lambda _:dict(id=0,result=value))
            with raw,self.assertRaises(ValueError) as failure:self.native['initialize'](raw,self.observation,self.root)
            self.assertTrue(raw.closed);self.assertNotIn('PRIVATE_',str(failure.exception))
            self.assertEqual([json.loads(payload)['method'] for opcode,payload in raw.frames if opcode==1],['initialize'])

    def test_initialization_schema_hash_change_refuses_before_handshake(self):
        path=self.root/'bin/agent-dialogue-codex-schema/InitializeResponse.json';path.write_bytes(path.read_bytes()+b' ')
        raw=FakeServer()
        with self.assertRaises(ValueError):self.native['initialize'](raw,self.observation,self.root)
        self.assertFalse(raw.upgraded)

    def test_initialize_closed_substages_keep_errors_notifications_and_wrong_ids_rejected(self):
        cases=[(dict(id=0,error=dict(code=-32600,message='PRIVATE_RPC',data='PRIVATE_DATA')),'envelope-error'),
               (dict(id=1,result=self.initial()),'envelope-id'),
               (dict(id=False,result=self.initial()),'envelope-id'),
               (dict(method='PRIVATE_METHOD',params={'secret':'PRIVATE_BODY'}),'envelope-notification'),
               (dict(id=0,result=self.initial(),unknown='PRIVATE_FIELD'),'envelope-shape')]
        for response,kind in cases:
            raw=FakeServer(lambda _:response)
            with self.subTest(kind=kind),raw,self.assertRaises(self.native['PolicyFailure']) as failure:
                self.native['initialize'](raw,self.observation,self.root)
            self.assertEqual((failure.exception.substage,failure.exception.kind),('initialize-envelope',kind))
            self.assertEqual(failure.exception.envelope_facts,self.observation['response_envelope_facts'](response,0))
            self.assertEqual(str(failure.exception),'policy-request');self.assertTrue(raw.closed)
            self.assertEqual(len(raw.frames),1)
            self.assertNotIn('PRIVATE_',json.dumps(vars(failure.exception)))

    def test_initialize_transport_decode_and_notification_write_boundaries(self):
        for case,stage,kind in [('http','upgrade','http-status'),('eof','initialize-read','eof'),
                ('timeout','initialize-read','deadline'),('frame','initialize-read','frame'),
                ('decode','initialize-decode','unknown'),('write','initialize-write','io'),
                ('initialized','initialized-write','io')]:
            def respond(request):
                if request['method']=='initialized':
                    if case=='initialized':raise OSError('PRIVATE_IO')
                    return None
                if case=='write':raise OSError('PRIVATE_IO')
                if case=='eof':return None
                if case=='decode':raw.incoming.extend(frame(b'PRIVATE_JSON'));return None
                if case=='frame':raw.incoming.extend(frame(b'PRIVATE_BINARY',opcode=2));return None
                return dict(id=0,result=self.initial())
            raw=FakeServer(respond,header_mutator=(lambda h:h.replace(b'101',b'403',1)) if case=='http' else lambda h:h)
            receive=raw.recv
            def recv(size):
                if case=='timeout' and raw.frames:raise TimeoutError('PRIVATE_TIMEOUT')
                return receive(size)
            raw.recv=recv
            with self.subTest(case=case),raw,self.assertRaises(self.native['PolicyFailure']) as failure:
                self.native['initialize'](raw,self.observation,self.root)
            self.assertEqual((failure.exception.substage,failure.exception.kind),(stage,kind))
            self.assertNotIn('PRIVATE_',json.dumps(vars(failure.exception)));self.assertTrue(raw.closed)
