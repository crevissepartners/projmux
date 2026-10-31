#!/usr/bin/env python3
"""Run process fixtures against a fresh copied CLI, refusing missing evidence."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
FILES = (
    "agent_lifecycle_guidance_cli_test.go",
    "agent_relaunch_profile_digest_cli_test.go",
    "agent_relaunch_host_lost_cli_test.go",
    "process_detached_create_cli_test.go",
    "process_detached_resume_cli_test.go",
    "sdk_resume_process_cli_test.go",
    "sdk_relaunch_process_cli_test.go",
    "sdk_labels_kept_test.go",
    "sdk_reserve_agent_cli_test.go",
    "sdk_start_reserved_agent_cli_test.go",
    "process_codex_host_upgrade_cli_test.go",
    "process_detached_relaunch_cli_test.go",
    "project_stop_process_cli_test.go",
    "virtual_window_lifecycle_cli_test.go",
    "process_agent_question_cli_test.go",
    "process_codex_steer_cli_test.go",
    "agent_relaunch_deferred_test.go",
    "agent_relaunch_process_claude_test.go",
    "agent_relaunch_host_test.go",
    "agent_relaunch_owned_test.go",
    "agent_relaunch_settings_owned_test.go",
    "agent_relaunch_host_codex_test.go",
    "agent_relaunch_process_codex_test.go",
    "process_agent_session_cli_test.go",
    "create_agent_process_codex_cli_test.go",
    "process_agent_resume_cli_test.go",
    "create_agent_process_cli_test.go",
    "sdk_create_operator_creator_cli_test.go",
    "sdk_preflight_create_agent_process_cli_test.go",
    "delete_agent_process_cli_test.go",
    "codex_process_binding_test.go",
    "codex_process_ownership_test.go",
    "process_attention_wiring_test.go",
    "process_host_supervisor_test.go",
)

# This real-tmux test has its own unit/integration gate and opt-in variable.
EXCLUDED_FILES = {"internal/app/cli_window_rename_display_real_tmux_test.go"}


def check_fixture_files(root: Path) -> None:
    discovered = {str(path.relative_to(root))
                  for path in (root / "internal").rglob("*_test.go")
                  if '"PMX_TEST_CLI"' in path.read_text()}
    expected = {"internal/app/" + filename for filename in FILES}
    actual = discovered - EXCLUDED_FILES
    if actual != expected:
        raise ValueError("PMX_TEST_CLI file inventory changed: "
                         f"unlisted={sorted(actual - expected)}, "
                         f"no longer consuming={sorted(expected - actual)}")


def selected_tests(root: Path) -> list[str]:
    check_fixture_files(root)
    tests = []
    for filename in FILES:
        source = (root / "internal/app" / filename).read_text()
        names = re.findall(r"(?m)^func[ \t]+(Test[A-Z0-9_]\w*)\s*\(\s*\w+\s+\*testing\.T\s*,?\s*\)", source)
        if not names:
            raise ValueError(f"no tests selected from {filename}")
        tests.extend(names)
    if len(set(tests)) != len(tests):
        raise ValueError("duplicate selected test names")
    return sorted(tests)


def check_results(events: list[dict], expected: list[str], returncode: int) -> dict:
    passed = {e.get("Test") for e in events if e.get("Action") == "pass"}
    skipped = [e.get("Test", "package") for e in events if e.get("Action") == "skip"]
    failed = [e.get("Test", "package") for e in events if e.get("Action") == "fail"]
    missing = sorted(set(expected) - passed)
    packages = [e for e in events if "Test" not in e and e.get("Action") == "pass"]
    report = {"selected": len(expected), "passed": len(set(expected) & passed),
              "skip": skipped, "fail": failed, "missing": missing}
    if not expected or returncode or skipped or failed or missing or not packages:
        raise ValueError(f"process CLI evidence rejected: {json.dumps(report)}; exit={returncode}")
    return report


class BatchCleanupError(RuntimeError):
    """Refuse another batch when its predecessor's group is not retired."""

    def __init__(self, message: str, expired: subprocess.TimeoutExpired | None = None):
        super().__init__(message)
        self.expired = expired


