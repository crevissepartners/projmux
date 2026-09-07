#!/usr/bin/env python3
"""Bounded RFC6455 client on an already-owned Unix socket; no dial/listener.

Raw frames and HTTP headers stay in memory. Diagnostics are closed labels.
"""
import base64
import hashlib
import os
import struct
import time

MAX_MESSAGE=1024*1024
MAX_WIRE=8*MAX_MESSAGE
MAX_FRAMES=128
GUID=b'258EAFA5-E914-47DA-95CA-C5AB0DC85B11'


class Refused(ValueError):
    pass


def require(value,code):
    if not value:raise Refused(code)


class MessageConnection:
    def __init__(self,raw,*,clock=time.monotonic):
        self.raw=raw;self.clock=clock;self.deadline=clock()+5
        self.wire=0;self.frames=0;self.invalid=False

    @property
    def family(self):return self.raw.family

    def getsockopt(self,*args):return self.raw.getsockopt(*args)

    def settimeout(self,seconds):
        require(0<seconds<=30,'ws-deadline')
        self.deadline=self.clock()+seconds

    def _remaining(self):
        remaining=self.deadline-self.clock()
        require(not self.invalid and remaining>0,'ws-deadline')
        self.raw.settimeout(remaining)

    def _exact(self,count):
        require(0<=count<=MAX_MESSAGE and self.wire+count<=MAX_WIRE,'ws-wire-bound')
        result=bytearray()
        while len(result)<count:
            self._remaining()
            chunk=self.raw.recv(min(65536,count-len(result)))
            require(chunk and len(chunk)<=count-len(result),'ws-eof')
            result.extend(chunk);self.wire+=len(chunk)
        return bytes(result)

    def _write(self,value):
        require(self.wire+len(value)<=MAX_WIRE,'ws-wire-bound')
        self._remaining();self.raw.sendall(value);self.wire+=len(value)

    def _frame(self,opcode,payload):
        require(len(payload)<=MAX_MESSAGE and self.frames<MAX_FRAMES,'ws-frame-bound')
        self.frames+=1
        size=len(payload)
        header=bytes((0x80|opcode,0x80|size)) if size<126 else bytes((0x80|opcode,0xfe))+struct.pack('!H',size) if size<=65535 else bytes((0x80|opcode,0xff))+struct.pack('!Q',size)
        mask=os.urandom(4)
        self._write(header+mask+bytes(value^mask[i%4] for i,value in enumerate(payload)))

    def upgrade(self):
        try:
            key=base64.b64encode(os.urandom(16))
            self._write(b'GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: '+key+b'\r\nSec-WebSocket-Version: 13\r\n\r\n')
            header=bytearray()
            while not header.endswith(b'\r\n\r\n'):
                require(len(header)<16384,'ws-upgrade-bound')
                header.extend(self._exact(1))
            lines=bytes(header).split(b'\r\n')
            status=lines[0].split(b' ',2)
            require(len(status)>=2 and status[:2]==[b'HTTP/1.1',b'101'],'ws-upgrade-status')
            fields={}
            for line in lines[1:-2]:
                name,sep,value=line.partition(b':')
                require(sep and name and all(c in b"!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" for c in name),'ws-upgrade-header')
                require(all(c==9 or 32<=c<127 for c in value),'ws-upgrade-header')
                name=name.lower();require(name not in fields,'ws-upgrade-duplicate')
                fields[name]=value.strip()
            # RFC6455 checksum, not a credential or authority hash.
            accept=base64.b64encode(hashlib.sha1(key+GUID).digest())
            require(fields.get(b'upgrade',b'').lower()==b'websocket' and b'upgrade' in [x.strip().lower() for x in fields.get(b'connection',b'').split(b',')] and fields.get(b'sec-websocket-accept')==accept,'ws-upgrade-accept')
            require(not any(key in fields for key in (b'sec-websocket-extensions',b'sec-websocket-protocol',b'transfer-encoding')) and fields.get(b'content-length',b'0')==b'0','ws-upgrade-extension')
            return self
        except Exception:
            self.invalid=True
            raise Refused('ws-upgrade-refused') from None

    def sendall(self,value):
        try:
            require(isinstance(value,bytes) and value.endswith(b'\n'),'ws-json-message')
            self._frame(1,value[:-1])
        except Exception:
            self.invalid=True
            raise Refused('ws-write-refused') from None

    def read_message(self,limit=MAX_MESSAGE):
        try:
            require(0<limit<=MAX_MESSAGE,'ws-message-limit')
            complete=bytearray();started=False
            while True:
                require(self.frames<MAX_FRAMES,'ws-frame-bound');self.frames+=1
                first,second=self._exact(2);fin=bool(first&0x80);opcode=first&15
                require(not first&0x70 and not second&0x80,'ws-frame-flags')
                size=second&127
                if size==126:
                    size=struct.unpack('!H',self._exact(2))[0];require(size>=126,'ws-length')
                elif size==127:
                    size=struct.unpack('!Q',self._exact(8))[0];require(65535<size<2**63,'ws-length')
                if opcode in (8,9,10):
                    require(fin and size<=125,'ws-control')
                    payload=self._exact(size)
                    if opcode==8:raise Refused('ws-closed')
                    if opcode==9:self._frame(10,payload)
                    continue
                require(opcode in (0,1) and ((opcode==1 and not started) or (opcode==0 and started)),'ws-opcode')
                require(len(complete)+size<=limit,'ws-message-bound')
                complete.extend(self._exact(size));started=True
                if fin:
                    complete.decode('utf-8')
                    return bytes(complete)
        except Exception:
            self.invalid=True
            raise Refused('ws-read-refused') from None
