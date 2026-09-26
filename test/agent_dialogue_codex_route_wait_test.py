"""The dialogue Codex route wait tells "not yet" from "wrong" without tracebacks."""
import copy
import json
import os
import pathlib
import shlex
import subprocess
import tempfile
import unittest


HARNESS_DEADLINE = 30  # seconds: long enough that load cannot trip it, so expiry means a hang
ROOT = pathlib.Path(__file__).resolve().parents[1]
SOURCE = ROOT / "test/e2e/heterogeneous-agent-dialogue.inc.sh"
CALL = 'smoke_wait_for "dialogue exact Codex composite route" dialogue_codex_route_ready'
VALID = {
    "runtimeEligibility": {
        "registryReady": True,
        "routeIncarnation": "route-1",
        "stateDomainID": "dialogue-state-domain",
        "endpointGenerationID": "dialogue-endpoint-generation",
        "brokerRuntimeID": "0123456789abcdef0123456789abcdef",
        "connectionEpoch": 1,
        "bindingEpoch": 1,
    },
    "capabilities": [
        {"action": "message.send", "available": True},
        {"action": "message.status", "available": True},
        {"action": "message.reply", "available": False},
    ],
}


def predicate_source(text):
    """The real wait-file definition, predicate, and call line, not a copy."""
    start = text.index('dialogue_codex_capabilities="$dialogue_root/capabilities-codex.json"\n')
    end = text.index(CALL + "\n", start) + len(CALL)
    return text[start:end]


class CodexRouteWaitTest(unittest.TestCase):
    source = SOURCE.read_text()

    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="pmx-codex-route-wait-")
        self.addCleanup(temp.cleanup)
        self.root = pathlib.Path(temp.name)
        self.fixture = self.root / "fixture.json"
        self.capabilities = self.root / "capabilities-codex.json"
        self.wait = self.root / "capabilities-codex.json.wait"

    def write(self, value):
        self.fixture.write_text(value if isinstance(value, str) else json.dumps(value))

    def variant(self, runtime=None, drop=(), available=None):
        value = copy.deepcopy(VALID)
        value["runtimeEligibility"].update(runtime or {})
        for key in drop:
            del value["runtimeEligibility"][key]
        for row in value["capabilities"]:
            if available and row["action"] in available:
                row["available"] = available[row["action"]]
        return value

    def run_shell(self, body, pmx='dialogue_pmx() { cat "$FIXTURE"; }'):
        block = predicate_source(self.source)
        call = block.rsplit("\n", 1)[1]
        predicate = block.rsplit("\n", 1)[0]
        script = "\n".join([
            "set -euo pipefail",
            "dialogue_root=" + shlex.quote(str(self.root)),
            "dialogue_codex_uid=agent-codex",
            pmx,
            predicate,
            body.replace("@CALL@", call),
        ])
        return subprocess.run(["bash", "-c", script], cwd=ROOT, capture_output=True, text=True,
                              env={**os.environ, "FIXTURE": str(self.fixture), "E2E_WAIT_SCALE": "1"},
                              timeout=HARNESS_DEADLINE, check=False)

    def poll(self, **kwargs):
        return self.run_shell("rc=0; dialogue_codex_route_ready || rc=$?; exit $rc", **kwargs)

    def assert_quiet_pending(self, value, condition):
        self.write(value)
        done = self.poll()
        self.assertEqual(done.returncode, 1, done.stderr)
        self.assertNotIn("Traceback", done.stderr)
        self.assertEqual(done.stderr, "")
        self.assertFalse(self.capabilities.exists())
        record = self.wait.read_text()
        self.assertTrue(record.startswith("pending: "), record)
        self.assertIn(condition, record)

    def assert_one_wrong_line(self, value, *needles):
        self.write(value)
        done = self.poll()
        self.assertEqual(done.returncode, 1, done.stderr)
        self.assertNotIn("Traceback", done.stderr)
        lines = done.stderr.splitlines()
        self.assertEqual(len(lines), 1, done.stderr)
        for needle in needles:
            self.assertIn(needle, lines[0])
        self.assertFalse(self.capabilities.exists())
        self.assertTrue(self.wait.read_text().startswith("wrong: "))
        for needle in needles:
            self.assertIn(needle, self.wait.read_text())
        return done

    def test_absent_route_key_is_silent_pending(self):
        self.assert_quiet_pending(self.variant(drop=("routeIncarnation",)), "routeIncarnation absent")

    def test_registry_not_ready_is_silent_pending(self):
        self.assert_quiet_pending(self.variant({"registryReady": False}), "registryReady=false")

    def test_unavailable_send_is_silent_pending(self):
        self.assert_quiet_pending(self.variant(available={"message.send": False}),
                                  "capabilities[message.send].available=false")

    def test_failed_capabilities_command_is_recorded_pending(self):
        done = self.poll(pmx="dialogue_pmx() { return 3; }")
        self.assertEqual(done.returncode, 1, done.stderr)
        self.assertEqual(done.stderr, "")
        self.assertIn("agent capabilities exited non-zero", self.wait.read_text())

    def test_bad_route_prefix_is_one_wrong_line(self):
        self.assert_one_wrong_line(self.variant({"routeIncarnation": "incarnation-1"}),
                                   "routeIncarnation", '"incarnation-1"')

    def test_broken_json_is_one_wrong_line(self):
        self.assert_one_wrong_line('{"runtimeEligibility":', "capabilities JSON")

    def test_wrong_epoch_names_the_field(self):
        self.assert_one_wrong_line(self.variant({"bindingEpoch": 2}), "bindingEpoch=2")

    def test_missing_catalog_row_is_wrong(self):
        value = self.variant()
        value["capabilities"] = [row for row in value["capabilities"] if row["action"] != "message.status"]
        self.assert_one_wrong_line(value, "message.status")

    def test_wrong_type_is_wrong_not_pending(self):
        self.assert_one_wrong_line(self.variant({"registryReady": "yes"}), "registryReady", '"yes"')

    def test_unexpected_shape_is_one_wrong_line(self):
        value = self.variant()
        value["capabilities"][0]["action"] = ["message.send"]
        self.assert_one_wrong_line(value, "unexpected capabilities shape")

    def test_repeated_wrong_reason_prints_once(self):
        self.assert_one_wrong_line(self.variant({"routeIncarnation": "incarnation-1"}), "routeIncarnation")
        again = self.poll()
        self.assertEqual(again.returncode, 1)
        self.assertEqual(again.stderr, "")

    def test_ready_moves_the_capabilities(self):
        self.write(VALID)
        done = self.poll()
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual(done.stderr, "")
        self.assertEqual(json.loads(self.capabilities.read_text()), VALID)

    def test_timeout_names_the_last_unmet_condition(self):
        self.write(self.variant(drop=("routeIncarnation",)))
        # The real call line, with smoke_wait_for's 5s budget shortened.
        done = self.run_shell("source " + shlex.quote(str(ROOT / "test/lib/smoke.sh")) +
                              '\nsmoke_wait_for() { smoke_wait_until 0.2 "$@"; }\n@CALL@')
        self.assertNotEqual(done.returncode, 0)
        self.assertNotIn("Traceback", done.stderr)
        self.assertIn("timed out waiting for dialogue exact Codex composite route", done.stderr)
        self.assertIn("pending: runtimeEligibility.routeIncarnation absent", done.stderr)


if __name__ == "__main__":
    unittest.main()
