from __future__ import annotations

import json
import os
import re
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


def workflow_step(job: str, name: str) -> str:
    lines = job.splitlines()
    marker = f"      - name: {name}"
    if lines.count(marker) != 1:
        raise AssertionError(f"workflow step must occur exactly once: {name}")
    start = lines.index(marker) + 1
    end = next(
        (
            index
            for index in range(start, len(lines))
            if lines[index].startswith("      - ")
        ),
        len(lines),
    )
    return "\n".join(lines[start:end])


def step_script(step: str) -> str:
    lines = step.splitlines()
    run_at = next(
        index for index, line in enumerate(lines) if line.startswith("        run: ")
    )
    command = lines[run_at].removeprefix("        run: ")
    if command != "|":
        return command
    body: list[str] = []
    for line in lines[run_at + 1 :]:
        if line.strip() and not line.startswith("          "):
            break
        body.append(line[10:])
    return "\n".join(body)


UNIT_TEST_COMMAND = 'TMPDIR="$tmpdir" PROJMUX_REAL_TMUX_STRICT=1 make test'
UNIT_TEST_MKDIR = 'mkdir -p "$tmpdir"'
UNIT_TEST_ECHO = 'echo "TMPDIR=$tmpdir (${#tmpdir} bytes)"'
UNIT_TEST_TMPDIR_MIN_BYTES = 100
TMUX_INSTALL = "sudo apt-get install --yes tmux"


def unit_test_tmpdir(script: str) -> str:
    """Return the TMPDIR the unit test step script sets, or fail on any drift."""
    lines = script.strip("\n").split("\n")
    if len(lines) != 4:
        raise AssertionError(f"unit test step must be exactly four lines, got {lines!r}")
    match = re.fullmatch(r"tmpdir=(/[^\s\"'$`]+)", lines[0])
    if match is None:
        raise AssertionError(f"unit test step must set an absolute tmpdir, got {lines[0]!r}")
    tmpdir = match.group(1)
    if len(tmpdir.encode()) < UNIT_TEST_TMPDIR_MIN_BYTES:
        raise AssertionError(
            f"unit test TMPDIR must be at least {UNIT_TEST_TMPDIR_MIN_BYTES} bytes,"
            f" got {len(tmpdir.encode())}: {tmpdir!r}"
        )
    expected = [UNIT_TEST_MKDIR, UNIT_TEST_ECHO, UNIT_TEST_COMMAND]
    if lines[1:] != expected:
        raise AssertionError(
            f"unit test step must run exactly {expected!r} after tmpdir=, got {lines[1:]!r}"
        )
    return tmpdir


def assert_job_runs_real_tmux_strict(job: str) -> None:
    """Fail unless a unit test job installs tmux and runs strict unit tests.

    The real-tmux Go tests skip without tmux. The job installs tmux before the
    tests and runs them with PROJMUX_REAL_TMUX_STRICT=1, so a missing tmux
    fails the job instead of counting skipped tests as a pass.

    The tests also run under a created TMPDIR of at least 100 bytes, printed
    with its length. A unix socket path has a platform bound (Linux 108B,
    macOS 104B), and the runner's short default TMPDIR would hide a socket test
    under t.TempDir() that breaks on a longer TMPDIR such as macOS's; at 100
    bytes a socket under t.TempDir() has almost no room left under either.

    The CI Unit Tests job and the Release Unit Tests job both hold this part.
    """
    if re.search(r"(?m)^\s+(?:if|continue-on-error|env):", job):
        raise AssertionError("unit job must not use if:, continue-on-error:, or env:")
    for needle in ("uses: actions/setup-go@", "go-version-file: go.mod"):
        if needle not in job:
            raise AssertionError(f"unit job is missing: {needle}")

    test = workflow_step(job, "Run unit tests")
    unit_test_tmpdir(step_script(test))

    install = workflow_step(job, "Install tmux")
    install_script = step_script(install)
    if TMUX_INSTALL not in install_script.splitlines():
        raise AssertionError(f"Install tmux step must run: {TMUX_INSTALL}")
    if "tmux -V" not in install_script:
        raise AssertionError("Install tmux step must print tmux -V")
    if re.search(r"(?m)^\s+(?:if|continue-on-error):", install):
        raise AssertionError("Install tmux step must not be conditional")

    order = ["go-version-file: go.mod", "      - name: Install tmux", UNIT_TEST_COMMAND]
    positions = [job.index(needle) for needle in order]
    if positions != sorted(positions):
        raise AssertionError(f"unit job steps must run in order: {order}")


