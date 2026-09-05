from __future__ import annotations

import json
import os
import re
import subprocess
import tempfile
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


def step_script(job: str, step: str) -> str:
    """Return the `run: |` body of a named step, dedented to column zero."""
    lines = job.splitlines()
    marker = f"      - name: {step}"
    try:
        start = lines.index(marker)
    except ValueError as exc:
        raise AssertionError(f"workflow step is missing: {step}") from exc
    end = next(
        (
            index
            for index in range(start + 1, len(lines))
            if lines[index].startswith("      - ")
        ),
        len(lines),
    )
    block = lines[start:end]
    try:
        run_at = next(
            index for index, line in enumerate(block) if line == "        run: |"
        )
    except StopIteration as exc:
        raise AssertionError(f"step has no block script: {step}") from exc
    body: list[str] = []
    for line in block[run_at + 1 :]:
        if line.strip() and not line.startswith("          "):
            break
        body.append(line[10:])
    return "\n".join(body).rstrip() + "\n"


def run_step_script(script: str, ref_name: str, *stubs: str) -> list[str]:
    """Execute a workflow step script with stubbed tools; return recorded argv."""
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        bindir = root / "bin"
        bindir.mkdir()
        record = root / "invocations"
        for stub in stubs:
            path = bindir / stub
            path.write_text(
                "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$RECORD\"\n",
                encoding="utf-8",
            )
            path.chmod(0o755)
        entry = root / "step.sh"
        entry.write_text(script, encoding="utf-8")
        env = dict(os.environ)
        env["PATH"] = f"{bindir}{os.pathsep}{env['PATH']}"
        env["RECORD"] = str(record)
        env["GITHUB_REF_NAME"] = ref_name
        subprocess.run(["bash", str(entry)], check=True, env=env, cwd=tmp)
        if not record.exists():
            return []
        return record.read_text(encoding="utf-8").splitlines()


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

    def test_release_please_config_declares_no_prerelease_keys(self) -> None:
        raw = (ROOT / "release-please-config.json").read_text(encoding="utf-8")
        config = json.loads(raw)

        # release-please 17.6.0 gates the GitHub prerelease flag on
        # `config.prerelease && (version.preRelease || version.major === 0)`
        # (manifest.js:657-659). This repository is 0.x, so turning the key on
        # would stamp *stable* releases as prereleases, `releases/latest` would
        # stop resolving, and every default-channel install would go quiet.
        for forbidden in ("prerelease", "prerelease-type"):
            with self.subTest(key=forbidden):
                self.assertNotIn(f'"{forbidden}"', raw)

        def walk(node: object) -> None:
            if isinstance(node, dict):
                for key, value in node.items():
                    self.assertNotIn(key, ("prerelease", "prerelease-type"))
                    walk(value)
            elif isinstance(node, list):
                for value in node:
                    walk(value)

        walk(config)

    def test_rc_cut_workflow_is_manual_only(self) -> None:
        workflow = (ROOT / ".github/workflows/release-rc.yml").read_text(
            encoding="utf-8"
        )

        # Cutting an rc publishes to npm, and npm publishes are not recallable.
        self.assertRegex(
            workflow,
            re.compile(r"^on:\n  workflow_dispatch:\n", re.MULTILINE),
            "the rc cut must be workflow_dispatch only",
        )
        for automatic_trigger in ("  push:", "  schedule:", "  pull_request:"):
            with self.subTest(trigger=automatic_trigger.strip()):
                self.assertNotIn(
                    f"\n{automatic_trigger}\n", workflow.split("\njobs:", 1)[0]
                )

    def test_rc_cut_drafts_a_prerelease_and_pushes_the_tag_with_the_pat(
        self,
    ) -> None:
        workflow = (ROOT / ".github/workflows/release-rc.yml").read_text(
            encoding="utf-8"
        )
        job = workflow_job(workflow, "cut-rc")

        draft = step_script(job, "Draft the prerelease GitHub Release")
        self.assertIn("gh release create", draft)
        self.assertIn("--prerelease", draft)
        self.assertIn("--draft", draft)

        push = step_script(job, "Push the rc tag")
        self.assertIn('git push origin "refs/tags/${TAG}"', push)
        self.assertIn(
            "token: ${{ secrets.RELEASE_PLEASE_TOKEN }}",
            job,
            "a GITHUB_TOKEN tag push would never dispatch release.yml",
        )

    def test_rc_cut_never_writes_release_please_state(self) -> None:
        workflow = (ROOT / ".github/workflows/release-rc.yml").read_text(
            encoding="utf-8"
        )

        # C-2 holds only because `.release-please-manifest.json` keeps naming a
        # stable version: release-please picks the previous release by matching
        # that value (manifest.js:205-238), with no prerelease filter anywhere
        # on the path. An rc that claimed the manifest slot would silently trim
        # the next stable release notes and its compare link.
        for owned_by_release_please in (
            ".release-please-manifest.json",
            "CHANGELOG.md",
            "release-please-config.json",
        ):
            with self.subTest(path=owned_by_release_please):
                self.assertNotIn(owned_by_release_please, workflow)
        for mutation in ("git commit", "git push origin main", "git push origin HEAD"):
            with self.subTest(fragment=mutation):
                self.assertNotIn(mutation, workflow)

    def test_only_a_prerelease_tag_publishes_to_the_rc_dist_tag(self) -> None:
        workflow = (ROOT / ".github/workflows/release.yml").read_text(
            encoding="utf-8"
        )
        script = step_script(
            workflow_job(workflow, "publish-npm"), "Publish npm packages"
        )
        packages = [
            "dist/npm/@projmux/linux-x64",
            "dist/npm/@projmux/linux-arm64",
            "dist/npm/@projmux/darwin-x64",
            "dist/npm/@projmux/darwin-arm64",
            "dist/npm/projmux",
        ]

        stable = run_step_script(script, "v0.14.2", "npm")
        self.assertEqual(
            stable,
            [f"publish {package} --access public" for package in packages],
            "the stable path must keep publishing without an explicit dist-tag",
        )

        rc = run_step_script(script, "v0.15.0-rc.1", "npm")
        self.assertEqual(
            rc,
            [f"publish {package} --access public --tag rc" for package in packages],
            "npm moves dist-tags.latest onto anything published without --tag",
        )

    def test_undrafting_keeps_a_prerelease_marked_as_one(self) -> None:
        workflow = (ROOT / ".github/workflows/release.yml").read_text(
            encoding="utf-8"
        )
        script = step_script(
            workflow_job(workflow, "publish-release"), "Publish the drafted release"
        )

        self.assertEqual(
            run_step_script(script, "v0.14.2", "gh"),
            ["release edit v0.14.2 --draft=false"],
        )
        self.assertEqual(
            run_step_script(script, "v0.15.0-rc.1", "gh"),
            ["release edit v0.15.0-rc.1 --draft=false --prerelease"],
            "releases/latest excludes prereleases; that exclusion hides rc",
        )

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
