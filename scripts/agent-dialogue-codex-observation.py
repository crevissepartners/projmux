#!/usr/bin/env python3
"""Review-only bounded projection of official Codex 0.153.2 observations.

No provider launcher, authentication, lifecycle mutation, input writer, or
filesystem transcript reader is present. Transport receives an already-owned,
already-initialized socket; only thread/read(includeTurns=true) is sent.
Raw JSON and command/output/reasoning stay in memory and never enter diagnostics.
"""
import copy
import hashlib
import json
import math
import os
import pathlib
import re
import socket
import struct
import time

MAX_FRAME = 1024 * 1024
MAX_TOTAL = 8 * MAX_FRAME
MAX_FRAMES = 128
MAX_NODES = 16384
MAX_DEPTH = 32
ID = re.compile(r"[A-Za-z0-9._:-]{1,160}\Z")
SOURCES = frozenset(("agent", "userShell", "unifiedExecStartup", "unifiedExecInteraction"))
SCHEMA_HASHES = {
    "JSONRPCRequest.json": "31bd6f360b2dd8a7ceaf682708105d40d38cb0b9d0821357a04da67028438f73",
    "JSONRPCResponse.json": "4796738c04c74288213a08fb8d820c7b4df19e0977cdcd35b65ffcb43cfc93ab",
    "JSONRPCNotification.json": "c2b43f26880db331393fe09f34bdd76dfeb4dc30d8418d1c26e18db166d6c8c8",
    "JSONRPCError.json": "d7ea353d4875ae204625da5a00a1ecb5afc69101d5778b9acc253a49f8932992",
    "InitializeParams.json": "6f0094be9a65242ec779a40794cbd4fdfa32fca1e45084a16adfb50501d33ea2",
    "InitializeResponse.json": "62ad689c2cb6379913c1d72749cfd8de5089d35760214123518eb92eef11acc9",
    "ThreadReadParams.json": "dfe040c6ac71d30795b8be3f3ff232e66f362a37f883b491e5d1ea367f470db4",
    "ThreadReadResponse.json": "a76583d07f6096fee33045da2dc9caed84d858f8f2d39b37bb38528dbaf32511",
    "ItemStartedNotification.json": "c4c34f47db6326cd4841bae428f23d08eb285077ffad35be9772b928c65bb912",
    "ItemCompletedNotification.json": "69aba3fe5f72f38bf5c541e7e2c09de40778abe65ff969d9fc73372037812091",
}


class Refused(ValueError):
    """Exception text is always an internal closed label, never upstream text."""


def require(value, reason):
    if not value:
        raise Refused(reason)


def identifier(value):
    require(isinstance(value, str) and ID.fullmatch(value), "identifier")
    return value