def group_exists(group: int) -> bool:
    try:
        os.killpg(group, 0)
    except ProcessLookupError:
        return False
    return True


def retire_batch(process: subprocess.Popen) -> tuple[dict, KeyboardInterrupt | None]:
    """Retire only this child's new group, without extending cleanup on Ctrl-C."""
    group = process.pid
    if group <= 1 or group == os.getpgrp():
        raise BatchCleanupError("refusing unowned process group")
    started = time.monotonic()
    interrupted = None
    signals, errors = [], []
    clear = False

    def remember_interrupt(signum=None, frame=None):
        nonlocal interrupted
        if interrupted is None:
            interrupted = KeyboardInterrupt()

    previous = signal.getsignal(signal.SIGINT)
    signal.signal(signal.SIGINT, remember_interrupt)
    try:
        for index, sig in enumerate((signal.SIGTERM, signal.SIGKILL), 1):
            deadline = started + index * 5
            signaled = False
            while time.monotonic() < deadline:
                try:
                    if process.returncode is not None and not group_exists(group):
                        clear = True
                        break
                    if not signaled:
                        try:
                            os.killpg(group, sig)
                            signals.append(sig.name)
                        except ProcessLookupError:
                            pass
                        signaled = True
                    try:
                        process.communicate(timeout=max(0.001, deadline - time.monotonic()))
                    except subprocess.TimeoutExpired:
                        break
                    if process.returncode is not None and not group_exists(group):
                        clear = True
                        break
                    time.sleep(min(0.05, max(0, deadline - time.monotonic())))
                except KeyboardInterrupt as exc:
                    # Also handles synchronous injected exceptions; real SIGINT
                    # is recorded by the temporary handler instead of unwinding.
                    if interrupted is None:
                        interrupted = exc
                except OSError as exc:
                    errors.append(str(exc))
                    break
            if clear:
                break
        if not clear:
            try:
                clear = process.returncode is not None and not group_exists(group)
            except OSError as exc:
                errors.append(str(exc))
        receipt = {"pid": process.pid, "group": group, "wait_returncode": process.returncode,
                   "group_clear": clear, "signals": signals, "errors": errors,
                   "cleanup_seconds": round(time.monotonic() - started, 3)}
        print("process CLI batch cleanup: " + json.dumps(receipt), flush=True)
        return receipt, interrupted
    finally:
        signal.signal(signal.SIGINT, previous)


def run_batch(command: list[str], *, timeout: int, **kwargs) -> subprocess.CompletedProcess:
    # setsid happens before exec; this Popen owns exactly the new pid/pgid.
    process = subprocess.Popen(command, start_new_session=True, **kwargs)
    original, traceback, cleanup_error, interrupted = None, None, None, None
    stdout, stderr = None, None
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except BaseException as exc:
        original, traceback = exc, exc.__traceback__
    finally:
        try:
            receipt, interrupted = retire_batch(process)
            if not receipt["group_clear"]:
                cleanup_error = BatchCleanupError("process CLI batch cleanup unresolved: " + json.dumps(receipt),
                    original if isinstance(original, subprocess.TimeoutExpired) else None)
        except Exception as exc:
            cleanup_error = BatchCleanupError(f"process CLI batch cleanup uncertain: pid={process.pid}: {exc}",
                original if isinstance(original, subprocess.TimeoutExpired) else None)
    # An initial cancellation wins over a later cleanup interruption/failure.
    # A first cancellation during cleanup must not turn into a successful batch.
    if original is not None and not isinstance(original, subprocess.TimeoutExpired):
        if cleanup_error is not None:
            raise original.with_traceback(traceback) from cleanup_error
        raise original.with_traceback(traceback)
    if interrupted is not None:
        if cleanup_error is not None:
            raise interrupted from cleanup_error
        raise interrupted
    if cleanup_error is not None:
        raise cleanup_error
    if original is not None:
        raise original.with_traceback(traceback)
    return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)



