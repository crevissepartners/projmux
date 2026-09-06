"""No provider traffic: exercise the live canary's public-stream sanitizer."""
import json
import pathlib
import subprocess
import tempfile
import unittest


class DialogueCollectorTest(unittest.TestCase):
    def setUp(self):
        source = (pathlib.Path(__file__).resolve().parents[1] /
                  "scripts/agent-dialogue-live-canary.sh").read_text()
        code = source.split("print(r'''#!/usr/bin/env python3\n", 1)[1].split("''')\nPY", 1)[0]
        self.root = tempfile.TemporaryDirectory(prefix="pmx-dialogue-collector-")
        self.addCleanup(self.root.cleanup)
        root = pathlib.Path(self.root.name)
        (root / "bin").mkdir()
        (root / "home/.claude").mkdir(parents=True)
        self.collector = root / "bin/collector"
        self.collector.write_text(code)
        commands = [
            "projmux internal agent-hook ingest claude-hook --pane=${PMX_INTERNAL_ACTIVATION_PANE_UID:-} >/dev/null 2>&1 || true # projmux-managed:claude-hook:v1",
            "exec projmux internal claude-endpoint-register >/dev/null 2>&1 # projmux-managed:claude-hook:v1",
        ]
        (root / "home/.claude/settings.json").write_text(json.dumps({"hooks": {
            "SessionStart": [{"hooks": [{"type": "command", "command": c} for c in commands]}]}}))
        self.rows = []
        for index, command in enumerate(commands):
            base = dict(type="system", hook_id=f"hook-{index}", hook_name=command,
                        hook_event="SessionStart", session_id="owned-session", uuid=f"event-{index}")
            self.rows.extend([dict(base, subtype="hook_started"), dict(base, subtype="hook_response",
                              output="", stdout="", stderr="", exit_code=0, outcome="success")])
        self.rows.append(dict(type="system", subtype="init", session_id="owned-session"))

    def collect(self, rows):
        return subprocess.run(["python3", str(self.collector)],
                              input="".join(json.dumps(row) + "\n" for row in rows),
                              text=True, capture_output=True, timeout=5, check=False)

    def test_paired_owned_startup_strips_empty_output(self):
        result = self.collect(self.rows)
        self.assertEqual(result.returncode, 0, result.stderr)
        for event in map(json.loads, result.stdout.splitlines()):
            self.assertFalse({"output", "stdout", "stderr"} & set(event))

    def test_foreign_or_incomplete_startup_fails_without_retaining_output(self):
        for mutation in ("alias", "output", "session", "pending", "plugin", "unknown", "setup"):
            with self.subTest(mutation=mutation):
                rows = [dict(row) for row in self.rows]
                if mutation == "alias":
                    rows[0]["hook_name"] = "unobserved-name"
                elif mutation == "output":
                    rows[1]["output"] = "DO_NOT_RETAIN"
                elif mutation == "session":
                    rows[1]["session_id"] = "foreign"
                elif mutation == "pending":
                    rows.pop(1)
                elif mutation == "plugin":
                    rows[0]["subtype"] = "plugin_install"
                elif mutation == "unknown":
                    rows[0]["unobserved-field"] = "DO_NOT_RETAIN"
                elif mutation == "setup":
                    rows[0]["hook_event"] = "Setup"
                result = self.collect(rows)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("DO_NOT_RETAIN", result.stdout + result.stderr)
                self.assertNotIn("unobserved-field", result.stderr)

    def test_observed_startup_display_alias_keeps_two_distinct_hook_pairs(self):
        rows = [dict(row) for row in self.rows]
        for row in rows[:-1]:
            row["hook_name"] = "SessionStart:startup"
        result = self.collect(rows)
        self.assertEqual(result.returncode, 0, result.stderr)
        rows[2]["hook_id"] = rows[0]["hook_id"]
        self.assertNotEqual(self.collect(rows).returncode, 0)


if __name__ == "__main__":
    unittest.main()
