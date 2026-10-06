from __future__ import annotations

import contextlib
import io
import json
import subprocess
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "process_host_cli_runner", ROOT / "scripts/test-process-host-cli.py")
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class ProcessHostCLIRunnerTest(unittest.TestCase):
    def test_rejects_skip_failure_missing_and_empty_selection(self):
        green = [{"Action": "pass", "Test": "TestFixture"}, {"Action": "pass"}]
        self.assertEqual(runner.check_results(green, ["TestFixture"], 0)["passed"], 1)
        for events, expected, status in [
            (green + [{"Action": "skip", "Test": "TestFixture/subtest"}], ["TestFixture"], 0),
            (green + [{"Action": "fail"}], ["TestFixture"], 0),
            (green, ["TestMissing"], 0),
            (green, [], 0),
            (green[:1], ["TestFixture"], 0),
            (green, ["TestFixture"], 1),
        ]:
            with self.subTest(events=events, expected=expected, status=status):
                with self.assertRaises(ValueError):
                    runner.check_results(events, expected, status)

    def test_new_tests_in_selected_files_are_included(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            directory = root / "internal/app"
            directory.mkdir(parents=True)
            for index, filename in enumerate(runner.FILES):
                (directory / filename).write_text(
                    'const cli = "PMX_TEST_CLI"\n'
                    f"func TestFixture{index}(t *testing.T) {{}}\n")
            self.assertEqual(len(runner.selected_tests(root)), len(runner.FILES))
            with (directory / runner.FILES[0]).open("a") as output:
                output.write("func TestNewFixture(\n  other *testing.T,\n) {}\n")
            self.assertIn("TestNewFixture", runner.selected_tests(root))
            (directory / runner.FILES[1]).write_text("package app\n")
            with self.assertRaises(ValueError):
                runner.selected_tests(root)

    def test_new_consumer_file_or_removed_consumer_fails_inventory(self):
        runner.check_fixture_files(ROOT)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            directory = root / "internal/app"
            directory.mkdir(parents=True)
            for filename in runner.FILES:
                (directory / filename).write_text('const cli = "PMX_TEST_CLI"\n')
            runner.check_fixture_files(root)
            extra = root / "internal/newpackage/extra_test.go"
            extra.parent.mkdir()
            extra.write_text('const cli = "PMX_TEST_CLI"\n')
            with self.assertRaisesRegex(ValueError, "unlisted=.*extra_test.go"):
                runner.check_fixture_files(root)
            extra.unlink()
            excluded = root / next(iter(runner.EXCLUDED_FILES))
            excluded.write_text('const cli = "PMX_TEST_CLI"\n')
            runner.check_fixture_files(root)
            (directory / runner.FILES[0]).write_text('const cli = "OTHER"\n')
            with self.assertRaisesRegex(ValueError, "no longer consuming"):
                runner.check_fixture_files(root)

    def test_timeout_partial_metadata_is_bounded_private_and_preserves_failure(self):
        sentinel = "private-timeout-fixture-sentinel"
        event = {"Action": "run", "Test": "TestFixture/" + sentinel, "Output": sentinel}
        partial = "\n".join([json.dumps(event)] * 24 + [
            "null", "[]", "42", '"text"',
            json.dumps({"Test": None}), json.dumps({"Test": []}),
            json.dumps({"Test": "TestFixture", "Action": {}}),
            json.dumps({"Test": sentinel, "Action": "run"}),
            '{"Action":"run"'])
        for captured in (partial.encode(), partial, None):
            with self.subTest(kind=type(captured).__name__):
                expired = subprocess.TimeoutExpired("go test", 240, output=captured, stderr=sentinel)
                output = io.StringIO()
                def invoke(command, **kwargs):
                    if command[1] == "build":
                        Path(command[command.index("-o") + 1]).write_bytes(b"fixture-binary")
                        return subprocess.CompletedProcess(command, 0)
                    if "-c" in command:
                        self.assertEqual(kwargs["timeout"], 120)
                        self.assertTrue(kwargs["check"])
                        Path(command[command.index("-o") + 1]).write_bytes(b"fixture-test-binary")
                        return subprocess.CompletedProcess(command, 0)
                    self.assertEqual(kwargs["timeout"], 240)
                    self.assertIn("-timeout=180s", command)
                    raise expired
                with patch.object(runner, "selected_tests", return_value=["TestFixture"]), patch.object(
                    runner, "isolated_env", return_value={}), patch.object(
                    runner.subprocess, "run", side_effect=invoke), contextlib.redirect_stdout(output):
                    with self.assertRaises(subprocess.TimeoutExpired) as caught:
                        runner.main()
                self.assertIs(caught.exception, expired)
                text = output.getvalue()
                self.assertNotIn(sentinel, text)
                self.assertNotIn("process CLI summary:", text)
                report = json.loads(text.split("process CLI timeout evidence: ")[1])
                self.assertEqual(len(report["events"]), 0 if captured is None else 16)
                self.assertTrue(all(row == {"test": "TestFixture", "action": "run"} for row in report["events"]))
                self.assertGreaterEqual(report["elapsed_seconds"], report["test_command_seconds"])
                self.assertGreaterEqual(report["elapsed_seconds"], report["precompile_seconds"])

    def test_isolation_removes_ambient_routes_and_provider_credentials(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            ambient = {"HOME": "/ambient", "PATH": "/usr/bin", "TMUX": "live",
                       "TMUX_PANE": "%1", "TMUX_TMPDIR": "/live", "PROJMUX_SOCKET": "live",
                       "__PROJMUX_RUNTIME_ANCHOR_PANE": "live", "PMX_TEST_CLI": "/old",
                       "OPENAI_API_KEY": "fixture", "ANTHROPIC_AUTH_TOKEN": "fixture",
                       "PROCESSHOST_TEST_CODEX": "1", "XDG_STATE_HOME": "/live"}
            with patch.dict(os.environ, ambient, clear=True), patch.object(
                runner.subprocess, "check_output",
                return_value='{"GOCACHE":"/fixed/cache","GOMODCACHE":"/fixed/mod"}',
            ):
                env = runner.isolated_env(root, "go")
            for key in ("TMUX", "TMUX_PANE", "PROJMUX_SOCKET", "PMX_TEST_CLI",
                        "__PROJMUX_RUNTIME_ANCHOR_PANE", "OPENAI_API_KEY",
                        "ANTHROPIC_AUTH_TOKEN", "PROCESSHOST_TEST_CODEX"):
                self.assertNotIn(key, env)
            for key in ("HOME", "XDG_STATE_HOME", "CODEX_HOME", "TMUX_TMPDIR"):
                self.assertTrue(Path(env[key]).is_relative_to(root))
                self.assertTrue(Path(env[key]).is_dir())
            self.assertEqual(env["GOCACHE"], "/fixed/cache")
            self.assertEqual(env["GOMODCACHE"], "/fixed/mod")
            self.assertEqual(env["GOTOOLCHAIN"], "local")


if __name__ == "__main__":
    unittest.main()