def real_tmux_strict_mutations(job: str) -> dict[str, str]:
    """Return drifted copies of a job that assert_job_runs_real_tmux_strict rejects."""
    install_marker = "      - name: Install tmux\n"
    install_block = install_marker + workflow_step(job, "Install tmux") + "\n"
    test_marker = "      - name: Run unit tests\n"
    test_block = test_marker + workflow_step(job, "Run unit tests") + "\n"
    if job.count(install_block) != 1 or job.count(test_block) != 1:
        raise AssertionError("Install tmux and Run unit tests must each occur once")
    tmpdir = unit_test_tmpdir(step_script(workflow_step(job, "Run unit tests")))
    without_install = job.replace(install_block, "")
    return {
        "install step removed": without_install,
        "apt-get install line removed": job.replace(f"          {TMUX_INSTALL}\n", ""),
        "strict prefix stripped": job.replace(
            UNIT_TEST_COMMAND, 'TMPDIR="$tmpdir" make test'
        ),
        "strict moved into env": job.replace(
            test_block,
            test_block.replace(
                "        run: |\n",
                "        env:\n"
                "          PROJMUX_REAL_TMUX_STRICT: \"1\"\n"
                "        run: |\n",
            ).replace(UNIT_TEST_COMMAND, 'TMPDIR="$tmpdir" make test'),
        ),
        "install after test": without_install.replace(
            test_block, test_block + install_block
        ),
        "long TMPDIR dropped": job.replace(
            UNIT_TEST_COMMAND, "PROJMUX_REAL_TMUX_STRICT=1 make test"
        ),
        "short TMPDIR": job.replace(f"tmpdir={tmpdir}\n", "tmpdir=/tmp/projmux-unit\n"),
        # A literal 99 bytes, not the constant, so lowering the floor itself
        # fails here.
        "TMPDIR one byte under the floor": job.replace(
            f"tmpdir={tmpdir}\n", f"tmpdir={tmpdir[:99]}\n"
        ),
        "TMPDIR not created": job.replace(f"          {UNIT_TEST_MKDIR}\n", ""),
        "TMPDIR length not printed": job.replace(f"          {UNIT_TEST_ECHO}\n", ""),
    }


def assert_unit_job_runs_real_tmux_strict(unit: str) -> None:
    """Fail unless the CI Unit Tests job runs strict unit tests between its gates.

    On top of assert_job_runs_real_tmux_strict, the job checks the pinned
    deadcode baseline before the tests and vets after them.
    """
    assert_job_runs_real_tmux_strict(unit)

    deadcode = workflow_step(unit, "Check pinned deadcode baseline")
    if deadcode.strip() != "run: make deadcode":
        raise AssertionError(f"unexpected deadcode step: {deadcode.strip()!r}")
    vet = workflow_step(unit, "Vet")
    if vet.strip() != "run: make vet":
        raise AssertionError(f"unexpected vet step: {vet.strip()!r}")

    order = [
        "go-version-file: go.mod",
        "      - name: Install tmux",
        "run: make deadcode",
        UNIT_TEST_COMMAND,
        "run: make vet",
    ]
    positions = [unit.index(needle) for needle in order]
    if positions != sorted(positions):
        raise AssertionError(f"unit job steps must run in order: {order}")


