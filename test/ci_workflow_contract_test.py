from __future__ import annotations

import json
import shutil
import subprocess
import sys
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
            if lines[index].startswith("  ")
            and not lines[index].startswith("   ")
        ),
        len(lines),
    )
    return "\n".join(lines[start:end])


class CIWorkflowContractTest(unittest.TestCase):
    def test_installed_codex_schedule_is_a_separate_fail_closed_matrix(self) -> None:
        ci_workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        workflow = (ROOT / ".github/workflows/installed-codex.yml").read_text(
            encoding="utf-8"
        )
        installed = workflow_job(workflow, "installed-codex")
        aggregate = workflow_job(workflow, "installed-codex-qualification")
        e2e_required = workflow_job(ci_workflow, "e2e-tests")
        required_test = workflow_job(ci_workflow, "test")

        self.assertIn('    - cron: "17 3 * * *"', workflow)
        self.assertIn("  workflow_dispatch:", workflow)
        self.assertIn("  cancel-in-progress: false", workflow)
        self.assertNotIn("  installed-codex:", ci_workflow)
        self.assertIn('          - "0.152.0"', installed)
        for primitive in ("daemon-lifecycle", "thread-list", "pre-turn-attach"):
            self.assertEqual(installed.count(f"          - {primitive}"), 1)
        self.assertIn(
            "      - name: Install the declared real Codex CLI\n"
            "        id: install-codex\n"
            "        continue-on-error: true",
            installed,
        )
        self.assertIn(
            'npm install --prefix "$npm_prefix" --ignore-scripts --no-audit '
            '--no-fund "@openai/codex@${CODEX_VERSION}"',
            installed,
        )
        self.assertIn("scripts/stage-installed-codex-release.sh", installed)
        self.assertIn('echo "$release_root/bin" >> "$GITHUB_PATH"', installed)
        self.assertIn(
            'test "$(command -v codex)" = "$release_root/bin/codex"', installed
        )
        self.assertIn("scripts/test-installed-codex-qualification.sh", installed)
        self.assertIn(
            "      - name: Run canonical installed canary\n        if: always()",
            installed,
        )
        self.assertIn(
            "          PROJMUX_CODEX_INSTALL_OUTCOME: ${{ steps.install-codex.outcome }}",
            installed,
        )
        self.assertIn(
            "          PROJMUX_CODEX_EXPECTED_VERSION: ${{ matrix.codex-version }}",
            installed,
        )
        self.assertIn(
            "          PROJMUX_CODEX_EVIDENCE_RUN: "
            "github-actions:${{ github.run_id }}:${{ github.run_attempt }}",
            installed,
        )
        self.assertIn("          OPENAI_API_KEY: \"\"", installed)
        self.assertIn("          CODEX_API_KEY: \"\"", installed)
        self.assertIn("          CODEX_TOKEN: \"\"", installed)
        self.assertIn("      - name: Upload typed primitive result", installed)
        self.assertIn("        if: always()", installed)
        self.assertIn("          if-no-files-found: error", installed)
        self.assertIn("          retention-days: 14", installed)

        self.assertIn("    name: Installed Codex Qualification", aggregate)
        self.assertIn("    if: always()", aggregate)
        self.assertIn("      - installed-codex", aggregate)
        self.assertIn("            aggregate \\", aggregate)
        self.assertIn("            artifacts/installed-codex/qualification.json", aggregate)
        self.assertIn("            --required installed-codex", aggregate)
        self.assertIn("      - name: Upload typed qualification bundle", aggregate)

        # The volatile real-binary lane reports its own non-required status. It
        # can neither replace fake C01 nor flow into either stable aggregate.
        for stable_aggregate in (e2e_required, required_test):
            self.assertNotIn("installed-codex", stable_aggregate)

    def test_native_platform_payload_stages_as_canonical_release(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            temporary_path = Path(temporary)
            prefix = temporary_path / "npm"
            meta = prefix / "node_modules/@openai/codex"
            platform = prefix / "node_modules/@openai/codex-linux-x64"
            source_release = platform / "vendor/x86_64-unknown-linux-musl"
            native = source_release / "bin/codex"
            native.parent.mkdir(parents=True)
            meta.mkdir(parents=True)
            native.write_text(
                "#!/bin/sh\nprintf 'codex-cli 0.152.0\\n'\n", encoding="utf-8"
            )
            native.chmod(0o700)
            (meta / "package.json").write_text(
                json.dumps({"name": "@openai/codex", "version": "0.152.0"}),
                encoding="utf-8",
            )
            (platform / "package.json").write_text(
                json.dumps(
                    {"name": "@openai/codex", "version": "0.152.0-linux-x64"}
                ),
                encoding="utf-8",
            )
            manifest = {
                "layoutVersion": 1,
                "version": "0.152.0",
                "target": "x86_64-unknown-linux-musl",
                "variant": "codex",
                "entrypoint": "bin/codex",
                "resourcesDir": "codex-resources",
                "pathDir": "codex-path",
            }
            (source_release / "codex-package.json").write_text(
                json.dumps(manifest), encoding="utf-8"
            )
            release = temporary_path / "release/0.152.0-x86_64-unknown-linux-musl"
            stage = ROOT / "scripts/stage-installed-codex-release.sh"
            completed = subprocess.run(
                ["bash", str(stage), str(prefix), str(release), "0.152.0"],
                cwd=ROOT,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            staged = release / "bin/codex"
            self.assertEqual(staged.resolve(), staged)
            self.assertEqual(staged.parent.parent, release)
            self.assertEqual((release / "codex").resolve(), staged)
            self.assertEqual(
                json.loads((release / "codex-package.json").read_text())["version"],
                "0.152.0",
            )
            version = subprocess.run(
                [str(staged), "--version"],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()
            self.assertEqual(version, "codex-cli 0.152.0")

            # npm normally hoists the optional platform alias beside the meta
            # package. Retain nested compatibility for installers that do not,
            # but never choose silently when both valid locations exist.
            nested = meta / "node_modules/@openai/codex-linux-x64"
            nested.parent.mkdir(parents=True)
            shutil.move(platform, nested)
            nested_release = temporary_path / "nested-release"
            nested_result = subprocess.run(
                ["bash", str(stage), str(prefix), str(nested_release), "0.152.0"],
                cwd=ROOT,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(nested_result.returncode, 0, nested_result.stderr)
            self.assertEqual(
                (nested_release / "codex").resolve(), nested_release / "bin/codex"
            )

            shutil.copytree(nested, platform)
            ambiguous_release = temporary_path / "ambiguous-release"
            ambiguous = subprocess.run(
                [
                    "bash",
                    str(stage),
                    str(prefix),
                    str(ambiguous_release),
                    "0.152.0",
                ],
                cwd=ROOT,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(ambiguous.returncode, 0)
            self.assertIn("platform package resolution is ambiguous", ambiguous.stderr)
            self.assertFalse(ambiguous_release.exists())

            shutil.rmtree(platform)
            bad_release = temporary_path / "bad-release"
            manifest["version"] = "0.151.0"
            nested_source_release = nested / "vendor/x86_64-unknown-linux-musl"
            (nested_source_release / "codex-package.json").write_text(
                json.dumps(manifest), encoding="utf-8"
            )
            rejected = subprocess.run(
                ["bash", str(stage), str(prefix), str(bad_release), "0.152.0"],
                cwd=ROOT,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(rejected.returncode, 0)
            self.assertFalse(bad_release.exists())

            shutil.rmtree(nested)
            missing_release = temporary_path / "missing-release"
            missing = subprocess.run(
                ["bash", str(stage), str(prefix), str(missing_release), "0.152.0"],
                cwd=ROOT,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(missing.returncode, 0)
            self.assertIn("platform package was not found", missing.stderr)
            self.assertFalse(missing_release.exists())

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
