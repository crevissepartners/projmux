from __future__ import annotations

import json
import subprocess
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def workflow_job(workflow: str, job: str) -> str:
    lines = workflow.splitlines()
    marker = f"  {job}:"
    try:
        start = lines.index(marker) + 1
    except ValueError as exc:
        raise AssertionError(f"workflow job is missing: {job}") from exc
    end = next(
        (
            index
            for index in range(start, len(lines))
            if lines[index].startswith("  ")
            and not lines[index].startswith("   ")
        ),
        len(lines),
    )
    return "\n".join(lines[start:end])


class CIWorkflowContractTest(unittest.TestCase):
    def test_race_children_preserve_coverage_behind_the_stable_aggregate(self) -> None:
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        core = workflow_job(workflow, "race-core")
        broker = workflow_job(workflow, "race-broker")
        appserver = workflow_job(workflow, "race-appserver")
        aggregate = workflow_job(workflow, "race")
        required_test = workflow_job(workflow, "test")

        for package in (
            "./internal/integrations/metadata/...",
            "./internal/core/notify/...",
            "./internal/core/recentwindows/...",
        ):
            self.assertIn(package, core)
        self.assertIn(
            "go test -race -count=2 ./internal/integrations/agents/codexbroker/...",
            broker,
        )
        self.assertIn(
            "go test -race -count=2 ./internal/integrations/agents/codexappserver/...",
            appserver,
        )

        self.assertIn("    name: Race Tests", aggregate)
        self.assertIn("    if: always()", aggregate)
        for child in ("race-core", "race-broker", "race-appserver"):
            self.assertIn(f"      - {child}", aggregate)
            self.assertIn(f"--required {child}", aggregate)
        self.assertIn("      - race", required_test)
        self.assertIn("--required race", required_test)
        self.assertNotIn("      - race-broker", required_test)
        self.assertNotIn("      - race-appserver", required_test)

    def test_broker_and_appserver_failure_make_race_tests_red(self) -> None:
        children = ("race-core", "race-broker", "race-appserver")
        gate = ROOT / "scripts/required-gate.py"
        for failed in ("race-broker", "race-appserver"):
            with self.subTest(failed=failed):
                results = {
                    child: {"result": "failure" if child == failed else "success"}
                    for child in children
                }
                command = [
                    sys.executable,
                    str(gate),
                    "--results-json",
                    json.dumps(results),
                ]
                for child in children:
                    command.extend(("--required", child))
                completed = subprocess.run(
                    command,
                    cwd=ROOT,
                    check=False,
                    capture_output=True,
                    text=True,
                )
                self.assertEqual(completed.returncode, 1)
                self.assertIn(
                    f"required gate: unsuccessful children: {failed}=failure",
                    completed.stderr,
                )


if __name__ == "__main__":
    unittest.main()
