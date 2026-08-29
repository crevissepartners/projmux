from __future__ import annotations

import json
import re
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
            if lines[index].startswith("  ") and not lines[index].startswith("   ")
        ),
        len(lines),
    )
    return "\n".join(lines[start:end])


def matrix_values(job: str, axis: str) -> list[str]:
    lines = job.splitlines()
    marker = f"        {axis}:"
    try:
        start = lines.index(marker) + 1
    except ValueError as exc:
        raise AssertionError(f"matrix axis is missing: {axis}") from exc
    values: list[str] = []
    for line in lines[start:]:
        if not line.startswith("          - "):
            break
        values.append(line.removeprefix("          - "))
    return values


class ReleaseWorkflowContractTest(unittest.TestCase):
    def test_draft_release_forces_tag_creation_in_the_release_pass(self) -> None:
        config = json.loads(
            (ROOT / "release-please-config.json").read_text(encoding="utf-8")
        )
        package = config["packages"]["."]

        self.assertIs(package.get("draft"), True)
        self.assertIs(
            package.get("force-tag-creation"),
            True,
            "draft releases must create their tag before release-please computes the next PR",
        )

    def test_release_workflow_has_no_post_action_tag_workaround(self) -> None:
        workflow = (ROOT / ".github/workflows/release-please.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("googleapis/release-please-action@v5", workflow)
        self.assertRegex(
            workflow,
            re.compile(
                r"^      - uses: googleapis/release-please-action@v5\n"
                r"(?: {8,}.*\n)*?"
                r"          token: \$\{\{ secrets\.RELEASE_PLEASE_TOKEN \}\}$",
                re.MULTILINE,
            ),
            "release-please must use the PAT so its tag push dispatches release.yml",
        )
        for retired_fragment in (
            "Create release tag",
            "steps.release.outputs.release_created",
            "git/ref/tags/",
            "git/refs",
        ):
            with self.subTest(fragment=retired_fragment):
                self.assertNotIn(retired_fragment, workflow)

    def test_release_workflow_still_listens_for_version_tag_pushes(self) -> None:
        workflow = (ROOT / ".github/workflows/release.yml").read_text(
            encoding="utf-8"
        )

        self.assertRegex(
            workflow,
            re.compile(
                r"^on:\n"
                r"  push:\n"
                r"    tags:\n"
                r"      - ['\"]?v\*['\"]?$",
                re.MULTILINE,
            ),
            "release.yml must run when release-please pushes a v* tag",
        )

    def test_release_e2e_shards_reduce_through_one_fail_closed_gate(self) -> None:
        workflow = (ROOT / ".github/workflows/release.yml").read_text(
            encoding="utf-8"
        )
        linux = workflow_job(workflow, "e2e-linux")
        suites = workflow_job(workflow, "e2e-suite")
        aggregate = workflow_job(workflow, "e2e")
        build = workflow_job(workflow, "release")

        self.assertEqual(
            matrix_values(linux, "shard"),
            ["fixture-1", "fixture-2", "fixture-3", "fixture-4"],
        )
        self.assertEqual(
            matrix_values(suites, "suite"),
            ["codex-lifecycle", "npm-staging"],
        )
        for block in (linux, suites):
            self.assertIn("      fail-fast: false", block)
            self.assertIn("    timeout-minutes: 30", block)
        self.assertIn("PROJMUX_E2E_LINUX_SHARD: ${{ matrix.shard }}", linux)
        self.assertIn("PROJMUX_E2E_SUITE: ${{ matrix.suite }}", suites)

        self.assertIn("    name: Release E2E Tests", aggregate)
        self.assertIn("    if: always()", aggregate)
        for child in ("e2e-linux", "e2e-suite"):
            self.assertIn(f"      - {child}", aggregate)
            self.assertIn(f"--required {child}", aggregate)
            self.assertNotIn(f"      - {child}", build)
        self.assertIn("      - e2e", build)

    def test_release_publish_order_stays_serial_after_build(self) -> None:
        workflow = (ROOT / ".github/workflows/release.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("    needs: release", workflow_job(workflow, "publish"))
        self.assertIn("    needs: publish", workflow_job(workflow, "publish-npm"))
        self.assertIn(
            "    needs: publish-npm", workflow_job(workflow, "publish-release")
        )


if __name__ == "__main__":
    unittest.main()
