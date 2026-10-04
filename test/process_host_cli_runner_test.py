from __future__ import annotations

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
                (directory / filename).write_text(f"func TestFixture{index}(t *testing.T) {{}}\n")
            self.assertEqual(len(runner.selected_tests(root)), len(runner.FILES))
            with (directory / runner.FILES[0]).open("a") as output:
                output.write("func TestNewFixture(\n  other *testing.T,\n) {}\n")
            self.assertIn("TestNewFixture", runner.selected_tests(root))
            (directory / runner.FILES[1]).write_text("package app\n")
            with self.assertRaises(ValueError):
                runner.selected_tests(root)

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