def object_pairs(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate-key")
        result[key] = value
    return result


def bound_tree(value):
    pending = [(value, 0)]
    count = 0
    while pending:
        current, depth = pending.pop()
        count += 1
        require(count <= MAX_NODES and depth <= MAX_DEPTH, "structure-bound")
        if isinstance(current, dict):
            require(len(current) <= 128, "object-bound")
            pending.extend((v, depth + 1) for v in current.values())
        elif isinstance(current, list):
            require(len(current) <= 256, "array-bound")
            pending.extend((v, depth + 1) for v in current)
        elif isinstance(current, str):
            require(len(current.encode()) <= MAX_FRAME, "string-bound")
        elif isinstance(current, float):
            require(math.isfinite(current), "nonfinite-number")


def decode(raw):
    require(isinstance(raw, bytes) and len(raw) <= MAX_FRAME, "frame-bound")
    try:
        value = json.loads(raw, object_pairs_hook=object_pairs)
    except (UnicodeError, ValueError, RecursionError):
        raise Refused("json-shape") from None
    bound_tree(value)
    return value


def response_rejection_kind(value, request_id):
    """Pinned public envelope forms; only the exact success reply succeeds.

    Error text/data, request methods/params/trace and all unknown keys are never
    returned. The expected-ID fence precedes classification of server requests.
    """
    if not isinstance(value,dict):return 'envelope-shape'
    if 'id' in value and (type(value['id']) is not int or value['id']!=request_id):return 'envelope-id'
    if set(value)=={'id','result'}:return None
    if set(value)=={'id','error'}:
        error=value['error']
        if isinstance(error,dict) and {'code','message'}<=set(error)<={'code','message','data'} and \
                type(error['code']) is int and -(2**63)<=error['code']<2**63 and isinstance(error['message'],str):
            return 'envelope-error'
        return 'envelope-shape'
    if isinstance(value.get('method'),str) and value['method']:
        if set(value) in ({'method'},{'method','params'}):return 'envelope-notification'
        if {'id','method'}<=set(value)<={'id','method','params','trace'}:
            trace=value.get('trace')
            if trace is None or isinstance(trace,dict) and set(trace)<={'traceparent','tracestate'} and \
                    all(v is None or isinstance(v,str) for v in trace.values()):
                return 'envelope-request'
    return 'envelope-shape'


def response_envelope_facts(value, request_id):
    """Fixed structural predicates only; never authority or upstream values.

    Observe every remaining shape branch together, including simultaneous
    defects. Unknown member names and arbitrary result/params/error data are
    not copied. The admission classifier above remains independent.
    """
    top = ('null' if value is None else 'boolean' if type(value) is bool else
           'integer' if type(value) is int else 'number' if type(value) is float else
           'string' if isinstance(value, str) else 'array' if isinstance(value, list) else
           'object' if isinstance(value, dict) else 'unknown')
    obj = value if isinstance(value, dict) else {}
    error = obj.get('error')
    error_obj = error if isinstance(error, dict) else {}
    trace = obj.get('trace')
    trace_obj = trace if isinstance(trace, dict) else {}
    code = error_obj.get('code')
    return {
        'topLevel': top,
        'id': ('absent' if 'id' not in obj else 'other-type' if type(obj['id']) is not int else
               'expected-integer' if obj['id'] == request_id else 'other-integer'),
        'unknownFields': bool(set(obj) - {'id', 'result', 'error', 'method', 'params', 'trace'}),
        'resultPresent': 'result' in obj,
        'error': 'absent' if 'error' not in obj else 'object' if isinstance(error, dict) else 'other-type',
        'errorUnknownFields': bool(set(error_obj) - {'code', 'message', 'data'}),
        'errorCode': ('absent' if 'code' not in error_obj else 'other-type' if type(code) is not int else
                      'int64' if -(2**63) <= code < 2**63 else 'integer-out-of-range'),
        'errorMessage': ('absent' if 'message' not in error_obj else
                         'string' if isinstance(error_obj['message'], str) else 'other-type'),
        'method': ('absent' if 'method' not in obj else 'other-type' if not isinstance(obj['method'], str) else
                   'nonempty-string' if obj['method'] else 'empty-string'),
        'paramsPresent': 'params' in obj,
        'trace': ('absent' if 'trace' not in obj else 'null' if trace is None else
                  'object' if isinstance(trace, dict) else 'other-type'),
        'traceUnknownFields': bool(set(trace_obj) - {'traceparent', 'tracestate'}),
        'traceValuesValid': isinstance(trace, dict) and all(v is None or isinstance(v, str) for v in trace.values()),
    }


def close_schema(value):
    if isinstance(value, dict):
        if "properties" in value and "additionalProperties" not in value:
            # The frozen public agentMessage schema requires id but omits its
            # property declaration. Required names are explicitly known keys;
            # item identifiers are additionally type/bound checked below.
            for name in value.get("required", []):
                value["properties"].setdefault(name, {})
            value["additionalProperties"] = False
        for item in value.values():
            close_schema(item)
    elif isinstance(value, list):
        for item in value:
            close_schema(item)


def schema_valid(schema, value, root):
    """The frozen export subset, with closed object names and no dependencies.

    This is a canary validator, not a general JSON Schema implementation. The
    exported formats are metadata; authority and identifiers are checked below.
    """
    if isinstance(schema, bool):
        return schema
    if "$ref" in schema:
        reference = schema["$ref"].split("/")
        if len(reference) != 3 or reference[:2] != ["#", "definitions"]:
            return False
        return schema_valid(root["definitions"][reference[2]], value, root)
    for union, required in (("allOf", "all"), ("anyOf", "any"), ("oneOf", "one")):
        if union in schema:
            matches = [schema_valid(part, value, root) for part in schema[union]]
            if (required == "all" and not all(matches)) or (required == "any" and not any(matches)) or (required == "one" and sum(matches) != 1):
                return False
    if "enum" in schema and not any(type(value) is type(item) and value == item for item in schema["enum"]):
        return False
    types = schema.get("type", [])
    if isinstance(types, str):
        types = [types]
    actual = ("null" if value is None else "boolean" if type(value) is bool else
              "integer" if type(value) is int else "number" if type(value) is float else
              "string" if isinstance(value, str) else "array" if isinstance(value, list) else
              "object" if isinstance(value, dict) else "invalid")
    if types and actual not in types and not (actual == "integer" and "number" in types):
        return False
    if isinstance(value, dict):
        properties = schema.get("properties", {})
        if not set(schema.get("required", [])) <= set(value):
            return False
        additional = schema.get("additionalProperties", not bool(properties))
        for key, item in value.items():
            if not schema_valid(properties.get(key, additional), item, root):
                return False
    elif isinstance(value, list):
        if not all(schema_valid(schema.get("items", True), item, root) for item in value):
            return False
    elif type(value) in (int, float):
        if ("minimum" in schema and value < schema["minimum"]) or ("maximum" in schema and value > schema["maximum"]):
            return False
    return True


class Schemas:
    def __init__(self, root):
        self.validators = {}
        for name, digest in SCHEMA_HASHES.items():
            path = pathlib.Path(root) / name
            require(not path.is_symlink(), "schema-link")
            raw = path.read_bytes()
            require(len(raw) <= 2 * MAX_FRAME and hashlib.sha256(raw).hexdigest() == digest, "schema-digest")
            schema = json.loads(raw)
            close_schema(schema)
            self.validators[name] = schema

    def validate(self, name, value):
        bound_tree(value)
        schema = self.validators[name]
        require(schema_valid(schema, value, schema), "public-schema")


def validate_result(value, expected):
    """The source action's sole stdout is this closed, body-free result."""
    require(isinstance(value, dict) and set(value) == {
        "version", "sourceAgentUID", "targetAgentUID", "qualification", "idle"
    }, "action-result-shape")
    require(type(value["version"]) is int and value["version"] == 1, "action-result-version")
    identifier(value["sourceAgentUID"])
    identifier(value["targetAgentUID"])
    for phase in ("qualification", "idle"):
        row = value[phase]
        require(isinstance(row, dict) and set(row) == {
            "originalRef", "replyRef", "conversationRef", "selfClaimed", "guardedCommitMatched"
        }, "action-result-correlation")
        for key in ("originalRef", "replyRef", "conversationRef"):
            identifier(row[key])
        require(row["originalRef"] != row["replyRef"], "action-result-ref")
        require(row["selfClaimed"] is True and row["guardedCommitMatched"] is True, "action-result-incomplete")
    require(value["qualification"]["originalRef"] != value["idle"]["originalRef"] and
            value["qualification"]["conversationRef"] != value["idle"]["conversationRef"], "action-result-phases")
    require(value == expected, "action-result-mismatch")


class ObservationReader:
    """Freeze a live started item before release; complete it only after action.

    allowed_sources has no default. The packet supplies a reviewed explicit
    allowlist; even a documented schema default never fills missing source.
    processId is schema-validated metadata only and is never emitted or parsed
    as an OS PID. OS ancestry must come from the independent owned launch handle.
    """
    def __init__(self, schemas, thread_id, turn_id, command, cwd, allowed_sources):
        self.schemas = schemas
        self.thread_id, self.turn_id = identifier(thread_id), identifier(turn_id)
        require(isinstance(command, str) and 0 < len(command.encode()) <= 4096, "expected-command")
        require(isinstance(cwd, str) and os.path.isabs(cwd), "expected-cwd")
        require(isinstance(allowed_sources, frozenset) and allowed_sources and
                allowed_sources <= SOURCES and "userShell" not in allowed_sources, "source-policy")
        self.command, self.cwd, self.allowed_sources = command, cwd, allowed_sources
        self.frozen = None
        self.started_at = None
        self.completed_at = None
        self.expected_result = None
        self.completed = False
        self.turn_completed = False
        self.invalid = False
        self.frames = 0
        self.total = 0

    def request(self, request_id):
        require(not self.invalid and type(request_id) is int and 0 < request_id <= MAX_FRAMES, "request-id")
        params = {"threadId": self.thread_id, "includeTurns": True}
        self.schemas.validate("ThreadReadParams.json", params)
        return {"id": request_id, "method": "thread/read", "params": params}

    def _command(self, item, *, freeze=False):
        require(item["type"] == "commandExecution", "unexpected-effect")
        require("source" in item, "source-unobserved")
        require(item["source"] in self.allowed_sources, "source-policy")
        require(item["command"] == self.command and item["cwd"] == self.cwd, "action-selection")
        require(item.get("pluginId") is None and item.get("scriptPath") is None, "plugin-action")
        item_id = identifier(item["id"])
        if freeze:
            require(self.frozen is None and item["status"] == "inProgress", "freeze-state")
            require(item.get("exitCode") is None and item.get("aggregatedOutput") in (None, ""), "pre-release-output")
            self.frozen = {"threadId": self.thread_id, "turnId": self.turn_id,
                           "itemId": item_id, "source": item["source"], "status": "inProgress",
                           "commandMatched": True, "cwdMatched": True}
        else:
            require(self.frozen is not None and item_id == self.frozen["itemId"] and
                    item["source"] == self.frozen["source"], "item-mismatch")
        status = item["status"]
        require(status in ("inProgress", "completed"), "action-failed")
        if status == "completed":
            require(self.expected_result is not None, "completion-before-release")
            require(type(item.get("exitCode")) is int and item["exitCode"] == 0, "action-exit")
            require(isinstance(item.get("aggregatedOutput"), str), "action-result-missing")
            raw = item["aggregatedOutput"].encode()
            require(len(raw) <= 4096, "action-result-bound")
            validate_result(decode(raw), self.expected_result)
            self.completed = True
        else:
            require(not self.completed and item.get("exitCode") is None and
                    item.get("aggregatedOutput") in (None, ""), "action-state-regression")

    def set_expected_result(self, result):
        require(self.frozen is not None and not self.invalid and self.expected_result is None, "result-phase")
        validate_result(result, result)
        self.expected_result = copy.deepcopy(result)

    def _snapshot(self, result, freeze):
        self.schemas.validate("ThreadReadResponse.json", result)
        thread = result["thread"]
        require(thread["id"] == self.thread_id and thread["cwd"] == self.cwd and
                thread["cliVersion"] == "0.153.2", "thread-mismatch")
        require(thread.get("parentThreadId") is None and thread.get("forkedFromId") is None, "derived-thread")
        require(len(thread["turns"]) == 1, "turn-count")
        turn = thread["turns"][0]
        require(turn["id"] == self.turn_id and turn["status"] in ("inProgress", "completed"), "turn-mismatch")
        # Absence is not defaulted to full: otherwise a paginated/summary read
        # could hide another effect. Actual availability is unverified.
        require(turn.get("itemsView") == "full", "history-view-unproven")
        require(len(turn["items"]) <= 32, "item-count")
        commands = []
        ids = set()
        users = 0
        for item in turn["items"]:
            key = identifier(item["id"])
            require(key not in ids, "duplicate-item")
            ids.add(key)
            kind = item["type"]
            if kind == "commandExecution":
                commands.append(item)
            elif kind == "userMessage":
                users += 1
                require(all(c.get("type") == "text" for c in item["content"]), "unexpected-input")
            else:
                require(kind in ("agentMessage", "reasoning", "plan"), "unexpected-effect")
        require(users == 1 and len(commands) == 1, "action-count")
        self._command(commands[0], freeze=freeze)
        if turn["status"] == "completed":
            require(self.completed and turn.get("error") is None, "turn-completion")
            self.turn_completed = True

    def _notification(self, frame, freeze):
        method = frame["method"]
        require(method in ("item/started", "item/completed"), "unknown-notification")
        params = frame["params"]
        self.schemas.validate("ItemStartedNotification.json" if method == "item/started" else "ItemCompletedNotification.json", params)
        require(params["threadId"] == self.thread_id and params["turnId"] == self.turn_id, "notification-route")
        require(params["item"]["type"] == "commandExecution", "unexpected-effect")
        if method == "item/started":
            require(self.started_at is None and params["item"]["status"] == "inProgress", "started-order")
            self._command(params["item"], freeze=freeze)
            self.started_at = params["startedAtMs"]
        else:
            require(not freeze and self.completed_at is None and params["item"]["status"] == "completed", "completed-order")
            self._command(params["item"])
            self.completed_at = params["completedAtMs"]
            require(self.started_at is None or self.completed_at >= self.started_at, "notification-time")

    def accept(self, raw, *, request_id=None, freeze=False):
        try:
            require(not self.invalid, "reader-invalid")
            self.frames += 1
            self.total += len(raw)
            require(self.frames <= MAX_FRAMES and self.total <= MAX_TOTAL, "stream-bound")
            frame = decode(raw)
            require(isinstance(frame, dict), "envelope-shape")
            if "method" in frame:
                require(set(frame) == {"method", "params"}, "notification-envelope")
                self._notification(frame, freeze)
            else:
                require(set(frame) == {"id", "result"} and type(frame["id"]) is int and
                        frame["id"] == request_id, "response-envelope")
                self._snapshot(frame["result"], freeze)
            return self.facts()
        except Refused:
            self.invalid = True
            raise

    def facts(self):
        require(not self.invalid and self.frozen is not None, "reader-not-frozen")
        return {**self.frozen, "toolCompleted": self.completed,
                "closedResultMatched": self.completed, "turnCompleted": self.turn_completed,
                "sourceDefaultApplied": False, "processIdUsedAsOSPID": False}


def process_birth(pid):
    require(type(pid) is int and pid > 1, "peer-pid")
    try:
        root = pathlib.Path("/proc") / str(pid)
        fields = (root / "stat").read_text().rsplit(")", 1)[1].split()
        require(fields[0] not in ("Z", "X"), "peer-exited")
        uid = int(next(x.split()[1] for x in (root / "status").read_text().splitlines() if x.startswith("Uid:")))
        return {"pid": pid, "uid": uid, "startTicks": fields[19]}
    except (OSError, IndexError, StopIteration, ValueError):
        raise Refused("peer-unavailable") from None


class OwnedReadConnection:
    """Existing initialized UDS only; no start/resume/subscribe/control writes.

    Parent owns initialization and connection establishment with the reviewed
    endpoint. This connection owns no daemon lifecycle. Caller must also pin
    socket inode and launch executable before passing it in; kernel peer birth
    is rechecked before and after each read. EOF/unknown frames invalidate it.
    """
    def __init__(self, connection, peer, reader, *, deadline_seconds=10, clock=time.monotonic):
        require(0 < deadline_seconds <= 30 and connection.family == socket.AF_UNIX, "connection-policy")
        require(set(peer) == {"pid", "uid", "startTicks"}, "peer-shape")
        self.socket, self.peer, self.reader = connection, dict(peer), reader
        self.clock, self.deadline = clock, clock() + deadline_seconds
        self.next_id = 1
        self._peer()

    def _peer(self):
        pid, uid, _ = struct.unpack("3i", self.socket.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i")))
        require(pid == self.peer["pid"] and uid == self.peer["uid"] == os.getuid() and
                process_birth(pid) == self.peer, "peer-changed")

    def _message(self):
        remaining=self.deadline-self.clock()
        require(remaining>0,'observation-timeout')
        self.socket.settimeout(remaining)
        return self.socket.read_message(MAX_FRAME)

    def read(self, *, freeze=False):
        try:
            self._peer()
            require(self.clock() < self.deadline, "observation-timeout")
            request_id = self.next_id
            self.next_id += 1
            request = self.reader.request(request_id)
            self.socket.settimeout(self.deadline - self.clock())
            self.socket.sendall(json.dumps(request, separators=(",", ":")).encode() + b"\n")
            while True:
                raw = self._message()
                frame = decode(raw)
                facts = self.reader.accept(raw, request_id=request_id, freeze=freeze and self.reader.frozen is None)
                if "id" in frame:
                    self._peer()
                    return facts
        except (ValueError, OSError):
            self.reader.invalid = True
            raise Refused("observation-refused") from None


if __name__ == "__main__":
    # Deliberately no standalone live command. The reviewed parent harness must
    # own endpoint preparation, initialized connection, pins and once-cleanup.
    raise SystemExit("offline module only; no live entrypoint")
