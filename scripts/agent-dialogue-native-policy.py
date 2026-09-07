#!/usr/bin/env python3
"""Bounded config/read policy projection for an already-owned initialized peer.

No layers, instructions, secret values or arbitrary config keys are returned.
This does not start a dummy thread or claim to observe ThreadStartResponse.
"""
import functools
import hashlib
import json
import pathlib


HASHES={
    'ConfigReadParams.json':'257c54a423b47c1d209ff1076765a1564d82322fd5161670fd489a2874de1bac',
    'ConfigReadResponse.json':'bd72c94e2c7d49ead6a20bcf54afedc8db11044bf8cadb387e42135dd5d1e342',
}


FAILURE_CODES=frozenset(('policy-schema','policy-request','policy-value','policy-origin','policy-config','policy-socket'))


class Refused(ValueError):
    def __init__(self,reason,code='policy-schema'):
        if code not in FAILURE_CODES: raise ValueError('policy failure code')
        super().__init__(reason)
        self.code=code


def classified(code):
    """Retain a closed boundary, never an exception's arbitrary text."""
    def decorate(function):
        @functools.wraps(function)
        def call(*args,**kwargs):
            try: return function(*args,**kwargs)
            except Refused: raise
            except Exception: raise Refused('policy-refused',code) from None
        return call
    return decorate


def require(condition,reason,code='policy-schema'):
    if not condition:raise Refused(reason,code)


class PolicyReader:
    @classified('policy-schema')
    def __init__(self,root,package_root,schemas,observation):
        self.root,self.package_root=pathlib.Path(root),pathlib.Path(package_root)
        self.observation=observation
        self.schemas={}
        for name,digest in HASHES.items():
            path=pathlib.Path(schemas)/name
            require(not path.is_symlink(),'policy-schema-link')
            raw=path.read_bytes()
            require(len(raw)<=1024*1024 and hashlib.sha256(raw).hexdigest()==digest,'policy-schema-hash')
            schema=json.loads(raw)
            observation['close_schema'](schema)
            self.schemas[name]=schema
        self.expected={
            'approval_policy':'never','sandbox_mode':'workspace-write','web_search':'disabled',
            'cli_auth_credentials_store':'file','check_for_update_on_startup':False,
            'sandbox_workspace_write.network_access':False,
            'sandbox_workspace_write.exclude_slash_tmp':True,
            'sandbox_workspace_write.exclude_tmpdir_env_var':True,
            'sandbox_workspace_write.writable_roots':[str(self.root)],
        }

    @classified('policy-schema')
    def validate(self,name,value):
        schema=self.schemas[name]
        require(self.observation['schema_valid'](schema,value,schema),'policy-public-schema')

    @classified('policy-schema')
    def project(self,value):
        self.validate('ConfigReadResponse.json',value)
        require(value.get('layers') in (None,[]),'policy-layers-not-requested','policy-value')
        config,origins=value['config'],value['origins']
        require(isinstance(config,dict) and isinstance(origins,dict) and len(origins)<=128,'policy-config-bound','policy-value')
        # Do not silently accept foreign project/managed/session inputs just
        # because selected scalar policy values happen to match.
        origin_counts={'user':0,'packagedDefaults':0}
        for origin in origins.values():
            source=origin['name'];kind=source['type']
            require(kind in origin_counts,'policy-foreign-origin','policy-origin')
            require(isinstance(origin['version'],str) and 0<len(origin['version'])<=256,'policy-origin-version','policy-origin')
            if kind=='user':
                require(source['file']==str(self.root/'codex-home/config.toml') and source.get('profile') is None,'policy-user-origin','policy-origin')
            else:
                path=pathlib.Path(source['file'])
                require(path.is_absolute() and self.package_root in path.parents and '..' not in path.parts,'policy-package-origin','policy-origin')
            origin_counts[kind]+=1
        projected={}
        for key,expected in self.expected.items():
            current=config
            for part in key.split('.'):
                require(isinstance(current,dict) and part in current,'policy-value-missing','policy-value')
                current=current[part]
            require(type(current) is type(expected) and current==expected,'policy-value-mismatch','policy-value')
            # The reviewed representation uses explicit dotted leaf origins.
            # Missing/alternative map representations are unverified, not
            # inferred from a parent key or silently replaced by a default.
            require(key in origins and origins[key]['name']['type']=='user','policy-origin-missing','policy-origin')
            projected[key]=expected
        for key in ('mcp_servers','plugins','hooks','instructions','developer_instructions'):
            require(config.get(key) in (None,{},[],''),'policy-extra-execution-input','policy-value')
        return dict(version=1,values=projected,origins={key:'owned-user-config' for key in self.expected},
                    originCounts=origin_counts,layersRequested=False,rawValuesRetained=False,
                    cwdMatched=True,threadPolicyObserved=False)

    @classified('policy-schema')
    def decode(self,line):
        return self.observation['decode'](line)

    @classified('policy-request')
    def read(self,connection):
        params=dict(cwd=str(self.root/'work'),includeLayers=False)
        self.validate('ConfigReadParams.json',params)
        connection.settimeout(5)
        connection.sendall(json.dumps(dict(id=1,method='config/read',params=params),separators=(',',':')).encode()+b'\n')
        value=self.decode(connection.read_message(1024*1024))
        require(isinstance(value,dict) and set(value)=={'id','result'} and type(value['id']) is int and value['id']==1,'policy-response-envelope','policy-request')
        return self.project(value['result'])