class CIWorkflowContractTest(unittest.TestCase):
    def test_required_unit_job_runs_pinned_deadcode_without_bypass(self) -> None:
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        assert_unit_job_runs_real_tmux_strict(workflow_job(workflow, "unit"))

        # The repository tool directive and module version pin the scanner;
        # a runner must not install a floating tool or substitute a waiver.
        module = (ROOT / "go.mod").read_text(encoding="utf-8")
        self.assertIn("tool golang.org/x/tools/cmd/deadcode\n", module)
        self.assertRegex(
            module, r"(?m)^\s*golang\.org/x/tools v\d+\.\d+\.\d+(?:\s|$)"
        )
        makefile = (ROOT / "Makefile").read_text(encoding="utf-8")
        self.assertIn("if ! $(GO) tool deadcode ./...", makefile)
        self.assertIn("\nvet:\n\t$(GO) vet ./...\n", makefile)

        for job, name in {
            "fmt": "Format",
            "unit": "Unit Tests",
            "npm-pack": "NPM Packages",
            "integration": "Integration Tests",
            "e2e-tests": "E2E Tests",
            "test": "Test",
        }.items():
            self.assertIn(f"    name: {name}\n", workflow_job(workflow, job))
        aggregate = workflow_job(workflow, "test")
        self.assertIn("    if: always()", aggregate)
        self.assertNotIn("continue-on-error:", aggregate)
        self.assertIn("      - unit\n", aggregate)
        self.assertIn("--required unit ", aggregate)

    def test_unit_job_contract_rejects_a_missing_tmux_install_or_strict_env(
        self,
    ) -> None:
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        unit = workflow_job(workflow, "unit")
        assert_unit_job_runs_real_tmux_strict(unit)

        mutations = real_tmux_strict_mutations(unit)
        for name, mutated in mutations.items():
            with self.subTest(mutation=name):
                self.assertNotEqual(mutated, unit)
                with self.assertRaises(AssertionError):
                    assert_unit_job_runs_real_tmux_strict(mutated)

    def test_unit_test_step_runs_make_test_under_a_created_long_tmpdir(self) -> None:
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        script = step_script(
            workflow_step(workflow_job(workflow, "unit"), "Run unit tests")
        )
        literal = unit_test_tmpdir(script)
        self.assertGreaterEqual(len(literal.encode()), UNIT_TEST_TMPDIR_MIN_BYTES)

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            calls = root / "make-calls"
            bin_dir = root / "bin"
            bin_dir.mkdir()
            make = bin_dir / "make"
            make.write_text(
                '#!/bin/sh\n'
                '{ printf "args=%s\\n" "$*"\n'
                '  printf "tmpdir=%s\\n" "$TMPDIR"\n'
                '  printf "strict=%s\\n" "$PROJMUX_REAL_TMUX_STRICT"\n'
                '  if [ -d "$TMPDIR" ]; then echo exists=yes; else echo exists=no; fi\n'
                '} > "$UNIT_TEST_CALLS"\n',
                encoding="utf-8",
            )
            make.chmod(0o700)
            # Run the real script against a long path of its own so the test
            # leaves nothing under /tmp; the directory must not exist yet.
            long_dir = str(root / "long")
            long_dir += "-" + "x" * max(0, UNIT_TEST_TMPDIR_MIN_BYTES - len(long_dir))
            self.assertGreaterEqual(len(long_dir.encode()), UNIT_TEST_TMPDIR_MIN_BYTES)
            self.assertFalse(os.path.exists(long_dir))
            self.assertEqual(script.count(f"tmpdir={literal}\n"), 1)
            local_script = script.replace(f"tmpdir={literal}\n", f"tmpdir={long_dir}\n")

            result = subprocess.run(
                ["bash", "-e", "-o", "pipefail", "-c", local_script],
                cwd=ROOT,
                check=False,
                capture_output=True,
                text=True,
                env={
                    **{k: v for k, v in os.environ.items() if k != "TMPDIR"},
                    "PATH": str(bin_dir) + os.pathsep + os.environ["PATH"],
                    "UNIT_TEST_CALLS": str(calls),
                },
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(
                f"TMPDIR={long_dir} ({len(long_dir.encode())} bytes)", result.stdout
            )
            self.assertEqual(
                calls.read_text(encoding="utf-8"),
                f"args=test\ntmpdir={long_dir}\nstrict=1\nexists=yes\n",
            )

    def test_deadcode_failure_fails_unit_and_test_aggregate(self) -> None:
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        unit_script = step_script(
            workflow_step(workflow_job(workflow, "unit"), "Check pinned deadcode baseline")
        )
        aggregate = workflow_job(workflow, "test")
        aggregate_script = step_script(
            workflow_step(aggregate, "Require every child to succeed")
        )
        children = re.findall(r"^      - ([\w-]+)$", aggregate, re.MULTILINE)
        self.assertIn("unit", children)
        self.assertEqual(
            set(children), set(re.findall(r"--required ([\w-]+)", aggregate_script))
        )

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            calls = root / "make-calls"
            make = root / "make"
            make.write_text(
                '#!/bin/sh\nprintf "%s\\n" "$*" >> "$DEADCODE_TEST_CALLS"\n'
                'exit "$DEADCODE_TEST_STATUS"\n',
                encoding="utf-8",
            )
            make.chmod(0o700)
            for status in (0, 42):
                with self.subTest(deadcode_exit=status):
                    calls.write_text("", encoding="utf-8")
                    unit_result = subprocess.run(
                        ["bash", "-e", "-o", "pipefail", "-c", unit_script],
                        cwd=ROOT,
                        check=False,
                        capture_output=True,
                        text=True,
                        env={
                            **os.environ,
                            "PATH": str(root) + os.pathsep + os.environ["PATH"],
                            "DEADCODE_TEST_CALLS": str(calls),
                            "DEADCODE_TEST_STATUS": str(status),
                        },
                    )
                    self.assertEqual(calls.read_text(encoding="utf-8"), "deadcode\n")
                    self.assertEqual(unit_result.returncode, status, unit_result.stderr)
                    outcomes = (
                        ("success",) if status == 0 else ("failure", "skipped", "cancelled")
                    )
                    for outcome in outcomes:
                        results = {
                            child: {"result": outcome if child == "unit" else "success"}
                            for child in children
                        }
                        completed = subprocess.run(
                            ["bash", "-e", "-o", "pipefail", "-c", aggregate_script],
                            cwd=ROOT,
                            check=False,
                            capture_output=True,
                            text=True,
                            env={**os.environ, "REQUIRED_RESULTS": json.dumps(results)},
                        )
                        self.assertEqual(
                            completed.returncode,
                            0 if outcome == "success" else 1,
                            completed.stderr,
                        )
                        if outcome != "success":
                            self.assertIn(f"unit={outcome}", completed.stderr)

    def test_security_contract_gate_runs_behind_the_stable_aggregate(self) -> None:
        # The contract gate pins the reviewed security baselines. Until this job
        # existed no CI job referenced `make security-contract`, so the gate
        # failed silently for four days while PRs merged past it.
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        contract = workflow_job(workflow, "security-contract")
        self.assertIn("    name: Security / Contract gate\n", contract)
        self.assertNotIn("continue-on-error:", contract)
        self.assertNotRegex(contract, r"(?m)^    if:")
        self.assertEqual(
            step_script(workflow_step(contract, "Run the security contract gate")).strip(),
            "make security-contract",
        )
        # The gate reinstalls the pinned scanners into a throwaway directory, so
        # it needs the same restore cache and toolchain the scanner jobs use.
        self.assertIn("uses: actions/setup-go@", contract)
        self.assertIn("go-version-file: go.mod", contract)
        self.assertIn(
            "hashFiles('.security/security-tools.versions')", contract
        )
        self.assertEqual(
            step_script(workflow_step(contract, "Verify pinned security tools")).strip(),
            "make security-tools",
        )
        # Only one job may write the shared tool cache; this one restores only.
        self.assertNotIn("uses: actions/cache/save@", contract)

        aggregate = workflow_job(workflow, "test")
        self.assertIn("      - security-contract\n", aggregate)
        self.assertIn("--required security-contract ", aggregate)

        # Required display names are owned by the `main-protect` ruleset. The
        # new job is an addition, never a rename or a split of those five.
        for job, name in {
            "fmt": "Format",
            "unit": "Unit Tests",
            "npm-pack": "NPM Packages",
            "integration": "Integration Tests",
            "e2e-tests": "E2E Tests",
        }.items():
            self.assertIn(f"    name: {name}\n", workflow_job(workflow, job))
            self.assertNotEqual(job, "security-contract")

    def test_security_contract_failure_makes_the_stable_aggregate_red(self) -> None:
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        aggregate = workflow_job(workflow, "test")
        aggregate_script = step_script(
            workflow_step(aggregate, "Require every child to succeed")
        )
        children = re.findall(r"^      - ([\w-]+)$", aggregate, re.MULTILINE)
        self.assertIn("security-contract", children)
        for outcome in ("failure", "skipped", "cancelled"):
            with self.subTest(security_contract=outcome):
                results = {
                    child: {
                        "result": outcome if child == "security-contract" else "success"
                    }
                    for child in children
                }
                completed = subprocess.run(
                    ["bash", "-e", "-o", "pipefail", "-c", aggregate_script],
                    cwd=ROOT,
                    check=False,
                    capture_output=True,
                    text=True,
                    env={**os.environ, "REQUIRED_RESULTS": json.dumps(results)},
                )
                self.assertEqual(completed.returncode, 1, completed.stderr)
                self.assertIn(f"security-contract={outcome}", completed.stderr)

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

    def test_update_flow_runs_on_update_path_prs_and_schedule_outside_required_gates(
        self,
    ) -> None:
        ci_workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        workflow = (ROOT / ".github/workflows/update-flow.yml").read_text(
            encoding="utf-8"
        )

        # Triggers: update-path pull requests, a daily schedule, and dispatch.
        # There is no push trigger; the lane never runs on main pushes.
        self.assertIn("  pull_request:\n    paths:\n", workflow)
        lines = workflow.splitlines()
        start = lines.index("    paths:") + 1
        paths: list[str] = []
        for line in lines[start:]:
            if not line.startswith("      - "):
                break
            paths.append(line.removeprefix("      - "))
        self.assertEqual(
            paths,
            [
                '".github/workflows/update-flow.yml"',
                '"test/e2e/update-flow.sh"',
                '"scripts/test-e2e-update-docker.sh"',
                '"scripts/test-docker-run.sh"',
                '"test/docker/Dockerfile.node"',
                '"internal/app/update.go"',
                '"internal/app/update_channel.go"',
                '"internal/app/tmux.go"',
                '"internal/app/runtime_mutation_route.go"',
            ],
        )
        self.assertIn('    - cron: "41 4 * * *"', workflow)
        self.assertIn("  workflow_dispatch:", workflow)
        self.assertNotRegex(workflow, r"(?m)^  push:")
        self.assertIn("permissions:\n  contents: read\n", workflow)

        job = workflow_job(workflow, "update-flow")
        self.assertIn("    timeout-minutes: 30\n", job)
        self.assertIn("    runs-on: ubuntu-latest\n", job)
        self.assertEqual(
            step_script(workflow_step(job, "Run the npm update-flow e2e")),
            "make test-e2e-update",
        )

        # The registry-dependent lane stays outside the required checks and
        # the aggregate `Test`, whose required display names stay intact.
        self.assertNotIn("update-flow", ci_workflow)
        self.assertNotIn("test-e2e-update", ci_workflow)
        for stable_aggregate in (
            workflow_job(ci_workflow, "e2e-tests"),
            workflow_job(ci_workflow, "test"),
        ):
            self.assertNotIn("update-flow", stable_aggregate)
            self.assertNotIn("test-e2e-update", stable_aggregate)
        for name in (
            "Format",
            "Unit Tests",
            "NPM Packages",
            "Integration Tests",
            "E2E Tests",
        ):
            self.assertIn(f"    name: {name}\n", ci_workflow)

        base_env = {
            key: value
            for key, value in os.environ.items()
            if key not in {"CI", "GITHUB_ACTIONS", "PROJMUX_UPDATE_FLOW_STRICT"}
        }
        with tempfile.TemporaryDirectory() as temporary:
            temporary_path = Path(temporary)

            # A skip passes locally and fails under --strict.
            stubs = temporary_path / "bin"
            stubs.mkdir()
            for name, body in {
                "node": 'echo "v20.0.0"\n',
                "npm": 'if [ "$1" = "--version" ]; then echo "10.0.0"; exit 0; fi\n'
                "exit 1\n",
                "go": "exit 0\n",
            }.items():
                stub = stubs / name
                stub.write_text("#!/bin/sh\n" + body, encoding="utf-8")
                stub.chmod(0o700)
            suite_env = {
                **base_env,
                "PATH": f"{stubs}:/usr/bin:/bin",
                "HOME": str(temporary_path / "home"),
            }
            suite = str(ROOT / "test/e2e/update-flow.sh")
            for args, code, message in (
                ([], 0, "update-flow e2e SKIP: npm registry unreachable"),
                (
                    ["--strict"],
                    1,
                    "update-flow e2e: SKIP is a failure under --strict: "
                    "npm registry unreachable",
                ),
                (["--bogus"], 2, "usage: test/e2e/update-flow.sh [--strict]"),
            ):
                with self.subTest(suite_args=args):
                    completed = subprocess.run(
                        ["bash", suite, *args],
                        check=False,
                        capture_output=True,
                        text=True,
                        env=suite_env,
                    )
                    self.assertEqual(completed.returncode, code, completed.stderr)
                    self.assertIn(message, completed.stderr)

            # The host wrapper maps CI=true (or the explicit opt-in) to --strict.
            wrapper_root = temporary_path / "root"
            (wrapper_root / "scripts").mkdir(parents=True)
            wrapper = wrapper_root / "scripts/test-e2e-update-docker.sh"
            shutil.copy2(ROOT / "scripts/test-e2e-update-docker.sh", wrapper)
            runner = wrapper_root / "scripts/test-docker-run.sh"
            runner.write_text(
                '#!/usr/bin/env bash\nprintf "%s\\n" "$@"\n', encoding="utf-8"
            )
            runner.chmod(0o700)
            for extra, expected in (
                ({}, ["test/e2e/update-flow.sh"]),
                ({"CI": "true"}, ["test/e2e/update-flow.sh", "--strict"]),
                (
                    {"PROJMUX_UPDATE_FLOW_STRICT": "1"},
                    ["test/e2e/update-flow.sh", "--strict"],
                ),
            ):
                with self.subTest(wrapper_env=extra):
                    completed = subprocess.run(
                        ["bash", str(wrapper)],
                        check=False,
                        capture_output=True,
                        text=True,
                        env={**base_env, **extra},
                    )
                    self.assertEqual(completed.returncode, 0, completed.stderr)
                    self.assertEqual(completed.stdout.splitlines(), expected)

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


DOCKER_INVOCATION_END = "--projmux-fake-docker-invocation-end--"
SUITE_RUNNER_STRIPPED_ENV = (
    "PROJMUX_TEST_DOCKER_NETWORK",
    "GOMAXPROCS",
    "GOFLAGS",
    "PROJMUX_TEST_PREBUILT_BIN",
    "PROJMUX_TEST_PREBUILT_SHA256",
    "XDG_CACHE_HOME",
    "PROJMUX_TEST_GOCACHE",
    "PROJMUX_TEST_GOMODCACHE",
    "PROJMUX_TEST_SKIP_PREFETCH",
    "PROJMUX_TEST_BASH_TRACE",
    "PROJMUX_TEST_IMAGE",
    "PROJMUX_E2E_ARTIFACTS",
    "GOCACHE",
)
GOCACHE_ASSIGNMENT = re.compile(r"(?<![\w${])GOCACHE=")


def docker_run_options(argv: list[str]) -> dict[str, object]:
    if not argv or argv[0] != "run":
        raise AssertionError(f"not a docker run invocation: {argv}")
    valued = {
        "--network": "network",
        "--user": "user",
        "-e": "env",
        "-v": "volume",
        "-w": "workdir",
    }
    parsed: dict[str, list[str]] = {name: [] for name in valued.values()}
    index = 1
    while index < len(argv) and argv[index].startswith("-"):
        option = argv[index]
        if option == "--rm":
            index += 1
            continue
        if option not in valued or index + 1 >= len(argv):
            raise AssertionError(f"unexpected docker run option {option!r} in {argv}")
        parsed[valued[option]].append(argv[index + 1])
        index += 2
    env: dict[str, list[str]] = {}
    for pair in parsed["env"]:
        name, _, value = pair.partition("=")
        env.setdefault(name, []).append(value)
    volumes = []
    for spec in parsed["volume"]:
        host, container, mode = spec.rsplit(":", 2)
        volumes.append((host, container, mode))
    return {
        "network": parsed["network"],
        "env": env,
        "volumes": volumes,
        "command": argv[index:],
    }


def shell_word(text: str) -> str:
    if text.startswith('"'):
        end = 1
        while end < len(text) and text[end] != '"':
            end += 2 if text[end] == "\\" else 1
        return text[1:end]
    if text.startswith("'"):
        return text[1 : text.find("'", 1)]
    return re.split(r"[\s;&|]", text, maxsplit=1)[0]


def inherits_gocache(value: str) -> bool:
    if not value.startswith("${GOCACHE:-") or not value.endswith("}"):
        return False
    depth = 0
    for index, char in enumerate(value):
        if char == "{":
            depth += 1
        elif char == "}":
            depth -= 1
            if depth == 0:
                return index == len(value) - 1
    return False


def gocache_overwrites(text: str) -> list[tuple[int, str]]:
    lines = text.splitlines()

    def assignments(name: str) -> list[tuple[int, str]]:
        pattern = re.compile(rf"(?<![\w${{]){re.escape(name)}=")
        found = []
        for number, line in enumerate(lines, start=1):
            if line.lstrip().startswith("#"):
                continue
            for match in pattern.finditer(line):
                found.append((number, shell_word(line[match.end() :])))
        return found

    offending = []
    for number, value in assignments("GOCACHE"):
        if inherits_gocache(value):
            continue
        indirect = re.fullmatch(r"\$(\w+)|\$\{(\w+)\}", value)
        if indirect:
            sources = assignments(indirect.group(1) or indirect.group(2))
            if sources and all(inherits_gocache(source) for _, source in sources):
                continue
        offending.append((number, lines[number - 1].strip()))
    return offending


class SuiteContainerBuildCacheContractTest(unittest.TestCase):
    suite = "test/integration/build-cache-contract-suite.sh"

    def run_suite_runner(
        self, temporary: Path, overrides: dict[str, str]
    ) -> list[list[str]]:
        fake_bin = temporary / "bin"
        fake_bin.mkdir()
        calls = temporary / "docker-calls"
        docker = fake_bin / "docker"
        docker.write_text(
            '#!/bin/sh\nprintf "%s\\n" "$@" >> "$FAKE_DOCKER_CALLS"\n'
            f'printf "%s\\n" "{DOCKER_INVOCATION_END}" >> "$FAKE_DOCKER_CALLS"\n'
            "exit 0\n",
            encoding="utf-8",
        )
        docker.chmod(0o700)
        home = temporary / "home"
        home.mkdir()
        env = {
            key: value
            for key, value in os.environ.items()
            if key not in SUITE_RUNNER_STRIPPED_ENV
        }
        env.update(
            {
                "PATH": str(fake_bin) + os.pathsep + os.environ["PATH"],
                "HOME": str(home),
                "FAKE_DOCKER_CALLS": str(calls),
                "PROJMUX_TEST_SKIP_IMAGE_BUILD": "1",
                "PROJMUX_E2E_ARTIFACTS": str(temporary / "evidence"),
            }
        )
        env.update(overrides)
        completed = subprocess.run(
            ["bash", str(ROOT / "scripts/test-docker-run.sh"), self.suite],
            cwd=ROOT,
            check=False,
            capture_output=True,
            text=True,
            env=env,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        invocations: list[list[str]] = [[]]
        for line in calls.read_text(encoding="utf-8").splitlines():
            if line == DOCKER_INVOCATION_END:
                invocations.append([])
            else:
                invocations[-1].append(line)
        self.assertEqual(invocations.pop(), [])
        return invocations

    def suite_invocation(self, invocations: list[list[str]]) -> dict[str, object]:
        suites = [argv for argv in invocations if argv and argv[-1] == self.suite]
        self.assertEqual(len(suites), 1, invocations)
        parsed = docker_run_options(suites[0])
        self.assertEqual(parsed["command"][-2:], ["bash", self.suite])
        return parsed

    def prepared_caches(self, temporary: Path) -> dict[str, str]:
        modcache = temporary / "gomodcache"
        modcache.mkdir()
        shutil.copyfile(ROOT / "go.sum", modcache / ".projmux-go-sum")
        return {
            "PROJMUX_TEST_GOMODCACHE": str(modcache),
            "PROJMUX_TEST_GOCACHE": str(temporary / "gocache"),
        }

    def test_suite_run_mounts_the_runner_build_cache_at_gocache(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory).resolve()
            caches = self.prepared_caches(temporary)
            invocations = self.run_suite_runner(temporary, caches)
            self.assertEqual(
                len(invocations), 1, "a stamped module cache must not prefetch"
            )
            suite = self.suite_invocation(invocations)
            self.assertEqual(suite["env"].get("GOCACHE"), ["/gocache"])
            build_mounts = [
                volume for volume in suite["volumes"] if volume[1] == "/gocache"
            ]
            self.assertEqual(
                build_mounts, [(caches["PROJMUX_TEST_GOCACHE"], "/gocache", "rw")]
            )
            self.assertTrue(Path(caches["PROJMUX_TEST_GOCACHE"]).is_dir())
            for argv in invocations:
                for argument in argv:
                    self.assertNotIn("/tmp/projmux-gocache", argument)

    def test_suite_run_defaults_the_build_cache_to_the_prefetch_location(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory).resolve()
            invocations = self.run_suite_runner(temporary, {})
            default = str(temporary / "home/.cache/projmux/test-gocache")
            suite = self.suite_invocation(invocations)
            self.assertEqual(suite["env"].get("GOCACHE"), ["/gocache"])
            self.assertEqual(
                [volume for volume in suite["volumes"] if volume[1] == "/gocache"],
                [(default, "/gocache", "rw")],
            )
            prefetch = [
                argv for argv in invocations if argv[-3:] == ["go", "mod", "download"]
            ]
            self.assertEqual(len(prefetch), 1, invocations)
            self.assertEqual(
                [
                    volume
                    for volume in docker_run_options(prefetch[0])["volumes"]
                    if volume[1] == "/gocache"
                ],
                [(default, "/gocache", "rw")],
            )

    def test_suite_run_keeps_network_workspace_and_home_isolation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory).resolve()
            caches = self.prepared_caches(temporary)
            suite = self.suite_invocation(self.run_suite_runner(temporary, caches))
            self.assertEqual(suite["network"], ["none"])
            volumes = suite["volumes"]
            self.assertIn((str(ROOT), "/workspace", "ro"), volumes)
            self.assertEqual(
                [volume for volume in volumes if volume[1] == "/workspace"],
                [(str(ROOT), "/workspace", "ro")],
            )
            isolated = {
                "HOME": "/tmp/projmux-home",
                "XDG_CACHE_HOME": "/tmp/projmux-cache",
                "XDG_CONFIG_HOME": "/tmp/projmux-config",
                "XDG_RUNTIME_DIR": "/tmp/projmux-runtime",
                "XDG_STATE_HOME": "/tmp/projmux-state",
            }
            for name, value in isolated.items():
                self.assertEqual(suite["env"].get(name), [value], name)
            self.assertEqual(
                {container for _, container, mode in volumes if mode == "rw"},
                {"/evidence", "/gocache"},
            )
            self.assertIn((str(temporary / "evidence"), "/evidence", "rw"), volumes)
            self.assertEqual(
                [volume for volume in volumes if volume[1] == "/gomodcache"],
                [(caches["PROJMUX_TEST_GOMODCACHE"], "/gomodcache", "ro")],
            )
            build_cache = Path("/gocache")
            for protected in (*isolated.values(), "/workspace"):
                protected_path = Path(protected)
                self.assertNotEqual(build_cache, protected_path)
                self.assertNotIn(protected_path, build_cache.parents)
                self.assertNotIn(build_cache, protected_path.parents)

    def test_smoke_env_honors_an_inherited_build_cache(self) -> None:
        script = (
            'set -euo pipefail\nsource "$1"\nsmoke_setup_env\n'
            'printf "%s\\n%s\\n" "$GOCACHE" "$PROJMUX_SMOKE_WORKDIR"\n'
        )
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory).resolve()
            inherited = temporary / "inherited-gocache"
            env = {
                key: value
                for key, value in os.environ.items()
                if key not in ("GOCACHE", "PROJMUX_E2E_ARTIFACTS", "PROJMUX_E2E_SUITE")
            }
            env["TMPDIR"] = str(temporary)
            cases = (("inherited", {"GOCACHE": str(inherited)}), ("unset", {}))
            smoke = str(ROOT / "test/lib/smoke.sh")
            for label, overrides in cases:
                with self.subTest(gocache=label):
                    completed = subprocess.run(
                        ["bash", "-c", script, "smoke-env", smoke],
                        cwd=ROOT,
                        check=False,
                        capture_output=True,
                        text=True,
                        env={**env, **overrides},
                    )
                    self.assertEqual(completed.returncode, 0, completed.stderr)
                    gocache, workdir = completed.stdout.splitlines()
                    self.assertEqual(Path(workdir).parent, temporary)
                    if overrides:
                        self.assertEqual(gocache, str(inherited))
                        self.assertTrue(inherited.is_dir())
                        self.assertFalse((Path(workdir) / "go-cache").exists())
                    else:
                        self.assertEqual(gocache, f"{workdir}/go-cache")
                        self.assertTrue(Path(gocache).is_dir())

    def test_no_suite_script_overwrites_an_inherited_build_cache(self) -> None:
        self.assertEqual(
            gocache_overwrites('export GOCACHE="$X/go-cache"\n'),
            [(1, 'export GOCACHE="$X/go-cache"')],
        )
        self.assertEqual(
            gocache_overwrites('cache="$X"\nGOCACHE="$cache" make build\n'),
            [(2, 'GOCACHE="$cache" make build')],
        )
        self.assertEqual(
            gocache_overwrites('export GOCACHE="${GOCACHE:-${X}/go-cache}"\n'), []
        )
        self.assertEqual(
            gocache_overwrites(
                'cache="${GOCACHE:-$X/go-cache}"\nGOCACHE="$cache" make build\n'
            ),
            [],
        )

        smoke = ROOT / "test/lib/smoke.sh"
        scripts = [smoke, *sorted((ROOT / "test/integration").glob("*.sh"))]
        self.assertTrue(
            GOCACHE_ASSIGNMENT.search(smoke.read_text(encoding="utf-8")),
            "the scan must see the smoke lib build cache assignment",
        )
        offending = [
            f"{script.relative_to(ROOT)}:{number}: {line}"
            for script in scripts
            for number, line in gocache_overwrites(script.read_text(encoding="utf-8"))
        ]
        self.assertEqual(
            offending, [], "GOCACHE assignments must keep an inherited cache"
        )


if __name__ == "__main__":
    unittest.main()
