#!/usr/bin/env python3
"""Pure memory/fake-connection regressions; no socket/provider/auth/actor launch."""
import copy
import importlib.util
import json
import pathlib
import struct
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("source_observation", str(pathlib.Path(__file__).resolve().parents[1] / "scripts/agent-dialogue-codex-observation.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
ROOT = pathlib.Path(__file__).resolve().parents[1] / "scripts/agent-dialogue-codex-schema"
SENTINEL = "RAW_PRIVATE_CONTENT_MUST_NOT_SURVIVE"
COMMAND = "/usr/bin/python3 /owned/bin/source-action.py"
CWD = "/owned/work"


def result():
    return {"version": 1, "sourceAgentUID": "agent-source", "targetAgentUID": "agent-target",
            "qualification": {"originalRef": "qualification-original", "replyRef": "qualification-reply",
                              "conversationRef": "qualification-conversation", "selfClaimed": True,
                              "guardedCommitMatched": True},
            "idle": {"originalRef": "idle-original", "replyRef": "idle-reply",
                     "conversationRef": "idle-conversation", "selfClaimed": True, "guardedCommitMatched": True}}


def command():
    return {"type": "commandExecution", "id": "item-action", "command": COMMAND,
            "commandActions": [{"type": "unknown", "command": COMMAND}], "cwd": CWD,
            "source": "agent", "status": "inProgress", "processId": "not-an-os-pid"}


def response():
    return {"id": 1, "result": {"thread": {
        "cliVersion": "0.153.2", "createdAt": 1, "cwd": CWD, "ephemeral": False,
        "id": "thread-source", "modelProvider": "openai", "preview": SENTINEL,
        "projectId": None, "sessionId": "session-source", "source": "appServer",
        "status": {"type": "active", "activeFlags": []}, "updatedAt": 2,
        "turns": [{"id": "turn-source", "status": "inProgress", "itemsView": "full", "items": [
            {"type": "userMessage", "id": "item-user", "content": [{"type": "text", "text": SENTINEL}]},
            {"type": "reasoning", "id": "item-reasoning", "summary": [SENTINEL], "content": [SENTINEL]},
            command()
        ]}]
    }}}


def notification(method, item, timestamp=100):
    field = "startedAtMs" if method == "item/started" else "completedAtMs"
    return {"method": method, "params": {"threadId": "thread-source", "turnId": "turn-source",
                                       field: timestamp, "item": item}}


def complete(value):
    value = copy.deepcopy(value)
    thread = value["result"]["thread"]
    turn = thread["turns"][0]
    thread["status"] = {"type": "idle"}
    turn["status"] = "completed"
    turn["items"][-1].update(status="completed", exitCode=0, aggregatedOutput=json.dumps(result()))
    return value


class ObservationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.schemas = m.Schemas(ROOT)

    def reader(self, sources=frozenset(("agent",))):
        return m.ObservationReader(self.schemas, "thread-source", "turn-source", COMMAND, CWD, sources)

    def accept(self, reader, frame, **kwargs):
        return reader.accept(json.dumps(frame).encode(), request_id=1, **kwargs)

    def assert_rejected(self, mutation, reason=None):
        reader = self.reader()
        value = response()
        mutation(value)
        with self.assertRaises(m.Refused) as caught:
            self.accept(reader, value, freeze=True)
        self.assertNotIn(SENTINEL, str(caught.exception))
        if reason is not None:
            self.assertEqual(str(caught.exception), reason)
        self.assertTrue(reader.invalid)

    def test_schema_hashes_and_read_only_request(self):
        reader = self.reader()
        self.assertEqual(reader.request(1), {"id": 1, "method": "thread/read",
                                           "params": {"threadId": "thread-source", "includeTurns": True}})
        with mock.patch.object(m, "SCHEMA_HASHES", {"ThreadReadParams.json": "0" * 64}):
            with self.assertRaisesRegex(m.Refused, "schema-digest"):
                m.Schemas(ROOT)

    def test_pre_release_freeze_then_recorded_return_without_circularity(self):
        reader = self.reader()
        facts = self.accept(reader, response(), freeze=True)
        self.assertFalse(facts["toolCompleted"])
        self.assertFalse(facts["sourceDefaultApplied"])
        self.assertFalse(facts["processIdUsedAsOSPID"])
        reader.set_expected_result(result())
        facts = self.accept(reader, complete(response()))
        self.assertTrue(facts["toolCompleted"] and facts["closedResultMatched"] and facts["turnCompleted"])
        for forbidden in (SENTINEL, COMMAND, CWD, "not-an-os-pid", "aggregatedOutput", "processId\""):
            self.assertNotIn(forbidden, json.dumps(facts))

    def test_notification_started_completed_pair_and_final_snapshot(self):
        reader = self.reader()
        self.accept(reader, notification("item/started", command()), freeze=True)
        reader.set_expected_result(result())
        item = command()
        item.update(status="completed", exitCode=0, aggregatedOutput=json.dumps(result()))
        facts = self.accept(reader, notification("item/completed", item, 101))
        self.assertTrue(facts["toolCompleted"])
        self.assertFalse(facts["turnCompleted"])
        facts = self.accept(reader, complete(response()))
        self.assertTrue(facts["turnCompleted"])

    def test_missing_source_is_not_schema_default(self):
        self.assert_rejected(lambda v: v["result"]["thread"]["turns"][0]["items"][-1].pop("source"), "source-unobserved")

    def test_other_documented_source_is_not_implicitly_agent(self):
        for source in ("userShell", "unifiedExecStartup", "unifiedExecInteraction", "unknown"):
            with self.subTest(source=source):
                self.assert_rejected(lambda v: v["result"]["thread"]["turns"][0]["items"][-1].update(source=source))

    def test_explicit_reviewed_source_allowlist_preserves_observed_enum(self):
        reader = self.reader(frozenset(("unifiedExecStartup",)))
        value = response()
        value["result"]["thread"]["turns"][0]["items"][-1]["source"] = "unifiedExecStartup"
        self.assertEqual(self.accept(reader, value, freeze=True)["source"], "unifiedExecStartup")
        with self.assertRaises(m.Refused):
            self.reader(frozenset(("userShell",)))

    def test_known_route_and_action_mismatches(self):
        mutations = [
            lambda v: v["result"]["thread"].update(id="different-thread"),
            lambda v: v["result"]["thread"].update(cliVersion="0.153.1"),
            lambda v: v["result"]["thread"].update(parentThreadId="another-thread"),
            lambda v: v["result"]["thread"]["turns"][0].update(id="different-turn"),
            lambda v: v["result"]["thread"]["turns"][0]["items"][-1].update(command=COMMAND + "; cat /secret"),
            lambda v: v["result"]["thread"]["turns"][0]["items"][-1].update(cwd="/foreign"),
            lambda v: v["result"]["thread"]["turns"][0]["items"][-1].update(pluginId="plugin-x"),
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutations.index(mutation)):
                self.assert_rejected(mutation)

    def test_unknown_fields_and_effects_are_refused(self):
        mutations = [
            lambda v: v.update(unknown=SENTINEL),
            lambda v: v["result"]["thread"].update(unknown=SENTINEL),
            lambda v: v["result"]["thread"]["turns"][0]["items"][-1].update(unknown=SENTINEL),
            lambda v: v["result"]["thread"]["turns"][0]["items"].append({"type": "contextCompaction", "id": "item-other"}),
            lambda v: v["result"]["thread"]["turns"][0]["items"].append(command()),
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutations.index(mutation)):
                self.assert_rejected(mutation)

    def test_partial_or_missing_view_cannot_prove_only_one_effect(self):
        for view in (None, "notLoaded", "summary"):
            self.assert_rejected(lambda v: v["result"]["thread"]["turns"][0].update(itemsView=view))
        self.assert_rejected(lambda v: v["result"]["thread"]["turns"][0].pop("itemsView"), "history-view-unproven")

    def test_completed_before_release_and_failed_status(self):
        for status in ("completed", "failed", "declined"):
            self.assert_rejected(lambda v: v["result"]["thread"]["turns"][0]["items"][-1].update(status=status))

    def test_wrong_completed_item_result_and_exit(self):
        mutations = [lambda i: i.update(id="other-item"), lambda i: i.update(exitCode=1),
                     lambda i: i.update(aggregatedOutput=json.dumps({**result(), "unknown": SENTINEL})),
                     lambda i: i.update(aggregatedOutput=SENTINEL), lambda i: i.update(exitCode=False)]
        for mutation in mutations:
            reader = self.reader()
            self.accept(reader, response(), freeze=True)
            reader.set_expected_result(result())
            value = complete(response())
            mutation(value["result"]["thread"]["turns"][0]["items"][-1])
            with self.assertRaises(m.Refused):
                self.accept(reader, value)

    def test_valid_but_wrong_correlated_reply_ref_fails(self):
        reader = self.reader()
        self.accept(reader, response(), freeze=True)
        reader.set_expected_result(result())
        value = complete(response())
        changed = result()
        changed["idle"]["replyRef"] = "wrong-but-valid-reply"
        value["result"]["thread"]["turns"][0]["items"][-1]["aggregatedOutput"] = json.dumps(changed)
        with self.assertRaisesRegex(m.Refused, "action-result-mismatch"):
            self.accept(reader, value)

    def test_unknown_notification_error_and_duplicate_envelopes(self):
        for value in ({"method": "unknown", "params": {}}, {"id": 1, "error": {"message": SENTINEL}}):
            with self.assertRaises(m.Refused):
                self.accept(self.reader(), value, freeze=True)
        with self.assertRaises(m.Refused):
            self.reader().accept(b'{"id":1,"id":1,"result":{}}', request_id=1, freeze=True)

    def test_frame_structure_and_total_bounds(self):
        with self.assertRaises(m.Refused):
            self.reader().accept(b"x" * (m.MAX_FRAME + 1), request_id=1)
        with self.assertRaises(m.Refused):
            m.decode(b"[" * 40 + b"0" + b"]" * 40)
        reader = self.reader()
        reader.total = m.MAX_TOTAL
        with self.assertRaises(m.Refused):
            self.accept(reader, response(), freeze=True)
        reader = self.reader()
        reader.frames = m.MAX_FRAMES
        with self.assertRaises(m.Refused):
            self.accept(reader, response(), freeze=True)


class FakeConnection:
    family = m.socket.AF_UNIX

    def __init__(self, payload=b""):
        self.payload, self.writes = payload, []

    def getsockopt(self, *_):
        return struct.pack("3i", 4242, m.os.getuid(), m.os.getgid())

    def settimeout(self, value):
        self.timeout = value

    def sendall(self, value):
        self.writes.append(value)

    def recv(self, limit):
        chunk, self.payload = self.payload[:limit], self.payload[limit:]
        return chunk


class ConnectionTests(ObservationTests):
    def connection(self, payload, **kwargs):
        peer = {"pid": 4242, "uid": m.os.getuid(), "startTicks": "42"}
        fake = FakeConnection(payload)
        self.patch = mock.patch.object(m, "process_birth", return_value=peer)
        self.patch.start()
        self.addCleanup(self.patch.stop)
        return m.OwnedReadConnection(fake, peer, self.reader(), **kwargs), fake

    def test_read_request_only_with_valid_response(self):
        conn, fake = self.connection(json.dumps(response()).encode() + b"\n")
        self.assertEqual(conn.read(freeze=True)["itemId"], "item-action")
        self.assertEqual(len(fake.writes), 1)
        self.assertEqual(json.loads(fake.writes[0])["method"], "thread/read")

    def test_eof_timeout_and_peer_replacement_fail_without_effect(self):
        conn, fake = self.connection(b"")
        with self.assertRaises(m.Refused):
            conn.read(freeze=True)
        self.assertTrue(conn.reader.invalid)
        conn, fake = self.connection(b"", clock=lambda: 20)
        conn.deadline = 10
        with self.assertRaises(m.Refused):
            conn.read(freeze=True)
        self.assertEqual(fake.writes, [])
        conn, fake = self.connection(json.dumps(response()).encode() + b"\n")
        with mock.patch.object(m, "process_birth", return_value={"pid": 4242, "uid": m.os.getuid(), "startTicks": "43"}):
            with self.assertRaises(m.Refused):
                conn.read(freeze=True)
        self.assertEqual(fake.writes, [])


class RequiredSchemaFieldTests(unittest.TestCase):
    def test_required_agent_message_id_remains_known_and_unknown_key_refused(self):
        schema = m.Schemas(ROOT)
        reader = m.ObservationReader(schema, "thread-source", "turn-source", COMMAND, CWD, frozenset(("agent",)))
        value = response()
        value["result"]["thread"]["turns"][0]["items"].insert(1, {
            "type": "agentMessage", "id": "item-assistant", "text": SENTINEL})
        facts = reader.accept(json.dumps(value).encode(), request_id=1, freeze=True)
        self.assertNotIn(SENTINEL, json.dumps(facts))
        reader.set_expected_result(result())
        completed = complete(value)
        self.assertTrue(reader.accept(json.dumps(completed).encode(), request_id=1)["turnCompleted"])
        value["result"]["thread"]["turns"][0]["items"][1]["unknown"] = SENTINEL
        with self.assertRaises(m.Refused):
            schema.validate("ThreadReadResponse.json", value["result"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