def strict_report(events: list[dict], expected: list[str], returncode: int) -> dict:
    # Subtests may repeat run/pass events; only top-level names count toward
    # inventory, while any subtest skip/failure still rejects the whole run.
    runs, passes = {}, {}
    for event in events:
        name = event.get("Test")
        if not isinstance(name, str) or "/" in name:
            continue
        counts = runs if event.get("Action") == "run" else passes if event.get("Action") == "pass" else None
        if counts is not None:
            counts[name] = counts.get(name, 0) + 1
    names = set(expected)
    return {"selected": len(expected), "passed": len(names & passes.keys()),
            "skip": [e.get("Test", "package") for e in events if e.get("Action") == "skip"],
            "fail": [e.get("Test", "package") for e in events if e.get("Action") == "fail"],
            "missing": sorted(names - passes.keys()),
            "missing_run": sorted(names - runs.keys()),
            "duplicate": sorted(n for n in runs.keys() | passes.keys()
                                if runs.get(n, 0) > 1 or passes.get(n, 0) > 1),
            "unexpected": sorted((runs.keys() | passes.keys()) - names),
            "exit": returncode}


def rejected(report: dict) -> bool:
    return bool(not report["selected"] or report["exit"] or any(
        report[key] for key in ("skip", "fail", "missing", "missing_run", "duplicate", "unexpected")))


def timeout_events(partial: bytes | str | None, expected: list[str]) -> list[dict]:
    if isinstance(partial, bytes):
        partial = partial[-65536:].decode(errors="replace")
    else:
        partial = (partial or "")[-65536:]
    observed = []
    for line in partial.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(event, dict) or not isinstance(event.get("Test"), str):
            continue
        name, action = event["Test"].split("/", 1)[0], event.get("Action")
        if name in expected and isinstance(action, str) and action in {"run", "pass", "fail", "skip"}:
            observed.append({"test": name, "action": action})
    return observed[-16:]


def write_summary(report: dict, failed: bool) -> None:
    lines = ["### Process Host CLI — " + ("FAIL" if failed else "PASS"),
             "", "75% budget warning threshold (315s of each 420s Go limit).",
             "", "| Batch | elapsed (s) | limit (s) | utilization | headroom (s) | evidence |",
             "| --- | --- | --- | --- | --- | --- |"]
    for batch in report["batches"]:
        status = "FAIL" if batch["rejected"] else "PASS"
        if batch["budget_warning"]:
            status += " WARNING: over 75%"
        lines.append(f"| {batch['batch']} | {batch['test_command_seconds']} | 420 | "
                     f"{batch['utilization_percent']}% | {batch['headroom_seconds']} | {status} |")
    lines += ["", f"Total elapsed: {report['elapsed_seconds']}s; inventory: {report['selected']}; "
              f"passed: {report['passed']}; skip: {report['skip']}; fail: {report['fail']}; "
              f"missing: {report['missing']}; missing RUN: {report['missing_run']}; "
              f"duplicate: {report['duplicate']}; unexpected: {report['unexpected']}.", ""]
    destination = os.environ.get("GITHUB_STEP_SUMMARY")
    if destination:
        with open(destination, "a", encoding="utf-8") as summary:
            summary.write("\n".join(lines))


def isolated_env(root: Path, go: str) -> dict[str, str]:
    # Resolve cache paths before changing HOME; local runs reuse the caller's
    # cache and CI reuses setup-go's cache without changing provider homes.
    caches = json.loads(subprocess.check_output(
        [go, "env", "-json", "GOMODCACHE", "GOCACHE"], cwd=ROOT, text=True,
        env={**os.environ, "GOTOOLCHAIN": "local"}))
    env = {k: v for k, v in os.environ.items()
           if k not in {"HOME", "TMUX", "TMUX_PANE", "TMUX_TMPDIR", "CODEX_HOME",
                        "CLAUDE_CONFIG_DIR", "OPENAI_API_KEY", "CODEX_API_KEY",
                        "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
           and not k.startswith(("PROJMUX_", "__PROJMUX_", "PMX_", "XDG_",
                                 "PROCESSHOST_TEST_", "ANTHROPIC_", "OPENAI_"))}
    env.update(caches)
    env.update(HOME=str(root / "home"), CODEX_HOME=str(root / "home/.codex"),
               CLAUDE_CONFIG_DIR=str(root / "home/.claude"),
               XDG_CONFIG_HOME=str(root / "config"), XDG_STATE_HOME=str(root / "state"),
               XDG_CACHE_HOME=str(root / "cache"), TMUX_TMPDIR=str(root / "tmux"),
               TMPDIR=str(root.parent), GOTOOLCHAIN="local", GOMAXPROCS="1")
    for key in ("HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
                "TMUX_TMPDIR", "TMPDIR", "CODEX_HOME", "CLAUDE_CONFIG_DIR"):
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    return env


def main() -> None:
    started = time.monotonic()
    expected = selected_tests(ROOT)
    go = os.environ.get("GO", "go")
    # Honor the caller's private temp domain. Each CLI fixture allocates its
    # own directory there; avoid nesting TMPDIR beyond Unix socket bounds.
    with tempfile.TemporaryDirectory(prefix="pc-") as temporary:
        root = Path(temporary)
        env = isolated_env(root, go)
        built, copy = root / "built-projmux", root / "projmux"
        build_started = time.monotonic()
        subprocess.run([go, "build", "-buildvcs=false", "-o", str(built), "./cmd/projmux"],
                       cwd=ROOT, env=env, check=True, timeout=180)
        shutil.copy2(built, copy)
        build_seconds = time.monotonic() - build_started
        env["PMX_TEST_CLI"] = str(copy)
        regression_copy = root / "regression-projmux"
        regression_started = time.monotonic()
        subprocess.run([go, "build", "-buildvcs=false", "-tags=projmux_process_cli_regression",
                        "-o", str(regression_copy), "./cmd/projmux"],
                       cwd=ROOT, env=env, check=True, timeout=180)
        regression_build_seconds = time.monotonic() - regression_started
        env["PMX_TEST_CLAUDE_REGRESSION_CLI"] = str(regression_copy)
        print(f"tagged regression CLI sha256={hashlib.sha256(regression_copy.read_bytes()).hexdigest()}", flush=True)
        print(f"copied CLI sha256={hashlib.sha256(copy.read_bytes()).hexdigest()}", flush=True)
        test_binary = root / "app.test"
        compile_started = time.monotonic()
        print("process CLI precompile started", flush=True)
        subprocess.run([go, "test", "-c", "-o", str(test_binary), "./internal/app"],
                       cwd=ROOT, env=env, check=True, stdout=subprocess.PIPE,
                       stderr=subprocess.PIPE, timeout=120)
        compile_seconds = time.monotonic() - compile_started
        print("process CLI precompile: " + json.dumps({
            "seconds": round(compile_seconds, 3),
            "test_binary_sha256": hashlib.sha256(test_binary.read_bytes()).hexdigest()}), flush=True)
        batches = [expected[::2], expected[1::2]]
        flattened = [name for batch in batches for name in batch]
        if len(flattened) != len(set(flattened)) or sorted(flattened) != expected:
            raise ValueError("process CLI batch inventory mismatch")
        print("process CLI inventory: " + json.dumps({
            "selected": expected, "batches": batches}), flush=True)
        reports, all_events, timeouts = [], [], []
        cleanup_failure = None
        for index, names in enumerate(batches, 1):
            if not names:
                continue
            selector = "^(" + "|".join(names) + ")$"
            test_started = time.monotonic()
            events, status, error = [], 0, None
            try:
                result = run_batch([go, "test", "-json", "-count=1", "-cpu=1",
                                         "-timeout=420s", "-run", selector, "./internal/app"],
                                        cwd=ROOT, env=env, text=True, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, timeout=480)
                status = result.returncode
                for line in result.stdout.splitlines():
                    event = json.loads(line)
                    if not isinstance(event, dict):
                        raise ValueError("non-object Go JSON event")
                    events.append(event)
                    if event.get("Output"):
                        print(event["Output"], end="", flush=True)
                if result.stderr:
                    print(result.stderr, end="", flush=True)
            except subprocess.TimeoutExpired as expired:
                status, error = 124, "Python test command timeout"
                timeouts.append((expired, {"batch": index,
                    "build_seconds": round(build_seconds, 3),
                    "precompile_seconds": round(compile_seconds, 3),
                    "test_command_seconds": round(time.monotonic() - test_started, 3),
                    "elapsed_seconds": round(time.monotonic() - started, 3),
                    "events": timeout_events(expired.stdout, names)}))
            except BatchCleanupError as exc:
                status, error, cleanup_failure = 1, str(exc), exc
                if exc.expired is not None:
                    timeouts.append((exc.expired, {"batch": index,
                        "test_command_seconds": round(time.monotonic() - test_started, 3),
                        "elapsed_seconds": round(time.monotonic() - started, 3),
                        "events": timeout_events(exc.expired.stdout, names)}))
            except OSError as exc:
                status, error = 1, str(exc)
                cleanup_failure = BatchCleanupError("batch ownership/cleanup uncertain: " + str(exc))
            except ValueError as exc:
                status, error = 1, str(exc)
                if isinstance(exc.__cause__, BatchCleanupError):
                    cleanup_failure = exc.__cause__
            seconds = time.monotonic() - test_started
            batch = strict_report(events, names, status)
            package_pass = any("Test" not in e and e.get("Action") == "pass" for e in events)
            batch.update(batch=index, test_command_seconds=round(seconds, 3), limit_seconds=420,
                         utilization_percent=round(seconds / 420 * 100, 3),
                         headroom_seconds=round(420 - seconds, 3), budget_warning=seconds > 315,
                         rejected=rejected(batch) or not package_pass, error=error)
            reports.append(batch)
            all_events.extend(events)
            print("process CLI batch summary: " + json.dumps(batch), flush=True)
            if seconds > 315:
                print(f"::warning::Process CLI batch {index} exceeded 75% of its 420s budget: "
                      f"elapsed={seconds:.3f}s headroom={420 - seconds:.3f}s", flush=True)
            if cleanup_failure is not None:
                break
        report = strict_report(all_events, expected, int(any(b["rejected"] for b in reports)))
        report.update(batches=reports, regression_test_seconds=round(sum(e.get("Elapsed", 0) for e in all_events
            if e.get("Test") == "TestDetachedClaudeChildSurvivesHostReadDeadlineCLI"
            and e.get("Action") == "pass"), 3),
            regression_build_seconds=round(regression_build_seconds, 3),
            build_seconds=round(build_seconds, 3), precompile_seconds=round(compile_seconds, 3),
            test_command_seconds=round(sum(b["test_command_seconds"] for b in reports), 3),
            elapsed_seconds=round(time.monotonic() - started, 3))
        failed = rejected(report)
        write_summary(report, failed)
        print(("process CLI failed summary: " if failed else "process CLI summary: ") + json.dumps(report), flush=True)
        for _, evidence in timeouts:
            print("process CLI timeout evidence: " + json.dumps(evidence), flush=True)
        if cleanup_failure is not None:
            raise cleanup_failure
        if timeouts:
            raise timeouts[0][0]
        if failed:
            raise ValueError("process CLI evidence rejected: " + json.dumps(report))


if __name__ == "__main__":
    main()
